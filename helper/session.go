package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
	"github.com/devgianlu/go-librespot/apresolve"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/devgianlu/go-librespot/login5"
	pbdata "github.com/devgianlu/go-librespot/proto/spotify/clienttoken/data/v0"
	pbhttp "github.com/devgianlu/go-librespot/proto/spotify/clienttoken/http/v0"
	credentialspb "github.com/devgianlu/go-librespot/proto/spotify/login5/v3/credentials"
	"github.com/devgianlu/go-librespot/spclient"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/proto"
)

type spotifySession struct {
	ap       *ap.Accesspoint
	sp       *spclient.Spclient
	audioKey *audio.KeyProvider
	login5   *login5.Login5
	client   *http.Client
	log      logger
}

func (s *spotifySession) Close() {
	if s != nil && s.ap != nil {
		s.ap.Close()
	}
}

func (s *spotifySession) alive() bool {
	if s == nil {
		return false
	}
	if s.ap == nil {
		return true
	}
	select {
	case <-s.ap.Done():
		return false
	default:
		return true
	}
}

type authStatus struct {
	State     string `json:"state"`
	Username  string `json:"username,omitempty"`
	URL       string `json:"url,omitempty"`
	Code      string `json:"code,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	Error     string `json:"error,omitempty"`
}

type sessionManager struct {
	dir    string
	log    logger
	client *http.Client

	mu       sync.Mutex
	state    *persistedState
	session  *spotifySession
	auth     authStatus
	starting bool
	cancel   context.CancelFunc
	webToken string
	webUser  string
}

func (m *sessionManager) setWebToken(token, username string) {
	m.mu.Lock()
	m.webToken, m.webUser = token, username
	m.mu.Unlock()
}

func newSessionManager(dir string, log logger) (*sessionManager, error) {
	state, err := loadPersistedState(dir)
	if err != nil {
		return nil, err
	}
	if state.DeviceID == "" {
		state.DeviceID, err = newDeviceID()
		if err != nil {
			return nil, fmt.Errorf("generate device id: %w", err)
		}
		if err := savePersistedState(dir, state); err != nil {
			return nil, err
		}
	}
	return &sessionManager{
		dir:    dir,
		log:    log,
		client: librespot.NewHttpClient(),
		state:  state,
		auth:   authStatus{State: "starting"},
	}, nil
}

func (m *sessionManager) start(forceDevice bool) {
	m.mu.Lock()
	if m.starting || (m.session != nil && m.session.alive() && !forceDevice) {
		m.mu.Unlock()
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.starting = true
	m.session.Close()
	m.session = nil
	m.auth = authStatus{State: "starting"}
	if forceDevice {
		m.state.Username = ""
		m.state.Credentials = nil
	}
	m.mu.Unlock()

	go func() {
		err := m.connect(ctx, forceDevice)
		m.mu.Lock()
		m.starting = false
		if err != nil {
			m.auth = authStatus{State: "error", Error: friendlyAuthError(err)}
		} else {
			m.auth = authStatus{State: "signed_in", Username: m.state.Username}
		}
		m.mu.Unlock()
	}()
}

func (m *sessionManager) stop() {
	m.mu.Lock()
	cancel := m.cancel
	sess := m.session
	m.session = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	sess.Close()
}

func (m *sessionManager) logout() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	sess := m.session
	m.session = nil
	m.starting = false
	m.webToken, m.webUser = "", ""
	m.auth = authStatus{State: "signed_out"}
	state := &persistedState{DeviceID: m.state.DeviceID}
	m.state = state
	m.mu.Unlock()
	sess.Close()
	_ = os.Remove(statePath(m.dir))
	_ = savePersistedState(m.dir, state)
}

func (m *sessionManager) status() (authStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.auth, m.session != nil && m.session.alive()
}

func (m *sessionManager) ensureAlive() {
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	if sess != nil && !sess.alive() {
		m.start(false)
	}
}

func (m *sessionManager) setAuth(status authStatus) {
	m.mu.Lock()
	m.auth = status
	m.mu.Unlock()
}

func (m *sessionManager) wait(ctx context.Context) (*spotifySession, error) {
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	if sess != nil && sess.alive() {
		return sess, nil
	}
	m.start(false)
	for {
		m.mu.Lock()
		sess := m.session
		auth := m.auth
		working := m.starting
		m.mu.Unlock()
		if sess != nil && sess.alive() {
			return sess, nil
		}
		if auth.State == "login_required" {
			return nil, &loginRequiredError{auth: auth}
		}
		if auth.State == "error" {
			return nil, errors.New(auth.Error)
		}
		if !working {
			return nil, errors.New("Spotify audio session is not ready")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

type loginRequiredError struct {
	auth authStatus
}

func (e *loginRequiredError) Error() string {
	if e.auth.URL == "" {
		return "Spotify audio sign-in is required"
	}
	if e.auth.Code != "" {
		return fmt.Sprintf("Spotify audio sign-in is required: open %s and enter code %s", e.auth.URL, e.auth.Code)
	}
	return "Spotify audio sign-in is required: open " + e.auth.URL
}

func friendlyAuthError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "audio key") && (strings.Contains(msg, "0 1") || strings.Contains(msg, "code 0")):
		return "Spotify refused this account's audio key. New Spotify accounts may not support librespot playback."
	case strings.Contains(msg, "accesspoint login failed"):
		return "Spotify audio login failed: " + msg
	case strings.Contains(msg, "context canceled"):
		return "Spotify audio sign-in was cancelled"
	default:
		return msg
	}
}

func (m *sessionManager) connect(ctx context.Context, forceDevice bool) (err error) {
	m.mu.Lock()
	state := *m.state
	webToken, webUser := m.webToken, m.webUser
	m.mu.Unlock()

	if state.DeviceID == "" {
		state.DeviceID, err = newDeviceID()
		if err != nil {
			return fmt.Errorf("generate device id: %w", err)
		}
	}
	if _, err := hex.DecodeString(state.DeviceID); err != nil || len(state.DeviceID) != 40 {
		return fmt.Errorf("invalid stored device id")
	}
	if state.ClientToken == "" {
		state.ClientToken, err = retrieveClientToken(m.client, state.DeviceID)
		if err != nil {
			return fmt.Errorf("get Spotify client token: %w", err)
		}
	}
	if err := savePersistedState(m.dir, &state); err != nil {
		return err
	}

	resolver := apresolve.NewApResolver(m.log, m.client, apresolve.FirewallFriendlyPorts)
	apAddr, err := resolver.GetAccesspoint(ctx)
	if err != nil {
		return fmt.Errorf("find Spotify access point: %w", err)
	}
	accesspoint := ap.NewAccesspoint(m.log, apAddr, state.DeviceID)
	closeOnError := true
	defer func() {
		if closeOnError {
			accesspoint.Close()
		}
	}()

	authenticated := false
	if !forceDevice && state.Username != "" && len(state.Credentials) > 0 {
		if err := accesspoint.ConnectStored(ctx, state.Username, state.Credentials); err != nil {
			m.log.WithError(err).Warnf("stored audio login failed; asking to pair again")
			accesspoint.Close()
			accesspoint = ap.NewAccesspoint(m.log, apAddr, state.DeviceID)
		} else {
			authenticated = true
		}
	} else if !forceDevice && webToken != "" {
		if err := accesspoint.ConnectSpotifyToken(ctx, webUser, webToken); err != nil {
			m.log.WithError(err).Warnf("Web API token could not start the audio session; falling back to device login")
			accesspoint.Close()
			accesspoint = ap.NewAccesspoint(m.log, apAddr, state.DeviceID)
		} else {
			authenticated = true
		}
	}
	if !authenticated {
		if err := m.deviceAuthorize(ctx, accesspoint); err != nil {
			return err
		}
	}

	l5 := login5.NewLogin5(m.log, m.client, state.DeviceID, state.ClientToken)
	if err := l5.Login(ctx, &credentialspb.StoredCredential{
		Username: accesspoint.Username(),
		Data:     accesspoint.StoredCredentials(),
	}); err != nil {
		return fmt.Errorf("Spotify login5: %w", err)
	}

	spAddr, err := resolver.GetSpclient(ctx)
	if err != nil {
		return fmt.Errorf("find Spotify metadata service: %w", err)
	}
	sp, err := spclient.NewSpclient(ctx, m.log, m.client, spAddr, l5.AccessToken(), state.DeviceID, state.ClientToken)
	if err != nil {
		return fmt.Errorf("start Spotify metadata client: %w", err)
	}

	state.Username = accesspoint.Username()
	state.Credentials = append([]byte(nil), accesspoint.StoredCredentials()...)
	if err := savePersistedState(m.dir, &state); err != nil {
		return err
	}

	sess := &spotifySession{
		ap:       accesspoint,
		sp:       sp,
		audioKey: audio.NewAudioKeyProvider(m.log, accesspoint),
		login5:   l5,
		client:   m.client,
		log:      m.log,
	}
	m.mu.Lock()
	if ctx.Err() != nil {
		m.mu.Unlock()
		return ctx.Err()
	}
	m.state = &state
	m.session = sess
	m.mu.Unlock()
	closeOnError = false
	return nil
}

var spotifyScopes = []string{
	"app-remote-control",
	"playlist-modify",
	"playlist-modify-private",
	"playlist-modify-public",
	"playlist-read",
	"playlist-read-collaborative",
	"playlist-read-private",
	"streaming",
	"user-library-modify",
	"user-library-read",
	"user-modify-playback-state",
	"user-read-currently-playing",
	"user-read-email",
	"user-read-playback-position",
	"user-read-playback-state",
	"user-read-private",
	"user-read-recently-played",
	"user-top-read",
}

func (m *sessionManager) deviceAuthorize(ctx context.Context, accesspoint *ap.Accesspoint) error {
	conf := &oauth2.Config{
		ClientID: librespot.ClientIdHex,
		Scopes:   spotifyScopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:       "https://accounts.spotify.com/authorize",
			TokenURL:      "https://accounts.spotify.com/api/token",
			DeviceAuthURL: "https://accounts.spotify.com/oauth2/device/authorize",
		},
	}
	da, err := conf.DeviceAuth(ctx)
	if err != nil {
		return fmt.Errorf("start Spotify device login: %w", err)
	}
	authURL := da.VerificationURIComplete
	if authURL == "" {
		authURL = da.VerificationURI
	}
	m.setAuth(authStatus{
		State:     "login_required",
		URL:       authURL,
		Code:      da.UserCode,
		ExpiresAt: da.Expiry.Unix(),
	})
	token, err := conf.DeviceAccessToken(ctx, da)
	if err != nil {
		return fmt.Errorf("complete Spotify device login: %w", err)
	}
	username, _ := token.Extra("username").(string)
	if err := accesspoint.ConnectSpotifyToken(ctx, username, token.AccessToken); err != nil {
		return fmt.Errorf("use Spotify device token: %w", err)
	}
	m.setAuth(authStatus{State: "starting"})
	return nil
}

func retrieveClientToken(c *http.Client, deviceID string) (string, error) {
	body, err := proto.Marshal(&pbhttp.ClientTokenRequest{
		RequestType: pbhttp.ClientTokenRequestType_REQUEST_CLIENT_DATA_REQUEST,
		Request: &pbhttp.ClientTokenRequest_ClientData{
			ClientData: &pbhttp.ClientDataRequest{
				ClientId:      librespot.ClientIdHex,
				ClientVersion: librespot.SpotifyLikeClientVersion(),
				Data: &pbhttp.ClientDataRequest_ConnectivitySdkData{
					ConnectivitySdkData: &pbdata.ConnectivitySdkData{
						DeviceId:             deviceID,
						PlatformSpecificData: librespot.GetPlatformSpecificData(),
					},
				},
			},
		},
	})
	if err != nil {
		return "", err
	}
	reqURL, _ := url.Parse("https://clienttoken.spotify.com/v1/clienttoken")
	req := &http.Request{
		Method: "POST",
		URL:    reqURL,
		Header: http.Header{
			"Accept":     []string{"application/x-protobuf"},
			"User-Agent": []string{librespot.UserAgent()},
		},
		Body: io.NopCloser(bytes.NewReader(body)),
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("clienttoken returned %s", resp.Status)
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var protoResp pbhttp.ClientTokenResponse
	if err := proto.Unmarshal(respBody, &protoResp); err != nil {
		return "", err
	}
	if protoResp.ResponseType != pbhttp.ClientTokenResponseType_RESPONSE_GRANTED_TOKEN_RESPONSE {
		return "", fmt.Errorf("clienttoken response %s is not supported", protoResp.ResponseType)
	}
	token := protoResp.GetGrantedToken().GetToken()
	if token == "" {
		return "", errors.New("Spotify clienttoken was empty")
	}
	return token, nil
}
