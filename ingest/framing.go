// Package ingest is the one service per machine that connects to stations:
// it keeps one connection per address and hands the original bytes to every
// consumer on the machine (the Observatory's listeners, the engine). It
// decodes nothing. A consumer may join at any moment; for containers that
// cannot be decoded from the middle (Ogg, FLAC) the ingest keeps the
// stream's headers and sends them first.
package ingest

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

// A framer cuts a stream into the pieces handed to consumers and keeps the
// headers a consumer joining late needs before them. read blocks on the
// network and changes nothing; take records a piece that is about to be
// sent, and headers is what a consumer joining now gets first. The feed
// calls take, sends the piece and adds consumers under one lock, so that
// a header is never sent twice nor missed.
type framer interface {
	read() ([]byte, error)
	take(piece []byte)
	headers() [][]byte
}

// newFramer picks the framing for a stream from its first bytes.
func newFramer(r io.Reader) framer {
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(4)
	switch {
	case bytes.Equal(head, []byte("OggS")):
		return &oggFramer{r: br, serials: map[uint32]*oggStream{}}
	case bytes.Equal(head, []byte("fLaC")):
		return &flacFramer{r: br}
	}
	return &rawFramer{r: br}
}

// --- anything that can be joined anywhere (MP3, AAC in ADTS) ---

type rawFramer struct{ r io.Reader }

func (f *rawFramer) take([]byte) {}

func (f *rawFramer) read() ([]byte, error) {
	buf := make([]byte, 16<<10)
	n, err := f.r.Read(buf)
	if n > 0 {
		return buf[:n], nil
	}
	return nil, err
}

func (f *rawFramer) headers() [][]byte { return nil }

// --- Ogg: whole pages; the header pages of every logical stream ---

// An Ogg page: "OggS", version, header type, granule position (8), serial
// (4), sequence (4), CRC (4), segment count, segment table, body.
const oggHeaderLen = 27

const (
	oggContinued = 0x01
	oggBOS       = 0x02
	oggEOS       = 0x04
)

type oggStream struct {
	headersDone bool // its first page with a granule position has passed
}

type oggFramer struct {
	r       *bufio.Reader
	serials map[uint32]*oggStream
	cache   [][]byte // header pages of the current chain, in order
	data    bool     // pages after the headers have flowed in this chain
}

var errNotOgg = errors.New("ogg: lost the page boundary")

func (f *oggFramer) read() ([]byte, error) { return f.readPage() }

func (f *oggFramer) take(page []byte) {
	typ := page[5]
	granule := binary.LittleEndian.Uint64(page[6:14])
	serial := binary.LittleEndian.Uint32(page[14:18])
	if typ&oggBOS != 0 {
		if f.data {
			// A new chain (Ogg chaining, e.g. on a change of title): its
			// headers replace the old ones.
			f.cache, f.serials, f.data = nil, map[uint32]*oggStream{}, false
		}
		f.serials[serial] = &oggStream{}
	}
	st := f.serials[serial]
	switch {
	case st == nil:
		// A page of a stream whose start we never saw: joined mid-stream
		// ourselves; nothing to cache for it.
		f.data = true
	case !st.headersDone && (granule == 0 || granule == ^uint64(0)):
		f.cache = append(f.cache, page)
	default:
		st.headersDone = true
		f.data = true
	}
	if typ&oggEOS != 0 {
		delete(f.serials, serial)
	}
}

// readPage reads one whole page, finding the next "OggS" if the stream
// does not start at one.
func (f *oggFramer) readPage() ([]byte, error) {
	for skipped := 0; ; skipped++ {
		head, err := f.r.Peek(oggHeaderLen)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(head[:4], []byte("OggS")) && head[4] == 0 {
			break
		}
		if skipped > 1<<20 {
			return nil, errNotOgg
		}
		f.r.Discard(1)
	}
	head, _ := f.r.Peek(oggHeaderLen)
	nseg := int(head[26])
	full, err := f.r.Peek(oggHeaderLen + nseg)
	if err != nil {
		return nil, err
	}
	body := 0
	for _, l := range full[oggHeaderLen:] {
		body += int(l)
	}
	page := make([]byte, oggHeaderLen+nseg+body)
	if _, err := io.ReadFull(f.r, page); err != nil {
		return nil, err
	}
	return page, nil
}

func (f *oggFramer) headers() [][]byte { return f.cache }

// --- raw FLAC: "fLaC" and the metadata blocks ---

type flacFramer struct {
	r      *bufio.Reader
	read1  bool   // the header was read
	header []byte // taken
	carry  []byte // audio read but not yet cut at a frame
}

func (f *flacFramer) take(piece []byte) {
	if f.header == nil {
		f.header = piece // the first piece is the header
	}
}

func (f *flacFramer) read() ([]byte, error) {
	if !f.read1 {
		// "fLaC", then metadata blocks: a 4-byte header each (bit 7 of the
		// first byte marks the last), then the block.
		header := make([]byte, 4)
		if _, err := io.ReadFull(f.r, header); err != nil {
			return nil, err
		}
		for {
			bh := make([]byte, 4)
			if _, err := io.ReadFull(f.r, bh); err != nil {
				return nil, err
			}
			n := int(bh[1])<<16 | int(bh[2])<<8 | int(bh[3])
			block := make([]byte, n)
			if _, err := io.ReadFull(f.r, block); err != nil {
				return nil, err
			}
			header = append(header, bh...)
			header = append(header, block...)
			if bh[0]&0x80 != 0 {
				break
			}
		}
		f.read1 = true
		return header, nil
	}
	// Audio: pieces cut at frame starts, so that a consumer joining late
	// gets the header and then a whole frame.
	for {
		if len(f.carry) > 0 {
			if cut := lastFrameStart(f.carry); cut > 0 {
				piece := f.carry[:cut]
				f.carry = append([]byte(nil), f.carry[cut:]...)
				return piece, nil
			}
		}
		buf := make([]byte, 16<<10)
		n, err := f.r.Read(buf)
		f.carry = append(f.carry, buf[:n]...)
		if err != nil {
			if len(f.carry) > 0 {
				piece := f.carry
				f.carry = nil
				return piece, nil
			}
			return nil, err
		}
		if len(f.carry) > 1<<20 {
			// No frame start found in a megabyte: pass it on as it is.
			piece := f.carry
			f.carry = nil
			return piece, nil
		}
	}
}

// lastFrameStart is the offset of the last FLAC frame header in b after
// offset 0 whose CRC-8 checks, or 0. A frame header starts with the sync
// code 0xFFF8 or 0xFFF9; the CRC rules out the same bits in audio data.
func lastFrameStart(b []byte) int {
	for i := len(b) - 2; i > 0; i-- {
		if b[i] == 0xFF && (b[i+1] == 0xF8 || b[i+1] == 0xF9) && flacHeaderOK(b[i:]) {
			return i
		}
	}
	return 0
}

// flacHeaderOK checks a frame header at the start of b: its fields and its
// CRC-8 (polynomial 0x07). Too few bytes to tell is not OK.
func flacHeaderOK(b []byte) bool {
	if len(b) < 6 {
		return false
	}
	bs, sr := b[2]>>4, b[2]&0x0F
	if bs == 0 || sr == 0x0F || b[3]&0x01 != 0 || (b[3]>>1)&0x07 == 3 || (b[3]>>1)&0x07 == 7 || b[3]>>4 > 10 {
		return false
	}
	n := 4
	// The frame or sample number, UTF-8 coded in 1 to 7 bytes.
	lead := b[n]
	extra := 0
	switch {
	case lead&0x80 == 0:
	case lead&0xE0 == 0xC0:
		extra = 1
	case lead&0xF0 == 0xE0:
		extra = 2
	case lead&0xF8 == 0xF0:
		extra = 3
	case lead&0xFC == 0xF8:
		extra = 4
	case lead&0xFE == 0xFC:
		extra = 5
	case lead == 0xFE:
		extra = 6
	default:
		return false
	}
	n += 1 + extra
	switch bs {
	case 6:
		n++
	case 7:
		n += 2
	}
	switch sr {
	case 12:
		n++
	case 13, 14:
		n += 2
	}
	if len(b) < n+1 {
		return false
	}
	var crc byte
	for _, x := range b[:n] {
		crc ^= x
		for k := 0; k < 8; k++ {
			if crc&0x80 != 0 {
				crc = crc<<1 ^ 0x07
			} else {
				crc <<= 1
			}
		}
	}
	return crc == b[n]
}

func (f *flacFramer) headers() [][]byte {
	if f.header == nil {
		return nil
	}
	return [][]byte{f.header}
}
