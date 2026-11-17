package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveSpeedProfileTransitions(t *testing.T) {
	dllPath := os.Getenv("URNET_LIVE_SDK")
	if dllPath == "" {
		t.Skip("live SDK check not requested")
	}
	path, err := guestStatePath()
	if err != nil {
		t.Fatal("proxy session unavailable")
	}
	state, err := loadSession(path)
	if err != nil || validateDeviceSession(state) != nil {
		t.Fatal("proxy session unavailable")
	}
	s, err := loadSDK(dllPath)
	if err != nil {
		t.Fatal("SDK load failed")
	}
	defer s.close()
	if err := s.init(""); err != nil {
		t.Fatal("SDK initialization failed")
	}
	if err := s.start(state.JWT, state.Instance, "", ""); err != nil {
		t.Fatal("SDK connection failed")
	}
	if err := s.useSpeedProfile(); err != nil {
		t.Fatal("speed profile failed validation")
	}
	if err := s.useAutoProfile(); err != nil {
		t.Fatal("automatic fallback profile failed validation")
	}
	if err := s.useSpeedProfile(); err != nil {
		t.Fatal("speed profile reactivation failed validation")
	}
	fmt.Printf("URNET_LIVE|desktop-speed-default|%t\n", initialSpeedPreference(t.TempDir(), os.Getenv("LOCALAPPDATA")))
	fmt.Println("URNET_LIVE|profile-transitions|OK")
}

// This integration check is opt-in, uses in-memory SDK state and never
// changes the system proxy or the official desktop session.
func TestLiveSpeedProfile(t *testing.T) {
	dllPath := os.Getenv("URNET_LIVE_SDK")
	if dllPath == "" {
		t.Skip("live SDK check not requested")
	}
	path, err := guestStatePath()
	if err != nil {
		t.Fatal("proxy session unavailable")
	}
	state, err := loadSession(path)
	if err != nil || validateDeviceSession(state) != nil {
		t.Fatal("proxy session unavailable")
	}
	s, err := loadSDK(dllPath)
	if err != nil {
		t.Fatal("SDK load failed")
	}
	defer s.close()
	if err := s.init(""); err != nil {
		t.Fatal("SDK initialization failed")
	}
	if err := s.start(state.JWT, state.Instance, "", ""); err != nil {
		t.Fatal("SDK connection failed")
	}
	if err := s.useSpeedProfile(); err != nil {
		t.Fatal("speed profile failed validation")
	}
	controller := s.call("device_local_open_connect_view_controller", s.device)
	if controller != 0 {
		s.call("connect_view_controller_start", controller)
		defer s.call("release", controller)
		defer s.call("device_local_close_connect_view_controller", s.device, controller)
	}
	var dials atomic.Int64
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		return s.dial(ctx, network, address)
	}
	var active activePolicy
	active.current.Store(&policy{profiles: map[string]profile{"benchmark": {mode: ur}}})
	server := httptest.NewServer(proxy{name: "benchmark", policy: &active, upstream: dial})
	defer server.Close()
	defer active.closeTunnels("")
	proxyURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal("local test proxy invalid")
	}
	deadline := time.Now().Add(120 * time.Second)
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true, TLSHandshakeTimeout: 12 * time.Second}
		client := &http.Client{Transport: transport, Timeout: 18 * time.Second}
		before := dials.Load()
		response, err := client.Get("https://speed.cloudflare.com/__down?bytes=65536")
		if err == nil {
			n, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 65537))
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == 200 && n == 65536 && dials.Load() > before {
				transport.CloseIdleConnections()
				if err := s.useAutoProfile(); err != nil {
					t.Fatal("automatic fallback profile failed validation")
				}
				fmt.Println("URNET_LIVE|speed-profile|proxy-download|OK")
				return
			}
		}
		transport.CloseIdleConnections()
		status := sdkConnectionStatus{}
		if controller != 0 {
			status = s.connectionStatus(controller)
		}
		fmt.Printf("URNET_LIVE|warmup|%d|%s|%d\n", attempt, status.State, status.Providers)
		time.Sleep(2 * time.Second)
	}
	t.Fatal("speed profile provider not ready")
}
