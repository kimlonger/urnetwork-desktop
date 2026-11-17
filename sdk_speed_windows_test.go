package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestSDKABIForComparison(t *testing.T) {
	dllPath := os.Getenv("URNET_BENCH_SDK")
	if dllPath == "" {
		t.Skip("explicit SDK check not requested")
	}
	s, err := loadSDK(dllPath)
	if err != nil {
		t.Fatalf("SDK export validation failed: %v", err)
	}
	defer s.close()
	if err := s.init(""); err != nil {
		t.Fatalf("SDK initialization failed: %v", err)
	}
	if !validID(s.newID()) {
		t.Fatal("SDK could not create a valid identifier")
	}
	fmt.Println("URNET_BENCH|abi|OK")
}

func TestSDKLicensesForComparison(t *testing.T) {
	dllPath := os.Getenv("URNET_BENCH_SDK")
	output := os.Getenv("URNET_BENCH_LICENSE_OUTPUT")
	if dllPath == "" || output == "" {
		t.Skip("explicit SDK license export not requested")
	}
	s, err := loadSDK(dllPath)
	if err != nil {
		t.Fatal("SDK could not be loaded")
	}
	defer s.close()
	licenses, err := s.licenses()
	if err != nil {
		t.Fatal("SDK license data is unavailable")
	}
	if err := os.WriteFile(output, []byte(licenses+"\n"), 0600); err != nil {
		t.Fatal("SDK license data could not be saved")
	}
	fmt.Println("URNET_BENCH|licenses|OK")
}

// This opt-in integration test uses the existing local proxy credential. It
// never enables the system proxy, launches the UI, or changes VPN settings.
func TestSDKSpeedComparison(t *testing.T) {
	dllPath := os.Getenv("URNET_BENCH_SDK")
	if dllPath == "" {
		t.Skip("explicit SDK benchmark not requested")
	}
	label := os.Getenv("URNET_BENCH_LABEL")
	if label != "old" && label != "new" {
		t.Fatal("invalid benchmark label")
	}
	storage := os.Getenv("URNET_BENCH_STORAGE")
	if storage == "" {
		t.Fatal("isolated SDK storage path is required")
	}
	statePath, err := guestStatePath()
	if err != nil {
		t.Fatal("the existing proxy state directory is unavailable")
	}
	state, err := loadSession(statePath)
	if err != nil || validateDeviceSession(state) != nil {
		t.Fatal("the existing proxy session is unavailable")
	}
	s, err := loadSDK(dllPath)
	if err != nil {
		t.Fatalf("SDK could not be loaded: %v", err)
	}
	defer s.close()
	if err := s.init(filepath.Join(storage, "sdk-storage")); err != nil {
		t.Fatal("SDK initialization failed")
	}
	providerID := os.Getenv("URNET_BENCH_PROVIDER_ID")
	if providerID != "" && !validID(providerID) {
		t.Fatal("the benchmark provider ID is invalid")
	}
	if err := s.start(state.JWT, state.Instance, "", providerID); err != nil {
		t.Fatal("SDK connection could not be started")
	}
	controller := s.call("device_local_open_connect_view_controller", s.device)
	if controller != 0 {
		s.call("connect_view_controller_start", controller)
		defer s.call("release", controller)
		defer s.call("device_local_close_connect_view_controller", s.device, controller)
	}

	var sdkDials atomic.Int64
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		sdkDials.Add(1)
		return s.dial(ctx, network, address)
	}
	var active activePolicy
	active.current.Store(&policy{profiles: map[string]profile{"benchmark": {mode: ur}}})
	server := httptest.NewServer(proxy{name: "benchmark", policy: &active, upstream: dial})
	defer server.Close()
	defer active.closeTunnels("")
	proxyURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal("local benchmark proxy URL is invalid")
	}

	clientFor := func(method string) (*http.Client, *http.Transport) {
		transport := &http.Transport{
			DisableKeepAlives:     true,
			DisableCompression:    true,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			ForceAttemptHTTP2:     false,
		}
		if method == "socket" {
			transport.DialContext = dial
		} else {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
		return &http.Client{Transport: transport, Timeout: 35 * time.Second}, transport
	}

	request := func(method, destination string, maximum int64) (int64, time.Duration, time.Duration, error) {
		client, transport := clientFor(method)
		defer transport.CloseIdleConnections()
		timeout := 35 * time.Second
		if maximum == 64 {
			timeout = 12 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, destination, nil)
		if err != nil {
			return 0, 0, 0, err
		}
		req.Header.Set("Cache-Control", "no-cache")
		req.Header.Set("Accept-Encoding", "identity")
		started := time.Now()
		before := sdkDials.Load()
		response, err := client.Do(req)
		if err != nil {
			return 0, time.Since(started), 0, err
		}
		defer response.Body.Close()
		ttfb := time.Since(started)
		if response.StatusCode != http.StatusOK || sdkDials.Load() <= before {
			return 0, ttfb, 0, fmt.Errorf("unexpected response or bypassed SDK")
		}
		bodyStart := time.Now()
		n, err := io.Copy(io.Discard, io.LimitReader(response.Body, maximum+1))
		return n, ttfb, time.Since(bodyStart), err
	}

	// A successful warmup ensures a provider is usable before the samples.
	const warmup = "https://speed.cloudflare.com/__down?bytes=64"
	ready := false
	deadline := time.Now().Add(90 * time.Second)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		started := time.Now()
		n, _, _, err := request("socket", warmup, 64)
		if err == nil && n == 64 {
			ready = true
			break
		}
		kind := "other"
		if err != nil {
			message := err.Error()
			switch {
			case strings.Contains(message, "URnetwork dial failed"):
				kind = "sdk-dial-failed"
			case strings.Contains(message, "deadline") || strings.Contains(message, "timeout"):
				kind = "timeout"
			case strings.Contains(message, "EOF"):
				kind = "eof"
			case strings.Contains(message, "certificate"):
				kind = "certificate"
			}
		}
		status := sdkConnectionStatus{}
		if controller != 0 {
			status = s.connectionStatus(controller)
		}
		fmt.Printf("URNET_BENCH|%s|warmup|%d|%s|%.1f|%s|%d\n", label, attempt+1, kind, time.Since(started).Seconds(), status.State, status.Providers)
		time.Sleep(2 * time.Second)
	}
	if !ready {
		t.Fatal("SDK provider was not ready for the benchmark")
	}
	if os.Getenv("URNET_BENCH_DISCOVER_PROVIDER") == "1" {
		lib, err := syscall.LoadDLL(dllPath)
		if err != nil {
			t.Fatal("provider discovery SDK loading failed")
		}
		defer lib.Release()
		proc, err := lib.FindProc("urnet_connect_grid_get_provider_grid_point_list")
		if err != nil {
			t.Fatal("provider discovery is unavailable")
		}
		s.procs["connect_grid_get_provider_grid_point_list"] = proc
		grid := s.call("connect_view_controller_get_grid", controller)
		if grid == 0 {
			t.Fatal("provider discovery grid is unavailable")
		}
		ptr := s.call("connect_grid_get_provider_grid_point_list", grid)
		data := copyCString(ptr)
		if ptr != 0 {
			s.call("free_string", ptr)
		}
		s.call("release", grid)
		var value any
		if json.Unmarshal([]byte(data), &value) != nil {
			t.Fatal("provider discovery returned invalid data")
		}
		var ids, activeIDs, uuidKeys []string
		var inspect func(any)
		inspect = func(item any) {
			switch item := item.(type) {
			case map[string]any:
				candidate := ""
				active := false
				for key, child := range item {
					if validID(key) {
						uuidKeys = append(uuidKeys, key)
					}
					if text, ok := child.(string); ok && validID(text) && (strings.EqualFold(key, "client_id") || strings.EqualFold(key, "clientid")) {
						ids = append(ids, text)
						candidate = text
					}
					if flag, ok := child.(bool); ok && strings.EqualFold(key, "active") {
						active = flag
					}
					inspect(child)
				}
				if active && candidate != "" {
					activeIDs = append(activeIDs, candidate)
				}
			case []any:
				for _, child := range item {
					inspect(child)
				}
			}
		}
		inspect(value)
		fmt.Printf("URNET_BENCH|%s|discovery|client-ids=%d|active=%d|uuid-keys=%d\n", label, len(ids), len(activeIDs), len(uuidKeys))
		if len(activeIDs) > 0 {
			fmt.Printf("URNET_PRIVATE_PROVIDER|%s\n", activeIDs[0])
		}
		return
	}

	// Record only an HMAC of the exit address, never the address itself. The
	// controller uses one random HMAC key across runs to spot exit changes.
	if key := os.Getenv("URNET_BENCH_HMAC_KEY"); key != "" {
		client, transport := clientFor("socket")
		response, err := client.Get("https://speed.cloudflare.com/cdn-cgi/trace")
		if err == nil {
			data, readErr := io.ReadAll(io.LimitReader(response.Body, 8192))
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(line, "ip=") {
						mac := hmac.New(sha256.New, []byte(key))
						_, _ = mac.Write([]byte(strings.TrimPrefix(line, "ip=")))
						fmt.Printf("URNET_BENCH|%s|exit|%s\n", label, hex.EncodeToString(mac.Sum(nil))[:12])
					}
				}
			}
		}
		transport.CloseIdleConnections()
	}

	order := []string{"socket", "proxy", "proxy", "socket"}
	if os.Getenv("URNET_BENCH_REVERSE") == "1" {
		order = []string{"proxy", "socket", "socket", "proxy"}
	}
	// Small enough to complete on the slow path; retain failures explicitly.
	const size = int64(256_000)
	for index, method := range order {
		url := fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=%d&run=%s-%d-%d", size, label, index, time.Now().UnixNano())
		n, ttfb, body, err := request(method, url, size)
		if err != nil || n != size || body <= 0 {
			fmt.Printf("URNET_BENCH|%s|%s|%d|FAIL|%d|%.3f|%.3f\n", label, method, index+1, n, ttfb.Seconds(), body.Seconds())
			continue
		}
		fmt.Printf("URNET_BENCH|%s|%s|%d|OK|%d|%.3f|%.3f\n", label, method, index+1, n, ttfb.Seconds(), body.Seconds())
	}
}
