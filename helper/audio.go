package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/audio"
	downloadpb "github.com/devgianlu/go-librespot/proto/spotify/download"
	extmetadatapb "github.com/devgianlu/go-librespot/proto/spotify/extendedmetadata"
	audiofilespb "github.com/devgianlu/go-librespot/proto/spotify/extendedmetadata/audiofiles"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
)

type encryptedAudio struct {
	reader  librespot.SizedReadAtSeeker
	format  metadatapb.AudioFile_Format
	output  string
	bitrate int
	close   func()
}

func (a *encryptedAudio) Close() {
	if a != nil && a.close != nil {
		a.close()
	}
}

func openAudio(ctx context.Context, sess *spotifySession, uri, quality string) (*encryptedAudio, error) {
	id, err := librespot.SpotifyIdFromUri(uri)
	if err != nil {
		return nil, fmt.Errorf("invalid Spotify URI: %w", err)
	}

	var files []*metadatapb.AudioFile
	switch id.Type() {
	case librespot.SpotifyIdTypeTrack:
		files, err = trackAudioFiles(ctx, sess, *id)
		if err != nil || !hasOutput(files, quality) {
			var track metadatapb.Track
			if metaErr := sess.sp.ExtendedMetadataSimple(ctx, *id, extmetadatapb.ExtensionKind_TRACK_V4, &track); metaErr == nil {
				for _, alt := range track.Alternative {
					if len(alt.Gid) != 16 {
						continue
					}
					altID := librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeTrack, alt.Gid)
					altFiles, altErr := trackAudioFiles(ctx, sess, altID)
					if altErr == nil && hasOutput(altFiles, quality) {
						files = altFiles
						id = &altID
						break
					}
				}
			}
		}
		if err != nil && len(files) == 0 {
			return nil, fmt.Errorf("read track audio formats: %w", err)
		}
	case librespot.SpotifyIdTypeEpisode:
		var episode metadatapb.Episode
		if err := sess.sp.ExtendedMetadataSimple(ctx, *id, extmetadatapb.ExtensionKind_EPISODE_V4, &episode); err != nil {
			return nil, fmt.Errorf("read episode audio formats: %w", err)
		}
		files = episode.Audio
	default:
		return nil, fmt.Errorf("Spotify %s is not playable audio", id.Type())
	}

	file, output, bitrate := chooseOutput(files, quality)
	if file == nil {
		return nil, errors.New("Spotify has no MP3, FLAC or Ogg source for this item")
	}

	audio, err := openEncrypted(ctx, sess, *id, file, output, bitrate)
	if err == nil {
		return audio, nil
	}
	// FLAC keys are refused for some accounts even when the format is listed.
	// Try the other FLAC depth once, then retry from Ogg as MP3.
	if output == "flac" {
		for _, alt := range chooseFLACAlternatives(files) {
			if alt.FileId == nil || string(alt.FileId) == string(file.FileId) {
				continue
			}
			if altAudio, altErr := openEncrypted(ctx, sess, *id, alt, "flac", 0); altErr == nil {
				return altAudio, nil
			}
		}
	}
	fallback := chooseOggForOutput(files, quality)
	if fallback != nil && (fallback.FileId == nil || file.FileId == nil || string(fallback.FileId) != string(file.FileId)) {
		if alt, altErr := openEncrypted(ctx, sess, *id, fallback, "mp3", mp3Bitrate(quality)); altErr == nil {
			return alt, nil
		}
	}
	return nil, err
}

func openEncrypted(ctx context.Context, sess *spotifySession, id librespot.SpotifyId, file *metadatapb.AudioFile, output string, bitrate int) (*encryptedAudio, error) {
	key, err := sess.audioKey.Request(ctx, id.Id(), file.FileId)
	if err != nil {
		if strings.Contains(err.Error(), "code 0") {
			return nil, fmt.Errorf("get Spotify audio key: %w (Spotify may refuse audio keys for this account)", err)
		}
		return nil, fmt.Errorf("get Spotify audio key: %w", err)
	}
	storage, err := sess.sp.ResolveStorageInteractive(ctx, file.FileId, file.Format, false)
	if err != nil {
		return nil, fmt.Errorf("find Spotify audio file: %w", err)
	}
	raw, err := openCDN(sess, storage)
	if err != nil {
		return nil, err
	}
	decryptor, err := audio.NewAesAudioDecryptor(raw, key)
	if err != nil {
		if closer, ok := raw.(io.Closer); ok {
			_ = closer.Close()
		}
		return nil, fmt.Errorf("start Spotify audio decryption: %w", err)
	}
	decrypted := &decryptedAudioReader{decryptor: decryptor, size: raw.Size()}
	info := audioFormatInfo(*file.Format)
	if output == "flac" && info.typ != "flac" {
		return nil, errors.New("Spotify has no FLAC source for this item")
	}
	if output == "mp3" && info.typ != "mp3" && info.typ != "ogg" {
		return nil, fmt.Errorf("Spotify offers %s but no MP3 or Ogg source to convert", info.typ)
	}
	return &encryptedAudio{reader: decrypted, format: *file.Format, output: output, bitrate: bitrate, close: func() {
		if closer, ok := raw.(io.Closer); ok {
			_ = closer.Close()
		}
	}}, nil
}

func (a *encryptedAudio) render(w io.Writer) error {
	switch a.output {
	case "flac":
		_, err := io.Copy(w, a.reader)
		return err
	case "mp3":
		if audioFormatInfo(a.format).typ == "mp3" {
			_, err := io.Copy(w, a.reader)
			return err
		}
		return transcodeOggToMP3(a.reader, w, a.bitrate)
	default:
		return errors.New("unknown output format")
	}
}

type decryptedAudioReader struct {
	decryptor *audio.Decryptor
	pos       int64
	size      int64
}

func (r *decryptedAudioReader) Read(p []byte) (int, error) {
	n, err := r.decryptor.ReadAt(p, r.pos)
	r.pos += int64(n)
	return n, err
}

func (r *decryptedAudioReader) ReadAt(p []byte, off int64) (int, error) {
	return r.decryptor.ReadAt(p, off)
}

func (r *decryptedAudioReader) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		off += r.pos
	case io.SeekEnd:
		off += r.size
	default:
		return 0, errors.New("invalid seek whence")
	}
	if off < 0 || off > r.size {
		return 0, errors.New("seek outside Spotify audio")
	}
	r.pos = off
	return off, nil
}

func (r *decryptedAudioReader) Size() int64 { return r.size }

func openCDN(sess *spotifySession, storage *downloadpb.StorageResolveResponse) (librespot.SizedReadAtSeeker, error) {
	switch storage.Result {
	case downloadpb.StorageResolveResponse_CDN:
		if len(storage.Cdnurl) == 0 {
			return nil, errors.New("Spotify returned no audio CDN URL")
		}
		var lastErr error
		for _, rawURL := range storage.Cdnurl {
			if _, err := url.Parse(rawURL); err != nil {
				lastErr = err
				continue
			}
			reader, err := audio.NewHttpChunkedReader(sess.log, sess.client, rawURL)
			if err == nil {
				return reader, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("open Spotify audio CDN: %w", lastErr)
	case downloadpb.StorageResolveResponse_STORAGE:
		return nil, errors.New("Spotify's legacy storage format is not supported")
	case downloadpb.StorageResolveResponse_RESTRICTED:
		return nil, errors.New("Spotify says this audio is restricted")
	default:
		return nil, fmt.Errorf("unknown Spotify storage result: %s", storage.Result)
	}
}

func chooseOgg(files []*metadatapb.AudioFile, quality string) *metadatapb.AudioFile {
	var order []metadatapb.AudioFile_Format
	switch strings.ToLower(quality) {
	case "standard":
		order = []metadatapb.AudioFile_Format{
			metadatapb.AudioFile_OGG_VORBIS_96,
			metadatapb.AudioFile_OGG_VORBIS_160,
			metadatapb.AudioFile_OGG_VORBIS_320,
		}
	case "lossless":
		order = []metadatapb.AudioFile_Format{
			metadatapb.AudioFile_OGG_VORBIS_320,
			metadatapb.AudioFile_OGG_VORBIS_160,
			metadatapb.AudioFile_OGG_VORBIS_96,
		}
	default:
		order = []metadatapb.AudioFile_Format{
			metadatapb.AudioFile_OGG_VORBIS_320,
			metadatapb.AudioFile_OGG_VORBIS_160,
			metadatapb.AudioFile_OGG_VORBIS_96,
		}
	}
	for _, want := range order {
		for _, file := range files {
			if file != nil && file.Format != nil && *file.Format == want && len(file.FileId) > 0 {
				return file
			}
		}
	}
	return nil
}

func chooseOutput(files []*metadatapb.AudioFile, quality string) (*metadatapb.AudioFile, string, int) {
	if strings.EqualFold(quality, "lossless") {
		if file := chooseFLAC(files); file != nil {
			return file, "flac", 0
		}
	}
	if file := chooseMP3(files, quality); file != nil {
		return file, "mp3", mp3Bitrate(quality)
	}
	if file := chooseOggForOutput(files, quality); file != nil {
		return file, "mp3", mp3Bitrate(quality)
	}
	return nil, "", 0
}

func hasOutput(files []*metadatapb.AudioFile, quality string) bool {
	file, _, _ := chooseOutput(files, quality)
	return file != nil
}

func chooseFLAC(files []*metadatapb.AudioFile) *metadatapb.AudioFile {
	files = chooseFLACAlternatives(files)
	if len(files) == 0 {
		return nil
	}
	return files[0]
}

func chooseFLACAlternatives(files []*metadatapb.AudioFile) []*metadatapb.AudioFile {
	var out []*metadatapb.AudioFile
	for _, want := range []metadatapb.AudioFile_Format{
		metadatapb.AudioFile_FLAC_FLAC,
		metadatapb.AudioFile_FLAC_FLAC_24BIT,
	} {
		for _, file := range files {
			if file != nil && file.Format != nil && *file.Format == want && len(file.FileId) > 0 {
				out = append(out, file)
			}
		}
	}
	return out
}

func chooseMP3(files []*metadatapb.AudioFile, quality string) *metadatapb.AudioFile {
	order := []metadatapb.AudioFile_Format{
		metadatapb.AudioFile_MP3_320,
		metadatapb.AudioFile_MP3_160,
		metadatapb.AudioFile_MP3_96,
		metadatapb.AudioFile_MP3_256,
		metadatapb.AudioFile_MP3_160_ENC,
	}
	if strings.EqualFold(quality, "standard") {
		order = []metadatapb.AudioFile_Format{
			metadatapb.AudioFile_MP3_160,
			metadatapb.AudioFile_MP3_96,
			metadatapb.AudioFile_MP3_320,
			metadatapb.AudioFile_MP3_256,
			metadatapb.AudioFile_MP3_160_ENC,
		}
	}
	for _, want := range order {
		for _, file := range files {
			if file != nil && file.Format != nil && *file.Format == want && len(file.FileId) > 0 {
				return file
			}
		}
	}
	return nil
}

func chooseOggForOutput(files []*metadatapb.AudioFile, quality string) *metadatapb.AudioFile {
	order := []metadatapb.AudioFile_Format{
		metadatapb.AudioFile_OGG_VORBIS_320,
		metadatapb.AudioFile_OGG_VORBIS_160,
		metadatapb.AudioFile_OGG_VORBIS_96,
	}
	if strings.EqualFold(quality, "standard") {
		order = []metadatapb.AudioFile_Format{
			metadatapb.AudioFile_OGG_VORBIS_160,
			metadatapb.AudioFile_OGG_VORBIS_320,
			metadatapb.AudioFile_OGG_VORBIS_96,
		}
	}
	for _, want := range order {
		for _, file := range files {
			if file != nil && file.Format != nil && *file.Format == want && len(file.FileId) > 0 {
				return file
			}
		}
	}
	return nil
}

func mp3Bitrate(quality string) int {
	if strings.EqualFold(quality, "standard") {
		return 160000
	}
	return 320000
}

func chooseOtherAudio(files []*metadatapb.AudioFile, quality string) *metadatapb.AudioFile {
	var order []metadatapb.AudioFile_Format
	switch strings.ToLower(quality) {
	case "standard":
		order = []metadatapb.AudioFile_Format{
			metadatapb.AudioFile_MP3_96,
			metadatapb.AudioFile_MP3_160,
			metadatapb.AudioFile_AAC_24,
			metadatapb.AudioFile_AAC_48,
			metadatapb.AudioFile_MP3_320,
			metadatapb.AudioFile_FLAC_FLAC,
		}
	case "lossless":
		order = []metadatapb.AudioFile_Format{
			metadatapb.AudioFile_FLAC_FLAC_24BIT,
			metadatapb.AudioFile_FLAC_FLAC,
			metadatapb.AudioFile_MP3_320,
			metadatapb.AudioFile_MP3_160,
		}
	default:
		order = []metadatapb.AudioFile_Format{
			metadatapb.AudioFile_MP3_320,
			metadatapb.AudioFile_MP3_160,
			metadatapb.AudioFile_MP3_96,
			metadatapb.AudioFile_AAC_48,
			metadatapb.AudioFile_AAC_24,
			metadatapb.AudioFile_FLAC_FLAC,
		}
	}
	for _, want := range order {
		for _, file := range files {
			if file != nil && file.Format != nil && *file.Format == want && len(file.FileId) > 0 {
				return file
			}
		}
	}
	return nil
}

type audioInfo struct {
	typ string
	ogg bool
}

func audioFormatInfo(format metadatapb.AudioFile_Format) audioInfo {
	switch format {
	case metadatapb.AudioFile_OGG_VORBIS_96,
		metadatapb.AudioFile_OGG_VORBIS_160,
		metadatapb.AudioFile_OGG_VORBIS_320:
		return audioInfo{typ: "ogg", ogg: true}
	case metadatapb.AudioFile_FLAC_FLAC,
		metadatapb.AudioFile_FLAC_FLAC_24BIT:
		return audioInfo{typ: "flac"}
	case metadatapb.AudioFile_MP3_96,
		metadatapb.AudioFile_MP3_160,
		metadatapb.AudioFile_MP3_256,
		metadatapb.AudioFile_MP3_320,
		metadatapb.AudioFile_MP3_160_ENC:
		return audioInfo{typ: "mp3"}
	case metadatapb.AudioFile_AAC_24,
		metadatapb.AudioFile_AAC_48,
		metadatapb.AudioFile_XHE_AAC_12,
		metadatapb.AudioFile_XHE_AAC_16,
		metadatapb.AudioFile_XHE_AAC_24:
		return audioInfo{typ: "aac"}
	default:
		return audioInfo{typ: "bin"}
	}
}

func mimeForType(typ string) string {
	switch typ {
	case "ogg":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "mp3":
		return "audio/mpeg"
	case "aac":
		return "audio/aac"
	default:
		return "application/octet-stream"
	}
}

func trackAudioFiles(ctx context.Context, sess *spotifySession, id librespot.SpotifyId) ([]*metadatapb.AudioFile, error) {
	var audioFiles audiofilespb.AudioFilesExtensionResponse
	if err := sess.sp.ExtendedMetadataSimple(ctx, id, extmetadatapb.ExtensionKind_AUDIO_FILES, &audioFiles); err != nil {
		return nil, err
	}
	files := make([]*metadatapb.AudioFile, 0, len(audioFiles.Files))
	for _, f := range audioFiles.Files {
		if f != nil && f.File != nil {
			files = append(files, f.File)
		}
	}
	return files, nil
}
