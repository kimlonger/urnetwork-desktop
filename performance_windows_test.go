package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInitialSpeedPreference(t *testing.T) {
	root := t.TempDir()
	local := t.TempDir()
	if initialSpeedPreference(root, local) {
		t.Fatal("missing desktop preference must use auto")
	}
	desktop := filepath.Join(local, "URnetwork", "app", "storage", "network_spaces", "bringyour.com", "main", ".by", ".performance_profile")
	if err := os.MkdirAll(filepath.Dir(desktop), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktop, []byte(`{"window_type":"speed","window_size":{"window_size_min":1,"window_size_max":1},"allow_direct":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if !initialSpeedPreference(root, local) {
		t.Fatal("desktop speed mode should be offered")
	}
	if err := saveSpeedPreference(root, false); err != nil {
		t.Fatal(err)
	}
	if initialSpeedPreference(root, local) {
		t.Fatal("explicit proxy preference must override desktop default")
	}
	if err := saveSpeedPreference(root, true); err != nil {
		t.Fatal(err)
	}
	if !initialSpeedPreference(root, local) {
		t.Fatal("saved proxy speed mode was not restored")
	}
	if err := os.WriteFile(filepath.Join(root, "desktop-performance.json"), []byte(`{"enabled":"invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if initialSpeedPreference(root, local) {
		t.Fatal("invalid proxy preference must fail safely")
	}
}

func TestSystemProxyIsNotOverriddenByDefault(t *testing.T) {
	for _, tc := range []struct {
		flags uint32
		err   error
		want  bool
	}{
		{proxyTypeDirect, nil, true},
		{proxyTypeDirect | proxyTypeManual, nil, false},
		{proxyTypeDirect | proxyTypeAutoConfig, nil, false},
		{proxyTypeDirect | proxyTypeAutoDetect, nil, false},
		{0, errors.New("query failed"), false},
	} {
		if got := shouldEnableSystemProxyByDefault(proxySettings{Flags: tc.flags}, tc.err); got != tc.want {
			t.Fatalf("flags=%d got=%v", tc.flags, got)
		}
	}
}

func TestDesktopSpeedRequiresSingleExit(t *testing.T) {
	root, local := t.TempDir(), t.TempDir()
	path := filepath.Join(local, "URnetwork", "app", "storage", "network_spaces", "bringyour.com", "main", ".by", ".performance_profile")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		`{"window_type":"auto"}`,
		`{"window_type":"quality","window_size":{"window_size_min":1,"window_size_max":1}}`,
		`{"window_type":"speed","window_size":{"window_size_min":2,"window_size_max":4}}`,
		`{invalid`,
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if initialSpeedPreference(root, local) {
			t.Fatal("unmatched desktop profile must not select single-exit speed mode")
		}
	}
}

func TestSpeedFallbackOnlyOnConnectionFailure(t *testing.T) {
	for _, tc := range []struct {
		enabled   bool
		state     string
		providers int64
		want      bool
	}{
		{true, "CONNECTING", 0, false},
		{true, "CONNECTED", 1, false},
		{false, "CONNECT_FAILED", 0, false},
		{true, "CONNECT_FAILED", 0, true},
	} {
		if got := shouldFallbackFromSpeed(tc.enabled, sdkConnectionStatus{State: tc.state, Providers: tc.providers}); got != tc.want {
			t.Fatalf("fallback enabled=%v state=%s got=%v", tc.enabled, tc.state, got)
		}
	}
}
