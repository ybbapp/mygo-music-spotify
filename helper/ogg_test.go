package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type memoryAudio struct {
	*bytes.Reader
	size int64
}

func (m memoryAudio) Size() int64 { return m.size }

func TestRemuxOggProducesPlayableStream(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}

	dir := t.TempDir()
	normal := filepath.Join(dir, "normal.ogg")
	cmd := exec.Command(
		ffmpeg, "-v", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=0.25:sample_rate=44100",
		"-ac", "2", "-c:a", "vorbis", "-strict", "-2", normal,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	body, err := os.ReadFile(normal)
	if err != nil {
		t.Fatal(err)
	}

	var preface bytes.Buffer
	w := newOggWriter(&preface, 1)
	metadata := bytes.Repeat([]byte{0x81}, spotifyOggHeaderEnd-28)
	if err := w.writePacket(metadata, 0, true, false); err != nil {
		t.Fatal(err)
	}
	if got := preface.Len(); got != spotifyOggHeaderEnd {
		t.Fatalf("preface is %d bytes, want %d", got, spotifyOggHeaderEnd)
	}

	input := append(preface.Bytes(), body...)
	src := memoryAudio{bytes.NewReader(input), int64(len(input))}
	var out bytes.Buffer
	if err := remuxOgg(src, &out); err != nil {
		t.Fatal(err)
	}
	var lastFlags byte
	pages := bytes.NewReader(out.Bytes())
	for {
		page, err := readOggPage(pages)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		lastFlags = page.headerType
	}
	if lastFlags&0x04 == 0 {
		t.Fatal("remuxed Ogg has no end-of-stream page")
	}

	got := filepath.Join(dir, "remuxed.ogg")
	if err := os.WriteFile(got, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,channels,sample_rate", "-of", "default=nw=1", got)
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("ffprobe rejected remuxed Ogg: %v\n%s", err, output)
	}
	decode := exec.Command(ffmpeg, "-v", "error", "-i", got, "-f", "null", "-")
	if output, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg could not decode remuxed Ogg: %v\n%s", err, output)
	}
}
