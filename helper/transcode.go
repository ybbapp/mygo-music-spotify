package main

import (
	"encoding/binary"
	"errors"
	"io"
	"math"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/jfreymuth/oggvorbis"
	mp3pcm "github.com/tphakala/go-mp3/pcm"
)

// transcodeOggToMP3 decodes Spotify's Ogg Vorbis stream and encodes a CBR MP3
// stream. Spotify does not offer MP3 as a source format, so a compatible MP3
// download necessarily goes through PCM.
func transcodeOggToMP3(src librespot.SizedReadAtSeeker, dst io.Writer, bitrate int) error {
	reader, err := oggSourceReader(src)
	if err != nil {
		return err
	}
	vorbis, err := oggvorbis.NewReader(reader)
	if err != nil {
		return err
	}
	channels := vorbis.Channels()
	if channels != 1 && channels != 2 {
		return errors.New("MP3 conversion supports only mono or stereo audio")
	}
	encoder, err := mp3pcm.NewEncoder(dst, mp3pcm.Config{
		SampleRate: vorbis.SampleRate(),
		Channels:   channels,
		Bitrate:    bitrate,
	})
	if err != nil {
		return err
	}

	samples := make([]float32, 4096*channels)
	pcm := make([]byte, len(samples)*2)
	for {
		n, readErr := vorbis.Read(samples)
		if n > 0 {
			for i, sample := range samples[:n] {
				v := math.Round(float64(sample) * 32767)
				if v > 32767 {
					v = 32767
				} else if v < -32768 {
					v = -32768
				}
				binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(v)))
			}
			if _, err := encoder.Write(pcm[:n*2]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return encoder.Close()
}

type readerAtSource struct {
	r    io.ReaderAt
	off  int64
	size int64
}

func (r *readerAtSource) Read(p []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	if int64(len(p)) > r.size-r.off {
		p = p[:r.size-r.off]
	}
	n, err := r.r.ReadAt(p, r.off)
	r.off += int64(n)
	return n, err
}

func oggSourceReader(src librespot.SizedReadAtSeeker) (io.Reader, error) {
	start, err := findOggStart(src)
	if err != nil {
		return nil, err
	}
	return &readerAtSource{r: src, off: start, size: src.Size()}, nil
}
