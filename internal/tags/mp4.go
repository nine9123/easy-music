package tags

import (
	"encoding/binary"
	"errors"
	"io"
)

var errNotMP4 = errors.New("tags: not an MP4 file")

type readerAtSeeker interface {
	io.ReaderAt
	io.ReadSeeker
}

// readMP4 walks the atom tree for moov/mvhd (duration) and
// moov/udta/meta/ilst (iTunes-style tags). It seeks over everything else, so
// a large mdat costs nothing.
func readMP4(r io.ReadSeeker, size int64, wantPic bool) (Info, *Picture, error) {
	ra, ok := r.(readerAtSeeker)
	if !ok {
		return Info{}, nil, ErrUnsupported
	}
	var in Info
	var pic *Picture
	found := false
	err := walkAtoms(ra, 0, size, func(typ string, off, end int64) error {
		if typ != "moov" {
			return nil
		}
		found = true
		return walkAtoms(ra, off, end, func(typ string, off, end int64) error {
			switch typ {
			case "mvhd":
				in.Duration = mvhdDuration(ra, off, end)
			case "udta":
				return walkAtoms(ra, off, end, func(typ string, off, end int64) error {
					if typ != "meta" {
						return nil
					}
					// meta is a "full box" (4 bytes version/flags) in MP4 but
					// not in QuickTime; tell them apart by peeking for hdlr.
					var peek [8]byte
					if _, err := ra.ReadAt(peek[:], off); err == nil && string(peek[4:8]) != "hdlr" {
						off += 4
					}
					return walkAtoms(ra, off, end, func(typ string, off, end int64) error {
						if typ != "ilst" {
							return nil
						}
						return walkAtoms(ra, off, end, func(item string, off, end int64) error {
							return readIlstItem(ra, item, off, end, &in, wantPic, &pic)
						})
					})
				})
			}
			return nil
		})
	})
	if !found && err == nil {
		err = errNotMP4
	}
	return in, pic, err
}

// walkAtoms calls fn for each atom in [start, end) with the bounds of its
// payload.
func walkAtoms(r io.ReaderAt, start, end int64, fn func(typ string, off, end int64) error) error {
	for pos := start; pos+8 <= end; {
		var h [16]byte
		if _, err := r.ReadAt(h[:8], pos); err != nil {
			return nil
		}
		size := int64(binary.BigEndian.Uint32(h[:4]))
		typ := string(h[4:8])
		hdr := int64(8)
		switch size {
		case 0:
			size = end - pos
		case 1:
			if _, err := r.ReadAt(h[8:16], pos+8); err != nil {
				return nil
			}
			size = int64(binary.BigEndian.Uint64(h[8:16]))
			hdr = 16
		}
		if size < hdr || pos+size > end {
			return nil // truncated or garbage; stop quietly
		}
		if err := fn(typ, pos+hdr, pos+size); err != nil {
			return err
		}
		pos += size
	}
	return nil
}

func mvhdDuration(r io.ReaderAt, off, end int64) float64 {
	var b [32]byte
	n := end - off
	if n > 32 {
		n = 32
	}
	if _, err := r.ReadAt(b[:n], off); err != nil || n < 20 {
		return 0
	}
	var scale, dur uint64
	if b[0] == 1 && n >= 32 {
		scale = uint64(binary.BigEndian.Uint32(b[20:24]))
		dur = binary.BigEndian.Uint64(b[24:32])
	} else {
		scale = uint64(binary.BigEndian.Uint32(b[12:16]))
		dur = uint64(binary.BigEndian.Uint32(b[16:20]))
	}
	if scale == 0 {
		return 0
	}
	return float64(dur) / float64(scale)
}

func readIlstItem(r io.ReaderAt, item string, off, end int64, in *Info, wantPic bool, pic **Picture) error {
	return walkAtoms(r, off, end, func(typ string, off, end int64) error {
		if typ != "data" || end-off < 8 {
			return nil
		}
		n := end - off - 8
		if item == "covr" {
			in.HasPicture = true
			if !wantPic || *pic != nil || n > maxPicture {
				return nil
			}
		} else if n > 64<<10 {
			return nil
		}
		b := make([]byte, end-off)
		if _, err := r.ReadAt(b, off); err != nil {
			return nil
		}
		kind, payload := binary.BigEndian.Uint32(b[:4])&0xffffff, b[8:]
		switch item {
		case "\xa9nam":
			in.setField("TITLE", string(payload))
		case "\xa9ART":
			in.setField("ARTIST", string(payload))
		case "\xa9alb":
			in.setField("ALBUM", string(payload))
		case "aART":
			in.setField("ALBUMARTIST", string(payload))
		case "\xa9gen":
			in.setField("GENRE", string(payload))
		case "gnre":
			if len(payload) >= 2 {
				in.setField("GENRE", genreByIndex(int(binary.BigEndian.Uint16(payload))-1))
			}
		case "\xa9day":
			in.setField("DATE", string(payload))
		case "trkn":
			if len(payload) >= 4 && in.Track == 0 {
				in.Track = int(binary.BigEndian.Uint16(payload[2:4]))
			}
		case "disk":
			if len(payload) >= 4 && in.Disc == 0 {
				in.Disc = int(binary.BigEndian.Uint16(payload[2:4]))
			}
		case "covr":
			mime := "image/jpeg"
			if kind == 14 {
				mime = "image/png"
			} else if kind != 13 {
				mime = sniffImage(payload)
			}
			*pic = &Picture{MIME: mime, Data: payload}
		}
		return nil
	})
}
