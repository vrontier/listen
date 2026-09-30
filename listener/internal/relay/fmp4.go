// Package relay passes the encoded audio from ffmpeg on to browsers, cut
// into fragmented-MP4 segments that carry their position on the listener's
// audio timeline, so the page can play them in step with the events.
package relay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Splitter reads a fragmented MP4 stream (ftyp, moov, then moof+mdat pairs)
// and reports the initialisation segment and each media fragment.
type Splitter struct {
	OnInit     func(init []byte, timescale uint32)
	OnFragment func(frag []byte, baseMediaTime uint64)
}

const maxBox = 16 << 20

// Run reads until EOF.
func (s Splitter) Run(r io.Reader) error {
	var init []byte
	var timescale uint32
	var moof []byte
	var base uint64
	for {
		box, typ, err := readBox(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch typ {
		case "ftyp":
			init = append(init[:0], box...)
		case "moov":
			init = append(init, box...)
			ts, err := mdhdTimescale(box[8:])
			if err != nil {
				return err
			}
			timescale = ts
			s.OnInit(append([]byte(nil), init...), timescale)
		case "moof":
			moof = box
			base, err = tfdtBase(box[8:])
			if err != nil {
				return err
			}
		case "mdat":
			if moof == nil {
				continue
			}
			frag := make([]byte, 0, len(moof)+len(box))
			frag = append(append(frag, moof...), box...)
			s.OnFragment(frag, base)
			moof = nil
		}
	}
}

// readBox reads one top-level box including its header.
func readBox(r io.Reader) ([]byte, string, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, "", err
	}
	size := uint64(binary.BigEndian.Uint32(hdr[:4]))
	typ := string(hdr[4:8])
	head := hdr[:]
	if size == 1 {
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, "", err
		}
		size = binary.BigEndian.Uint64(ext[:])
		head = append(append([]byte(nil), hdr[:]...), ext[:]...)
	}
	if size < uint64(len(head)) || size > maxBox {
		return nil, "", fmt.Errorf("fmp4: bad %q box size %d", typ, size)
	}
	box := make([]byte, size)
	copy(box, head)
	if _, err := io.ReadFull(r, box[len(head):]); err != nil {
		return nil, "", err
	}
	return box, typ, nil
}

// child returns the payload of the first child box of type typ.
func child(b []byte, typ string) ([]byte, bool) {
	for len(b) >= 8 {
		size := int(binary.BigEndian.Uint32(b[:4]))
		hdr := 8
		if size == 1 && len(b) >= 16 {
			size, hdr = int(binary.BigEndian.Uint64(b[8:16])), 16
		}
		if size < hdr || size > len(b) {
			return nil, false
		}
		if string(b[4:8]) == typ {
			return b[hdr:size], true
		}
		b = b[size:]
	}
	return nil, false
}

func path(b []byte, types ...string) ([]byte, bool) {
	for _, t := range types {
		var ok bool
		if b, ok = child(b, t); !ok {
			return nil, false
		}
	}
	return b, true
}

// mdhdTimescale reads moov/trak/mdia/mdhd.timescale from a moov payload.
func mdhdTimescale(moov []byte) (uint32, error) {
	m, ok := path(moov, "trak", "mdia", "mdhd")
	if !ok || len(m) < 24 {
		return 0, errors.New("fmp4: no mdhd")
	}
	if m[0] == 1 { // version 1: 64-bit times
		if len(m) < 32 {
			return 0, errors.New("fmp4: short mdhd")
		}
		return binary.BigEndian.Uint32(m[20:24]), nil
	}
	return binary.BigEndian.Uint32(m[12:16]), nil
}

// tfdtBase reads moof/traf/tfdt.baseMediaDecodeTime from a moof payload.
func tfdtBase(moof []byte) (uint64, error) {
	t, ok := path(moof, "traf", "tfdt")
	if !ok || len(t) < 8 {
		return 0, errors.New("fmp4: no tfdt")
	}
	if t[0] == 1 {
		if len(t) < 12 {
			return 0, errors.New("fmp4: short tfdt")
		}
		return binary.BigEndian.Uint64(t[4:12]), nil
	}
	return uint64(binary.BigEndian.Uint32(t[4:8])), nil
}
