package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	port := flag.Int("port", 43111, "loopback HTTP port")
	dataDir := flag.String("data-dir", "", "directory for credentials and stream cache")
	tokenStdin := flag.Bool("token-stdin", false, "read a Web API token from the first line of stdin")
	flag.Parse()

	if *dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.TempDir()
		}
		*dataDir = filepath.Join(home, ".local", "share", "mygo-music-spotify")
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if logFile, err := os.OpenFile(filepath.Join(*dataDir, "helper.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		defer logFile.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
	}
	lg := logger{}
	manager, err := newSessionManager(*dataDir, lg)
	if err != nil {
		log.Fatal(err)
	}
	defer manager.stop()
	server, err := newAudioServer(*dataDir, lg, manager)
	if err != nil {
		log.Fatal(err)
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatal(err)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	lg.Infof("Spotify audio helper listening on 127.0.0.1:%d", actualPort)

	httpServer := &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	// Open the health endpoint before waiting for a token: on Windows a
	// delayed token refresh must not look like a helper that never started.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	if *tokenStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			log.Fatal(err)
		}
		var tok struct {
			AccessToken string `json:"accessToken"`
			Username    string `json:"username"`
		}
		if err := json.Unmarshal([]byte(line), &tok); err != nil {
			lg.Warnf("ignoring malformed Web API token on stdin: %v", err)
		} else {
			manager.setWebToken(tok.AccessToken, tok.Username)
		}
	}
	manager.start(false)
	if err := <-serveErr; err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	server.Close()
}
