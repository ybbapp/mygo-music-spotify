package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type persistedState struct {
	DeviceID    string `json:"device_id,omitempty"`
	ClientToken string `json:"client_token,omitempty"`
	Username    string `json:"username,omitempty"`
	Credentials []byte `json:"credentials,omitempty"`
}

func statePath(dir string) string {
	return filepath.Join(dir, "audio-state.json")
}

func loadPersistedState(dir string) (*persistedState, error) {
	state := &persistedState{}
	b, err := os.ReadFile(statePath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audio state: %w", err)
	}
	if err := json.Unmarshal(b, state); err != nil {
		return nil, fmt.Errorf("decode audio state: %w", err)
	}
	return state, nil
}

func savePersistedState(dir string, state *persistedState) error {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode audio state: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make audio data dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".audio-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create audio state temp file: %w", err)
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(b)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		dest := statePath(dir)
		if err = os.Rename(name, dest); err != nil {
			// Windows cannot replace an existing file with os.Rename.
			if _, statErr := os.Stat(dest); statErr == nil {
				if removeErr := os.Remove(dest); removeErr == nil {
					err = os.Rename(name, dest)
				}
			}
		}
	}
	if err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("save audio state: %w", err)
	}
	return nil
}

func newDeviceID() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
