package main

import (
	"context"
	"os"
	"testing"
)

func TestStartKeepsAnExistingSession(t *testing.T) {
	sess := &spotifySession{}
	m := &sessionManager{
		state:   &persistedState{},
		session: sess,
		auth:    authStatus{State: "signed_in"},
	}
	m.start(false)
	if m.session != sess || m.starting {
		t.Fatalf("existing session was replaced")
	}
}

func TestWaitReturnsAnExistingSession(t *testing.T) {
	sess := &spotifySession{}
	m := &sessionManager{
		state:   &persistedState{},
		session: sess,
		auth:    authStatus{State: "signed_in"},
	}
	got, err := m.wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != sess {
		t.Fatalf("wait returned %p, want %p", got, sess)
	}
}

func TestLogoutForgetsAudioCredentials(t *testing.T) {
	dir := t.TempDir()
	m := &sessionManager{
		dir:   dir,
		state: &persistedState{DeviceID: "0123456789012345678901234567890123456789", Username: "u", Credentials: []byte("secret")},
	}
	m.logout()
	if m.auth.State != "signed_out" || m.session != nil {
		t.Fatalf("logout left %+v / %v", m.auth, m.session)
	}
	state, err := loadPersistedState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Username != "" || len(state.Credentials) != 0 {
		t.Fatalf("credentials survived logout: %+v", state)
	}
	if state.DeviceID == "" {
		t.Fatal("device id was forgotten")
	}
	if _, err := os.Stat(statePath(dir)); err != nil {
		t.Fatal(err)
	}
}
