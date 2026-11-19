package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"io"
	"testing"

	"github.com/devgianlu/go-librespot/audio"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
)

func oggFile(format metadatapb.AudioFile_Format, id byte) *metadatapb.AudioFile {
	return &metadatapb.AudioFile{Format: &format, FileId: []byte{id}}
}

func TestChooseOggQuality(t *testing.T) {
	files := []*metadatapb.AudioFile{
		oggFile(metadatapb.AudioFile_OGG_VORBIS_96, 1),
		oggFile(metadatapb.AudioFile_OGG_VORBIS_160, 2),
		oggFile(metadatapb.AudioFile_OGG_VORBIS_320, 3),
	}
	if got := chooseOgg(files, "standard"); got == nil || got.FileId[0] != 1 {
		t.Fatalf("standard chose %v", got)
	}
	if got := chooseOgg(files, "high"); got == nil || got.FileId[0] != 3 {
		t.Fatalf("high chose %v", got)
	}
	if got := chooseOgg(files, "lossless"); got == nil || got.FileId[0] != 3 {
		t.Fatalf("lossless chose %v", got)
	}
}

func TestChooseOggIgnoresOtherCodecs(t *testing.T) {
	mp3 := metadatapb.AudioFile_MP3_320
	if got := chooseOgg([]*metadatapb.AudioFile{{Format: &mp3, FileId: []byte{1}}}, "high"); got != nil {
		t.Fatalf("chose non-Ogg file: %v", got)
	}
}

func TestChooseOtherAudioForPodcast(t *testing.T) {
	files := []*metadatapb.AudioFile{
		oggFile(metadatapb.AudioFile_MP3_160, 1),
		oggFile(metadatapb.AudioFile_MP3_320, 2),
		oggFile(metadatapb.AudioFile_AAC_48, 3),
	}
	if got := chooseOtherAudio(files, "high"); got == nil || got.FileId[0] != 2 {
		t.Fatalf("high chose %v", got)
	}
	if got := chooseOtherAudio(files, "standard"); got == nil || got.FileId[0] != 1 {
		t.Fatalf("standard chose %v", got)
	}
}

func TestChooseOutputFormat(t *testing.T) {
	files := []*metadatapb.AudioFile{
		oggFile(metadatapb.AudioFile_OGG_VORBIS_320, 1),
		oggFile(metadatapb.AudioFile_FLAC_FLAC, 2),
		oggFile(metadatapb.AudioFile_FLAC_FLAC_24BIT, 3),
	}
	file, typ, bitrate := chooseOutput(files, "high")
	if file == nil || typ != "mp3" || bitrate != 320000 || file.FileId[0] != 1 {
		t.Fatalf("high: %v %q %d", file, typ, bitrate)
	}
	file, typ, bitrate = chooseOutput(files, "lossless")
	if file == nil || typ != "flac" || bitrate != 0 || file.FileId[0] != 2 {
		t.Fatalf("lossless: %v %q %d", file, typ, bitrate)
	}
	file, typ, bitrate = chooseOutput(files[:1], "lossless")
	if file == nil || typ != "mp3" || bitrate != 320000 {
		t.Fatalf("lossless without FLAC: %v %q %d", file, typ, bitrate)
	}
}

func TestAudioFormatInfo(t *testing.T) {
	if got := audioFormatInfo(metadatapb.AudioFile_OGG_VORBIS_320); got.typ != "ogg" || !got.ogg {
		t.Fatalf("ogg info: %+v", got)
	}
	if got := audioFormatInfo(metadatapb.AudioFile_MP3_320); got.typ != "mp3" || got.ogg {
		t.Fatalf("mp3 info: %+v", got)
	}
}

func TestDecryptedReader(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 16)
	plain := bytes.Repeat([]byte("spotify audio "), 100)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	// This is go-librespot's fixed Spotify audio CTR IV.
	iv := []byte{0x72, 0xe0, 0x67, 0xfb, 0xdd, 0xcb, 0xcf, 0x77, 0xeb, 0xe8, 0xbc, 0x64, 0x3f, 0x63, 0x0d, 0x93}
	encrypted := make([]byte, len(plain))
	cipher.NewCTR(block, iv).XORKeyStream(encrypted, plain)

	decryptor, err := audio.NewAesAudioDecryptor(memoryAudio{bytes.NewReader(encrypted), int64(len(encrypted))}, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(&decryptedAudioReader{decryptor: decryptor, size: int64(len(encrypted))})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("decrypted bytes differ")
	}
}
