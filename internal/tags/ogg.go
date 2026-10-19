package tags

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

var errNotOgg = errors.New("tags: not an Ogg file")

// readOgg reads the identification and comment packets of an Ogg Vorbis or
// Opus stream, and the duration from the granule position of the last page.
func readOgg(r io.ReadSeeker, size int64, wantPic bool) (Info, *Picture, error) {
	var in Info
	var pic *Picture
	br := bufio.NewReader(r)
	packets, err := oggPackets(br, 2, 32<<20)
	if err != nil || len(packets) < 2 {
		if err == nil {
			err = errNotOgg
		}
		return in, nil, err
	}
	id, comment := packets[0], packets[1]
	var rate, preskip int64
	switch {
	case bytes.HasPrefix(id, []byte("\x01vorbis")) && len(id) >= 16:
		rate = int64(binary.LittleEndian.Uint32(id[12:16]))
		if bytes.HasPrefix(comment, []byte("\x03vorbis")) {
			readVorbisComment(comment[7:], &in, wantPic, &pic)
		}
	case bytes.HasPrefix(id, []byte("OpusHead")) && len(id) >= 12:
		rate = 48000 // Opus granule positions are always 48 kHz
		preskip = int64(binary.LittleEndian.Uint16(id[10:12]))
		if bytes.HasPrefix(comment, []byte("OpusTags")) {
			readVorbisComment(comment[8:], &in, wantPic, &pic)
		}
	default:
		return in, nil, ErrUnsupported
	}
	if g := lastGranule(r, size); g > preskip && rate > 0 {
		in.Duration = float64(g-preskip) / float64(rate)
	}
	return in, pic, nil
}

// oggPackets reassembles the first n packets of the (first) logical stream.
func oggPackets(r io.Reader, n int, limit int) ([][]byte, error) {
	var packets [][]byte
	var cur []byte
	var serial uint32
	first := true
	for len(packets) < n {
		var h [27]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return packets, err
		}
		if string(h[:4]) != "OggS" {
			return packets, errNotOgg
		}
		s := binary.LittleEndian.Uint32(h[14:18])
		segs := make([]byte, h[26])
		if _, err := io.ReadFull(r, segs); err != nil {
			return packets, err
		}
		total := 0
		for _, l := range segs {
			total += int(l)
		}
		body := make([]byte, total)
		if _, err := io.ReadFull(r, body); err != nil {
			return packets, err
		}
		if first {
			serial, first = s, false
		} else if s != serial {
			continue // interleaved stream (e.g. a video track); ignore
		}
		off := 0
		for _, l := range segs {
			cur = append(cur, body[off:off+int(l)]...)
			off += int(l)
			if len(cur) > limit {
				return packets, errors.New("tags: ogg packet too large")
			}
			if l < 255 {
				packets = append(packets, cur)
				cur = nil
				if len(packets) == n {
					break
				}
			}
		}
	}
	return packets, nil
}

func lastGranule(r io.ReadSeeker, size int64) int64 {
	n := int64(64 << 10)
	if n > size {
		n = size
	}
	if _, err := r.Seek(size-n, io.SeekStart); err != nil {
		return 0
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return 0
	}
	i := bytes.LastIndex(b, []byte("OggS"))
	if i < 0 || i+14 > len(b) {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(b[i+6 : i+14]))
}
