package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuestAuthorizationUsesSeparateAccountAndDeviceCredentials(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000002"
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"client_id":"` + id + `"}`))
	token := "header." + claims + ".signature"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/network-create":
			var body map[string]bool
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" || json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || !body["terms"] {
				http.Error(w, "unexpected create request", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"network":{"by_jwt":"account-test-token"},"seedphrase":"mock recovery phrase"}`)
		case "/network/auth-client":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer account-test-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"client_id":%q,"by_client_jwt":%q}`, id, token)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a := &authorization{endpoint: server.URL, client: server.Client()}
	jwt, err := a.createGuest()
	if err != nil || jwt != "account-test-token" {
		t.Fatalf("create failed: %v", err)
	}
	got, err := a.registerGuest(jwt)
	if err != nil || got != token {
		t.Fatalf("register failed: %v", err)
	}
}

func TestGuestAuthorizationReportsStageWithoutExposingBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sensitive test value", http.StatusTooManyRequests)
	}))
	defer server.Close()
	a := &authorization{endpoint: server.URL, client: server.Client()}
	_, err := a.createGuest()
	if err == nil || !strings.Contains(err.Error(), "开通访客身份") || !strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unexpected error: %v", err)
	}
}
