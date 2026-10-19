package tags

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

var errNotFLAC = errors.New("tags: not a FLAC file")

func readFLAC(r io.ReadSeeker, size int64, wantPic bool) (Info, *Picture, error) {
	var in Info
	var pic *Picture
	// Some taggers put an ID3v2 tag in front of FLAC; skip it.
	start, err := readID3v2(r, &Info{}, false, new(*Picture))
	if err != nil {
		return in, nil, err
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return in, nil, err
	}
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || string(magic[:]) != "fLaC" {
		return in, nil, errNotFLAC
	}
	for {
		var h [4]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return in, pic, nil
		}
		last, typ := h[0]&0x80 != 0, h[0]&0x7f
		n := int64(h[1])<<16 | int64(h[2])<<8 | int64(h[3])
		switch {
		case typ == 0 || typ == 4 || (typ == 6 && wantPic && n <= maxPicture):
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil {
				return in, pic, nil
			}
			switch typ {
			case 0:
				if len(b) >= 18 {
					rate := int(b[10])<<12 | int(b[11])<<4 | int(b[12])>>4
					samples := int64(b[13]&0x0f)<<32 | int64(binary.BigEndian.Uint32(b[14:18]))
					if rate > 0 {
						in.Duration = float64(samples) / float64(rate)
					}
				}
			case 4:
				readVorbisComment(b, &in, wantPic, &pic)
			case 6:
				in.HasPicture = true
				if p := parseFLACPicture(b); p != nil && (pic == nil || p.front) {
					pic = &p.Picture
				}
			}
		default:
			if typ == 6 {
				in.HasPicture = true
			}
			if _, err := r.Seek(n, io.SeekCurrent); err != nil {
				return in, pic, nil
			}
		}
		if last {
			return in, pic, nil
		}
	}
}

// readVorbisComment parses a Vorbis comment block (FLAC, Ogg Vorbis, Opus).
func readVorbisComment(b []byte, in *Info, wantPic bool, pic **Picture) {
	rd := bytes.NewReader(b)
	var n uint32
	if binary.Read(rd, binary.LittleEndian, &n) != nil || int64(n) > int64(rd.Len()) {
		return
	}
	rd.Seek(int64(n), io.SeekCurrent) // vendor string
	var count uint32
	if binary.Read(rd, binary.LittleEndian, &count) != nil {
		return
	}
	for i := uint32(0); i < count; i++ {
		if binary.Read(rd, binary.LittleEndian, &n) != nil || int64(n) > int64(rd.Len()) {
			return
		}
		kv := make([]byte, n)
		io.ReadFull(rd, kv)
		k, v, ok := strings.Cut(string(kv), "=")
		if !ok {
			continue
		}
		if strings.EqualFold(k, "METADATA_BLOCK_PICTURE") {
			in.HasPicture = true
			if wantPic {
				if raw, err := base64.StdEncoding.DecodeString(v); err == nil {
					if p := parseFLACPicture(raw); p != nil && (*pic == nil || p.front) {
						*pic = &p.Picture
					}
				}
			}
			continue
		}
		in.setField(k, v)
	}
}

func parseFLACPicture(b []byte) *apic {
	rd := bytes.NewReader(b)
	var ptype, n uint32
	if binary.Read(rd, binary.BigEndian, &ptype) != nil || binary.Read(rd, binary.BigEndian, &n) != nil || int64(n) > int64(rd.Len()) {
		return nil
	}
	mime := make([]byte, n)
	io.ReadFull(rd, mime)
	if binary.Read(rd, binary.BigEndian, &n) != nil || int64(n) > int64(rd.Len()) {
		return nil
	}
	rd.Seek(int64(n)+16, io.SeekCurrent) // description, width, height, depth, colors
	if binary.Read(rd, binary.BigEndian, &n) != nil || int64(n) > int64(rd.Len()) || n == 0 {
		return nil
	}
	data := make([]byte, n)
	io.ReadFull(rd, data)
	m := string(mime)
	if !strings.Contains(m, "/") {
		m = sniffImage(data)
	}
	return &apic{Picture{MIME: m, Data: data}, ptype == 3}
}
