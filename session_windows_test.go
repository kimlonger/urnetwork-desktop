package main

import (
	"path/filepath"
	"testing"
)

func TestEncryptedSessionCanBeReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.dpapi")
	want := session{JWT: "test-value", Instance: "00000000-0000-0000-0000-000000000001"}
	for _, token := range []string{"first dummy value", "second dummy value"} {
		want.JWT = token
		if err := saveSession(path, want); err != nil {
			t.Fatal(err)
		}
		got, err := loadSession(path)
		if got != want || err != nil {
			t.Fatalf("encrypted session round trip failed: %v", err)
		}
	}
}
