package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	librespot "github.com/devgianlu/go-librespot"
)

const spotifyOggHeaderEnd = 0xa7

type oggPage struct {
	headerType byte
	granule    uint64
	serial     uint32
	lacing     []byte
	payload    []byte
}

type oggPacket struct {
	data       []byte
	granule    uint64
	serial     uint32
	lastInPage bool
	eos        bool
}

type oggPacketReader struct {
	r       io.Reader
	current []byte
	queue   []*oggPacket
}

func (r *oggPacketReader) next() (*oggPacket, error) {
	for {
		if len(r.queue) > 0 {
			packet := r.queue[0]
			r.queue = r.queue[1:]
			return packet, nil
		}
		page, err := readOggPage(r.r)
		if err != nil {
			return nil, err
		}
		at := 0
		for i, lace := range page.lacing {
			end := at + int(lace)
			if end > len(page.payload) {
				return nil, errors.New("Ogg page has a broken segment table")
			}
			r.current = append(r.current, page.payload[at:end]...)
			at = end
			if lace < 255 {
				last := i == len(page.lacing)-1
				r.queue = append(r.queue, &oggPacket{
					data:       r.current,
					granule:    page.granule,
					serial:     page.serial,
					lastInPage: last,
					eos:        last && page.headerType&0x04 != 0,
				})
				r.current = nil
			}
		}
	}
}

func readOggPage(r io.Reader) (*oggPage, error) {
	var head [27]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	if string(head[:4]) != "OggS" || head[4] != 0 {
		return nil, errors.New("not an Ogg page")
	}
	n := int(head[26])
	lacing := make([]byte, n)
	if _, err := io.ReadFull(r, lacing); err != nil {
		return nil, err
	}
	total := 0
	for _, lace := range lacing {
		total += int(lace)
	}
	payload := make([]byte, total)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return &oggPage{
		headerType: head[5],
		granule:    binary.LittleEndian.Uint64(head[6:14]),
		serial:     binary.LittleEndian.Uint32(head[14:18]),
		lacing:     lacing,
		payload:    payload,
	}, nil
}

func remuxOgg(src librespot.SizedReadAtSeeker, out io.Writer) error {
	start, err := findOggStart(src)
	if err != nil {
		return err
	}
	section := io.NewSectionReader(src, start, src.Size()-start)
	reader := &oggPacketReader{r: section}
	ident, err := readVorbisHeader(reader, 1)
	if err != nil {
		return err
	}
	comment, err := readVorbisHeader(reader, 3)
	if err != nil {
		return err
	}
	setup, err := readVorbisHeader(reader, 5)
	if err != nil {
		return err
	}

	writer := newOggWriter(out, uint32(time.Now().UnixNano()))
	if err := writer.writePacket(ident, 0, true, false); err != nil {
		return err
	}
	if err := writer.writePacket(comment, 0, false, false); err != nil {
		return err
	}
	if err := writer.writePacket(setup, 0, true, false); err != nil {
		return err
	}

	var pending *oggPacket
	for {
		packet, err := reader.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read Spotify Ogg audio: %w", err)
		}
		if packet.granule == 0 {
			continue
		}
		if pending != nil {
			if err := writer.writePacket(pending.data, pending.granule, pending.lastInPage, pending.eos); err != nil {
				return err
			}
		}
		pending = packet
		if packet.eos {
			break
		}
	}
	if pending != nil {
		if err := writer.writePacket(pending.data, pending.granule, true, true); err != nil {
			return err
		}
	}
	return writer.finish()
}

func readVorbisHeader(reader *oggPacketReader, code byte) ([]byte, error) {
	packet, err := reader.next()
	if err != nil {
		return nil, fmt.Errorf("read Vorbis header %d: %w", code, err)
	}
	if len(packet.data) == 0 || packet.data[0] != code {
		return nil, fmt.Errorf("unexpected Vorbis header %d", code)
	}
	return packet.data, nil
}

func findOggStart(src librespot.SizedReadAtSeeker) (int64, error) {
	size := src.Size()
	if size <= 0 {
		return 0, errors.New("Spotify audio file is empty")
	}
	if size > spotifyOggHeaderEnd {
		var head [4]byte
		if n, _ := src.ReadAt(head[:], spotifyOggHeaderEnd); n == 4 && string(head[:]) == "OggS" {
			return spotifyOggHeaderEnd, nil
		}
	}
	limit := min(size, int64(4096))
	buf := make([]byte, limit)
	n, _ := src.ReadAt(buf, 0)
	if at := bytes.Index(buf[:n], []byte("OggS")); at >= 0 {
		return int64(at), nil
	}
	return 0, errors.New("Spotify audio does not contain an Ogg stream")
}

type oggWriter struct {
	w            io.Writer
	serial       uint32
	sequence     uint32
	segments     []byte
	payload      []byte
	lastGranule  uint64
	pageContinue bool
	wrotePage    bool
}

func newOggWriter(w io.Writer, serial uint32) *oggWriter {
	return &oggWriter{w: w, serial: serial}
}

func (w *oggWriter) writePacket(data []byte, granule uint64, lastInPage, eos bool) error {
	for {
		lace := len(data)
		chunk := data
		if lace > 255 {
			lace = 255
			chunk = data[:255]
		}
		if len(w.segments) == 255 {
			if err := w.flush(w.lastGranule, false, w.pageContinue); err != nil {
				return err
			}
			w.pageContinue = false
		}
		w.segments = append(w.segments, byte(lace))
		w.payload = append(w.payload, chunk...)
		data = data[len(chunk):]
		if lace < 255 {
			w.lastGranule = granule
			if lastInPage || eos || len(w.segments) == 255 {
				if err := w.flush(granule, eos, w.pageContinue); err != nil {
					return err
				}
				w.pageContinue = false
			}
			return nil
		}
		if len(w.segments) == 255 {
			if err := w.flush(w.lastGranule, false, w.pageContinue); err != nil {
				return err
			}
			w.pageContinue = true
		}
	}
}

func (w *oggWriter) flush(granule uint64, eos, continued bool) error {
	if len(w.segments) == 0 {
		return nil
	}
	flags := byte(0)
	if continued {
		flags |= 0x01
	}
	if !w.wrotePage {
		flags |= 0x02
	}
	if eos {
		flags |= 0x04
	}
	head := make([]byte, 27+len(w.segments))
	copy(head[:4], "OggS")
	head[4] = 0
	head[5] = flags
	binary.LittleEndian.PutUint64(head[6:14], granule)
	binary.LittleEndian.PutUint32(head[14:18], w.serial)
	binary.LittleEndian.PutUint32(head[18:22], w.sequence)
	head[26] = byte(len(w.segments))
	copy(head[27:], w.segments)
	page := append(head, w.payload...)
	binary.LittleEndian.PutUint32(page[22:26], oggCRC(page))
	if _, err := w.w.Write(page); err != nil {
		return err
	}
	w.sequence++
	w.wrotePage = true
	w.segments = w.segments[:0]
	w.payload = w.payload[:0]
	w.lastGranule = 0
	return nil
}

func (w *oggWriter) finish() error {
	if len(w.segments) == 0 {
		return nil
	}
	return w.flush(w.lastGranule, true, w.pageContinue)
}

var oggCRCTable = func() [256]uint32 {
	var table [256]uint32
	for i := range table {
		r := uint32(i) << 24
		for range 8 {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		table[i] = r
	}
	return table
}()

func oggCRC(buf []byte) uint32 {
	var crc uint32
	for _, b := range buf {
		crc = crc<<8 ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}
