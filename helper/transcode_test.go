package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTranscodeOggToMP3(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	dir := t.TempDir()
	ogg := filepath.Join(dir, "source.ogg")
	cmd := exec.Command(
		ffmpeg, "-v", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=0.5:sample_rate=44100",
		"-ac", "2", "-c:a", "vorbis", "-strict", "-2", ogg,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	data, err := os.ReadFile(ogg)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := transcodeOggToMP3(memoryAudio{bytes.NewReader(data), int64(len(data))}, &out, 320000); err != nil {
		t.Fatal(err)
	}
	mp3 := filepath.Join(dir, "result.mp3")
	if err := os.WriteFile(mp3, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,sample_rate,channels", "-of", "default=nw=1", mp3)
	output, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte("codec_name=mp3")) {
		t.Fatalf("not MP3: %s", output)
	}
}
