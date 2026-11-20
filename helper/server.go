package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type audioServer struct {
	log      logger
	manager  *sessionManager
	cacheDir string

	mu   sync.Mutex
	jobs map[string]*streamJob
}

type streamJob struct {
	key  string
	typ  string
	mu   sync.Mutex
	cond *sync.Cond
	file *os.File
	path string
	n    int64
	done bool
	err  error
}

func newAudioServer(dir string, log logger, manager *sessionManager) (*audioServer, error) {
	cacheDir := filepath.Join(dir, "stream-cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("create stream cache: %w", err)
	}
	cleanOldStreams(cacheDir)
	return &audioServer{
		log:      log,
		manager:  manager,
		cacheDir: cacheDir,
		jobs:     map[string]*streamJob{},
	}, nil
}

func cleanOldStreams(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".audio") && !strings.HasSuffix(entry.Name(), ".ogg")) {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func (s *audioServer) Close() {
	s.mu.Lock()
	jobs := make([]*streamJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	s.jobs = map[string]*streamJob{}
	s.mu.Unlock()
	for _, job := range jobs {
		job.mu.Lock()
		file, path := job.file, job.path
		job.done = true
		if job.err == nil {
			job.err = errors.New("audio helper is shutting down")
		}
		job.cond.Broadcast()
		job.mu.Unlock()
		if file != nil {
			_ = file.Close()
		}
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

func (s *audioServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health", "/auth":
		s.serveHealth(w)
	case "/auth/start":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.manager.start(r.URL.Query().Get("new") == "1")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.health())
	case "/logout":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.manager.logout()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.health())
	case "/stream":
		s.serveStream(w, r)
	default:
		http.NotFound(w, r)
	}
}

type healthResponse struct {
	OK        bool       `json:"ok"`
	Ready     bool       `json:"ready"`
	Auth      authStatus `json:"auth"`
	Version   string     `json:"version"`
	Timestamp int64      `json:"timestamp"`
}

func (s *audioServer) health() healthResponse {
	s.manager.ensureAlive()
	auth, ready := s.manager.status()
	return healthResponse{
		OK:        true,
		Ready:     ready,
		Auth:      auth,
		Version:   "0.4.2",
		Timestamp: time.Now().Unix(),
	}
}

func (s *audioServer) serveHealth(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(s.health())
}

func (s *audioServer) serveStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	uri := r.URL.Query().Get("uri")
	if uri == "" {
		http.Error(w, "uri is required", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(uri, "spotify:track:") && !strings.HasPrefix(uri, "spotify:episode:") {
		http.Error(w, "uri must be a Spotify track or episode", http.StatusBadRequest)
		return
	}
	quality := r.URL.Query().Get("quality")
	if quality == "" {
		quality = "high"
	}

	if _, ready := s.manager.status(); !ready {
		auth, _ := s.manager.status()
		if auth.State == "login_required" {
			http.Error(w, "Spotify audio sign-in is required: "+auth.URL, http.StatusUnauthorized)
			return
		}
		if auth.State == "error" {
			http.Error(w, auth.Error, http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if _, err := s.manager.wait(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
	}

	job := s.job(uri, quality)
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		s.serveRange(w, r, job)
		return
	}
	s.serveWhole(w, r, job)
}

func (s *audioServer) job(uri, quality string) *streamJob {
	key := uri + "\x00" + quality
	s.mu.Lock()
	defer s.mu.Unlock()
	if job := s.jobs[key]; job != nil {
		job.mu.Lock()
		failed := job.done && job.err != nil
		if !failed {
			job.mu.Unlock()
			return job
		}
		file, path := job.file, job.path
		job.cond.Broadcast()
		job.mu.Unlock()
		delete(s.jobs, key)
		if file != nil {
			_ = file.Close()
		}
		if path != "" {
			_ = os.Remove(path)
		}
	}
	job := &streamJob{key: key}
	job.cond = sync.NewCond(&job.mu)
	file, err := os.CreateTemp(s.cacheDir, "*.audio")
	if err != nil {
		job.done, job.err = true, err
		s.jobs[key] = job
		return job
	}
	job.file, job.path = file, file.Name()
	s.jobs[key] = job
	go s.produce(job, uri, quality)
	return job
}

func (s *audioServer) produce(job *streamJob, uri, quality string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	sess, err := s.manager.wait(ctx)
	if err != nil {
		s.finish(job, err)
		return
	}
	src, err := openAudio(ctx, sess, uri, quality)
	if err != nil {
		s.finish(job, err)
		return
	}
	defer src.Close()
	job.mu.Lock()
	job.typ = src.output
	job.cond.Broadcast()
	job.mu.Unlock()
	err = src.render(jobWriter{job: job})
	if err != nil {
		s.log.WithError(err).WithField("uri", uri).Warnf("stream failed")
	}
	s.finish(job, err)
}

func (s *audioServer) finish(job *streamJob, err error) {
	job.mu.Lock()
	if !job.done {
		job.done, job.err = true, err
	}
	job.cond.Broadcast()
	job.mu.Unlock()
}

type jobWriter struct {
	job *streamJob
}

func (w jobWriter) Write(p []byte) (int, error) {
	w.job.mu.Lock()
	defer w.job.mu.Unlock()
	if w.job.err != nil {
		return 0, w.job.err
	}
	n, err := w.job.file.Write(p)
	if n > 0 {
		w.job.n += int64(n)
		w.job.cond.Broadcast()
	}
	return n, err
}

func (s *audioServer) waitStarted(ctx context.Context, job *streamJob) error {
	for {
		job.mu.Lock()
		n, done, err := job.n, job.done, job.err
		job.mu.Unlock()
		if n > 0 {
			return nil
		}
		if done {
			if err != nil {
				return err
			}
			return io.EOF
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *audioServer) waitDone(ctx context.Context, job *streamJob) error {
	for {
		job.mu.Lock()
		done, err := job.done, job.err
		job.mu.Unlock()
		if done {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *audioServer) serveWhole(w http.ResponseWriter, r *http.Request, job *streamJob) {
	if err := s.waitStarted(r.Context(), job); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, io.EOF) {
			status = http.StatusBadGateway
		}
		http.Error(w, err.Error(), status)
		return
	}
	job.mu.Lock()
	typ := job.typ
	job.mu.Unlock()
	if typ == "" {
		typ = "mp3"
	}
	w.Header().Set("Content-Type", mimeForType(typ))
	w.Header().Set("X-Audio-Type", typ)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = io.Copy(w, &jobReader{job: job})
}

func (s *audioServer) serveRange(w http.ResponseWriter, r *http.Request, job *streamJob) {
	spec, ok := parseByteRange(r.Header.Get("Range"))
	if !ok {
		http.Error(w, "invalid Range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	var total int64
	var done bool
	var jobErr error
	if spec.suffix {
		// A suffix needs the final length, so wait for production to end.
		if err := s.waitDone(r.Context(), job); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		job.mu.Lock()
		total, done, jobErr = job.n, job.done, job.err
		job.mu.Unlock()
		if done && jobErr != nil {
			http.Error(w, jobErr.Error(), http.StatusBadGateway)
			return
		}
		if total == 0 {
			http.Error(w, "empty stream", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		spec.start = max(0, total-spec.length)
		spec.end = total - 1
	} else {
		if spec.end < 0 {
			// An open-ended range also needs the final length.
			if err := s.waitDone(r.Context(), job); err != nil && total == 0 {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			job.mu.Lock()
			total, done, jobErr = job.n, job.done, job.err
			job.mu.Unlock()
			if done && jobErr != nil {
				http.Error(w, jobErr.Error(), http.StatusBadGateway)
				return
			}
		} else if err := s.waitForBytes(r.Context(), job, spec.end+1); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		job.mu.Lock()
		total, done, jobErr = job.n, job.done, job.err
		job.mu.Unlock()
		if done && jobErr != nil {
			http.Error(w, jobErr.Error(), http.StatusBadGateway)
			return
		}
		if spec.end < 0 || spec.end >= total {
			spec.end = total - 1
		}
	}
	if total == 0 || spec.start >= total || spec.end < spec.start {
		totalPart := "*"
		if done {
			totalPart = strconv.FormatInt(total, 10)
		}
		w.Header().Set("Content-Range", "bytes */"+totalPart)
		http.Error(w, "range starts after the stream", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	length := spec.end - spec.start + 1
	job.mu.Lock()
	typ := job.typ
	job.mu.Unlock()
	if typ == "" {
		typ = "mp3"
	}
	w.Header().Set("Content-Type", mimeForType(typ))
	w.Header().Set("X-Audio-Type", typ)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	totalPart := "*"
	if done {
		totalPart = strconv.FormatInt(total, 10)
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%s", spec.start, spec.end, totalPart))
	w.WriteHeader(http.StatusPartialContent)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.CopyN(w, &jobReader{job: job, off: spec.start}, length)
}

type byteRange struct {
	start     int64
	end       int64 // -1 means open ended
	length    int64 // suffix length
	suffix    bool
	openEnded bool
}

func parseByteRange(value string) (byteRange, bool) {
	var out byteRange
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return out, false
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "bytes="), "-", 2)
	if len(parts) != 2 {
		return out, false
	}
	if parts[0] == "" {
		n, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || n <= 0 {
			return out, false
		}
		out.suffix, out.length = true, n
		return out, true
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 {
		return out, false
	}
	out.start = start
	if parts[1] == "" {
		out.end, out.openEnded = -1, true
		return out, true
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start {
		return out, false
	}
	out.end = end
	return out, true
}

func (s *audioServer) waitForBytes(ctx context.Context, job *streamJob, want int64) error {
	for {
		job.mu.Lock()
		n, done, err := job.n, job.done, job.err
		job.mu.Unlock()
		if n >= want || done {
			if done && err != nil && n == 0 {
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

type jobReader struct {
	job *streamJob
	off int64
}

func (r *jobReader) Read(p []byte) (int, error) {
	r.job.mu.Lock()
	defer r.job.mu.Unlock()
	for r.off >= r.job.n && !r.job.done {
		r.job.cond.Wait()
	}
	if r.off < r.job.n {
		n, err := r.job.file.ReadAt(p, r.off)
		r.off += int64(n)
		if n > 0 {
			return n, nil
		}
		return 0, err
	}
	if r.job.err != nil {
		return 0, r.job.err
	}
	return 0, io.EOF
}
