package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newTestJob(t *testing.T, data []byte) (*streamJob, *audioServer) {
	t.Helper()
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "*.audio")
	if err != nil {
		t.Fatal(err)
	}
	job := &streamJob{key: "x", file: f, path: f.Name()}
	job.cond = sync.NewCond(&job.mu)
	if _, err := job.file.Write(data); err != nil {
		t.Fatal(err)
	}
	job.n = int64(len(data))
	job.done = true
	return job, &audioServer{cacheDir: dir, jobs: map[string]*streamJob{"x": job}}
}

func TestJobReaderDeliversAllBytes(t *testing.T) {
	want := []byte("OggS-test-audio")
	job, _ := newTestJob(t, want)
	got, err := io.ReadAll(&jobReader{job: job})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestServeRange(t *testing.T) {
	job, server := newTestJob(t, []byte("0123456789"))
	req := httptest.NewRequest(http.MethodGet, "/stream?uri=spotify:track:x", nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	server.serveRange(rec, req, job)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", res.StatusCode)
	}
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "2345" {
		t.Fatalf("body = %q, want %q", got, "2345")
	}
	if got := res.Header.Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q", got)
	}
	if got := res.Header.Get("X-Audio-Type"); got != "mp3" {
		t.Fatalf("X-Audio-Type = %q", got)
	}
}

func TestHeadReportsAudioType(t *testing.T) {
	job, server := newTestJob(t, []byte("ID3audio"))
	job.typ = "mp3"
	req := httptest.NewRequest(http.MethodHead, "/stream?uri=spotify:episode:x", nil)
	rec := httptest.NewRecorder()
	server.serveWhole(rec, req, job)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if got := res.Header.Get("X-Audio-Type"); got != "mp3" {
		t.Fatalf("X-Audio-Type = %q", got)
	}
	if got := res.Header.Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestServeStreamUsesSharedJob(t *testing.T) {
	job, base := newTestJob(t, []byte("ID3audio"))
	job.typ = "mp3"
	manager := &sessionManager{
		state:   &persistedState{},
		session: &spotifySession{},
		auth:    authStatus{State: "signed_in"},
	}
	server := &audioServer{
		manager:  manager,
		cacheDir: base.cacheDir,
		jobs:     map[string]*streamJob{"spotify:track:x\x00high": job},
	}
	req := httptest.NewRequest(http.MethodGet, "/stream?uri=spotify:track:x&quality=high", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	got, _ := io.ReadAll(res.Body)
	if string(got) != "ID3audio" {
		t.Fatalf("body = %q", got)
	}
}

func TestServeRangeWhileStreamIsStillArriving(t *testing.T) {
	job, server := newTestJob(t, []byte("0123456789"))
	job.mu.Lock()
	job.done = false
	job.cond = sync.NewCond(&job.mu)
	job.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/stream?uri=spotify:track:x", nil)
	req.Header.Set("Range", "bytes=0-4")
	rec := httptest.NewRecorder()
	server.serveRange(rec, req, job)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", res.StatusCode)
	}
	got, _ := io.ReadAll(res.Body)
	if string(got) != "01234" {
		t.Fatalf("body = %q, want %q", got, "01234")
	}
	if got := res.Header.Get("Content-Range"); got != "bytes 0-4/*" {
		t.Fatalf("Content-Range = %q", got)
	}
}

func TestParseByteRange(t *testing.T) {
	tests := []struct {
		in   string
		ok   bool
		want byteRange
	}{
		{"bytes=2-5", true, byteRange{start: 2, end: 5}},
		{"bytes=2-", true, byteRange{start: 2, end: -1, openEnded: true}},
		{"bytes=-5", true, byteRange{length: 5, suffix: true}},
		{"bytes=5-2", false, byteRange{}},
		{"bytes=0-1,3-4", false, byteRange{}},
	}
	for _, tt := range tests {
		got, ok := parseByteRange(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("%q: got %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestJobCachePathExists(t *testing.T) {
	job, server := newTestJob(t, []byte("abc"))
	if filepath.Dir(job.path) != server.cacheDir {
		t.Fatalf("job path %q is not in cache dir %q", job.path, server.cacheDir)
	}
}
