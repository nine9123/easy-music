package tags

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

// readMP3 reads ID3v2 (2.2–2.4), falls back to ID3v1, and estimates the
// duration from the Xing/VBRI header or, for CBR files, the bitrate.
func readMP3(r io.ReadSeeker, size int64, wantPic bool) (Info, *Picture, error) {
	var in Info
	var pic *Picture
	tagEnd, err := readID3v2(r, &in, wantPic, &pic)
	if err != nil {
		return in, pic, err
	}
	if in.Title == "" || in.Artist == "" {
		readID3v1(r, size, &in)
	}
	if in.Duration == 0 {
		in.Duration = mp3Duration(r, tagEnd, size)
	}
	return in, pic, nil
}

// readID3v2 parses an ID3v2 tag at the current start of r, if any, and
// returns the offset where audio begins (0 without a tag).
func readID3v2(r io.ReadSeeker, in *Info, wantPic bool, pic **Picture) (int64, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	var hdr [10]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil || string(hdr[:3]) != "ID3" {
		return 0, nil
	}
	ver, flags := hdr[3], hdr[5]
	size := int64(synchsafe(hdr[6:10]))
	end := 10 + size
	if flags&0x10 != 0 {
		end += 10 // footer
	}
	if ver < 2 || ver > 4 || size > 64<<20 {
		return end, nil
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return end, nil
	}
	if flags&0x80 != 0 && ver < 4 {
		data = unsync(data)
	}
	if flags&0x40 != 0 && ver == 2 {
		return end, nil // v2.2 has no extended header: 0x40 is compression
	}
	if flags&0x40 != 0 && len(data) >= 4 {
		// Extended header: v2.4 size includes itself and is synchsafe; v2.3
		// excludes its own 4 size bytes.
		var n int
		if ver == 4 {
			n = int(synchsafe(data[:4]))
		} else {
			n = int(binary.BigEndian.Uint32(data[:4])) + 4
		}
		if n > len(data) {
			return end, nil
		}
		data = data[n:]
	}

	idLen, hdrLen := 4, 10
	if ver == 2 {
		idLen, hdrLen = 3, 6
	}
	for len(data) >= hdrLen && data[0] != 0 {
		id := string(data[:idLen])
		var fsize int
		var fflags uint16
		switch ver {
		case 2:
			fsize = int(data[3])<<16 | int(data[4])<<8 | int(data[5])
		case 3:
			fsize = int(binary.BigEndian.Uint32(data[4:8]))
			fflags = binary.BigEndian.Uint16(data[8:10])
		case 4:
			fsize = int(synchsafe(data[4:8]))
			fflags = binary.BigEndian.Uint16(data[8:10])
		}
		if fsize < 0 || hdrLen+fsize > len(data) {
			break
		}
		body := data[hdrLen : hdrLen+fsize]
		data = data[hdrLen+fsize:]
		if ver == 4 && fflags&0x0040 != 0 && len(body) >= 1 {
			body = body[1:] // group id, ahead of the data length indicator
		}
		if ver == 4 && fflags&0x0002 != 0 {
			body = unsync(body)
		}
		if ver == 4 && fflags&0x0001 != 0 && len(body) >= 4 {
			body = body[4:] // data length indicator
		}
		// Compressed or encrypted frames: skip rather than misread.
		if (ver == 3 && fflags&0x00C0 != 0) || (ver == 4 && fflags&0x000C != 0) {
			continue
		}
		if ver == 3 && fflags&0x0020 != 0 && len(body) >= 1 {
			body = body[1:] // group id
		}
		applyID3Frame(in, id, body, wantPic, pic)
	}
	return end, nil
}

func applyID3Frame(in *Info, id string, body []byte, wantPic bool, pic **Picture) {
	switch id {
	case "TIT2", "TT2":
		in.setField("TITLE", id3Text(body))
	case "TPE1", "TP1":
		in.setField("ARTIST", id3Text(body))
	case "TALB", "TAL":
		in.setField("ALBUM", id3Text(body))
	case "TPE2", "TP2":
		in.setField("ALBUMARTIST", id3Text(body))
	case "TCON", "TCO":
		in.setField("GENRE", resolveID3Genre(id3Text(body)))
	case "TDRC", "TYER", "TYE", "TDOR", "TORY", "TDRL":
		in.setField("DATE", id3Text(body))
	case "TRCK", "TRK":
		in.setField("TRACKNUMBER", id3Text(body))
	case "TPOS", "TPA":
		in.setField("DISCNUMBER", id3Text(body))
	case "TLEN", "TLE":
		if ms, err := strconv.Atoi(strings.TrimSpace(id3Text(body))); err == nil && ms > 0 {
			in.Duration = float64(ms) / 1000
		}
	case "APIC", "PIC":
		in.HasPicture = true
		if wantPic {
			if p := parseAPIC(body, id == "PIC"); p != nil && (*pic == nil || p.front) {
				*pic = &p.Picture
			}
		}
	}
}

type apic struct {
	Picture
	front bool
}

func parseAPIC(b []byte, v22 bool) *apic {
	if len(b) < 4 {
		return nil
	}
	enc := b[0]
	b = b[1:]
	var mime string
	if v22 {
		switch strings.ToUpper(string(b[:3])) {
		case "PNG":
			mime = "image/png"
		default:
			mime = "image/jpeg"
		}
		b = b[3:]
	} else {
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			return nil
		}
		mime = string(b[:i])
		b = b[i+1:]
	}
	if len(b) < 1 {
		return nil
	}
	ptype := b[0]
	b = b[1:]
	_, b = splitEncoded(enc, b) // description
	if len(b) == 0 || len(b) > maxPicture {
		return nil
	}
	if mime == "" || !strings.Contains(mime, "/") {
		mime = sniffImage(b)
	}
	return &apic{Picture{MIME: mime, Data: append([]byte(nil), b...)}, ptype == 3}
}

func sniffImage(b []byte) string {
	if bytes.HasPrefix(b, []byte("\x89PNG")) {
		return "image/png"
	}
	return "image/jpeg"
}

// splitEncoded splits a null-terminated string in the given ID3 encoding off
// the front of b.
func splitEncoded(enc byte, b []byte) (string, []byte) {
	if enc == 1 || enc == 2 {
		for i := 0; i+1 < len(b); i += 2 {
			if b[i] == 0 && b[i+1] == 0 {
				return decodeID3(enc, b[:i]), b[i+2:]
			}
		}
		return decodeID3(enc, b), nil
	}
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return decodeID3(enc, b[:i]), b[i+1:]
	}
	return decodeID3(enc, b), nil
}

// id3Text decodes a text frame. Multiple values (v2.4 null separators) are
// joined with "; ".
func id3Text(b []byte) string {
	if len(b) < 1 {
		return ""
	}
	enc := b[0]
	b = b[1:]
	var parts []string
	for len(b) > 0 {
		var s string
		s, b = splitEncoded(enc, b)
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "; ")
}

func decodeID3(enc byte, b []byte) string {
	switch enc {
	case 0: // ISO-8859-1
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		return string(r)
	case 1, 2: // UTF-16 with BOM / UTF-16BE
		be := enc == 2
		if len(b) >= 2 {
			if b[0] == 0xFF && b[1] == 0xFE {
				be, b = false, b[2:]
			} else if b[0] == 0xFE && b[1] == 0xFF {
				be, b = true, b[2:]
			}
		}
		u := make([]uint16, len(b)/2)
		for i := range u {
			if be {
				u[i] = binary.BigEndian.Uint16(b[2*i:])
			} else {
				u[i] = binary.LittleEndian.Uint16(b[2*i:])
			}
		}
		return string(utf16.Decode(u))
	default: // UTF-8
		return string(b)
	}
}

func synchsafe(b []byte) uint32 {
	return uint32(b[0]&0x7f)<<21 | uint32(b[1]&0x7f)<<14 | uint32(b[2]&0x7f)<<7 | uint32(b[3]&0x7f)
}

// unsync reverses ID3 unsynchronisation (0xFF 0x00 -> 0xFF).
func unsync(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		out = append(out, b[i])
		if b[i] == 0xFF && i+1 < len(b) && b[i+1] == 0 {
			i++
		}
	}
	return out
}

func readID3v1(r io.ReadSeeker, size int64, in *Info) {
	if size < 128 {
		return
	}
	var b [128]byte
	if _, err := r.Seek(size-128, io.SeekStart); err != nil {
		return
	}
	if _, err := io.ReadFull(r, b[:]); err != nil || string(b[:3]) != "TAG" {
		return
	}
	field := func(s []byte) string { return decodeID3(0, bytes.TrimRight(s, "\x00 ")) }
	in.setField("TITLE", field(b[3:33]))
	in.setField("ARTIST", field(b[33:63]))
	in.setField("ALBUM", field(b[63:93]))
	in.setField("DATE", field(b[93:97]))
	if b[125] == 0 && b[126] != 0 {
		in.setField("TRACKNUMBER", strconv.Itoa(int(b[126])))
	}
	in.setField("GENRE", genreByIndex(int(b[127])))
}

var errNoFrame = errors.New("no mpeg frame")

// mp3Duration finds the first MPEG audio frame after the tag and derives the
// duration from a Xing/Info or VBRI header, or from the bitrate for CBR.
func mp3Duration(r io.ReadSeeker, start, size int64) float64 {
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return 0
	}
	buf := make([]byte, 64<<10)
	n, _ := io.ReadFull(r, buf)
	buf = buf[:n]
	for i := 0; i+4 <= len(buf); i++ {
		if buf[i] != 0xFF || buf[i+1]&0xE0 != 0xE0 {
			continue
		}
		fh, err := parseFrameHeader(buf[i : i+4])
		if err != nil {
			continue
		}
		frame := buf[i:]
		// Xing/Info: after the side information.
		if off := 4 + fh.sideInfo; off+16 <= len(frame) {
			tag := string(frame[off : off+4])
			if tag == "Xing" || tag == "Info" {
				flags := binary.BigEndian.Uint32(frame[off+4:])
				if flags&1 != 0 {
					frames := binary.BigEndian.Uint32(frame[off+8:])
					return float64(frames) * float64(fh.samples) / float64(fh.rate)
				}
			}
		}
		// VBRI: fixed 32 bytes after the header.
		if 36+18 <= len(frame) && string(frame[36:40]) == "VBRI" {
			frames := binary.BigEndian.Uint32(frame[36+14:])
			return float64(frames) * float64(fh.samples) / float64(fh.rate)
		}
		audio := size - start - int64(i)
		if size >= 128 {
			audio -= 128 // possible ID3v1; the error is negligible either way
		}
		return float64(audio) * 8 / float64(fh.bitrate)
	}
	return 0
}

type frameHeader struct {
	bitrate  int // bits/s
	rate     int // Hz
	samples  int // per frame
	sideInfo int // bytes
}

func parseFrameHeader(h []byte) (frameHeader, error) {
	verBits := (h[1] >> 3) & 3 // 0: 2.5, 2: v2, 3: v1
	layer := (h[1] >> 1) & 3   // 1: III, 2: II, 3: I
	brIdx := h[2] >> 4
	srIdx := (h[2] >> 2) & 3
	mono := h[3]>>6 == 3
	if verBits == 1 || layer == 0 || brIdx == 0 || brIdx == 15 || srIdx == 3 {
		return frameHeader{}, errNoFrame
	}
	v1 := verBits == 3
	rates := [3]int{44100, 48000, 32000}
	rate := rates[srIdx]
	switch verBits {
	case 2:
		rate /= 2
	case 0:
		rate /= 4
	}
	// layer bits: 1 = Layer III, 2 = Layer II, 3 = Layer I.
	var table [16]int
	switch {
	case v1 && layer == 3:
		table = [16]int{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448}
	case v1 && layer == 2:
		table = [16]int{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384}
	case v1:
		table = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	case layer == 3:
		table = [16]int{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256}
	default:
		table = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
	}
	fh := frameHeader{bitrate: table[brIdx] * 1000, rate: rate}
	switch {
	case layer == 3:
		fh.samples = 384
	case layer == 2 || v1:
		fh.samples = 1152
	default:
		fh.samples = 576
	}
	if layer == 1 { // Layer III
		switch {
		case v1 && mono:
			fh.sideInfo = 17
		case v1:
			fh.sideInfo = 32
		case mono:
			fh.sideInfo = 9
		default:
			fh.sideInfo = 17
		}
	}
	return fh, nil
}
