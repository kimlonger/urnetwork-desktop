package main

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeGuest struct {
	created, registered int
}

func (s *fakeGuest) newID() string { return "00000000-0000-0000-0000-000000000001" }

func (s *fakeGuest) createGuest() (string, error) {
	s.created++
	return "mock temporary network credential", nil
}

func (s *fakeGuest) registerGuest(jwt string) (string, error) {
	s.registered++
	if jwt != "mock temporary network credential" || s.registered == 1 {
		return "", errors.New("mock registration failure")
	}
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"client_id":"00000000-0000-0000-0000-000000000002"}`))
	return "header." + claims + ".signature", nil
}

func TestGuestProvisionResumesAndReusesDevice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.dpapi")
	s := &fakeGuest{}
	if _, err := provisionSession(path, s, s.newID, func() bool { return false }); err == nil || s.created != 0 {
		t.Fatal("declining terms should leave device unregistered")
	}
	if _, err := provisionSession(path, s, s.newID, func() bool { return true }); err == nil || s.created != 1 || s.registered != 1 {
		t.Fatal("expected one guest account and a failed device registration")
	}
	noPrompt := func() bool { t.Fatal("unexpected terms prompt"); return false }
	state, err := provisionSession(path, s, s.newID, noPrompt)
	if err != nil || state.JWT == "" || state.PendingNetworkJWT != "" {
		t.Fatalf("expected usable device session: %v", err)
	}
	again, err := provisionSession(path, s, s.newID, noPrompt)
	if err != nil || again != state || s.created != 1 || s.registered != 2 {
		t.Fatal("expected the saved device, without creating a guest again")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) == state.JWT {
		t.Fatal("session was not encrypted")
	}
}
