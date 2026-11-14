package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestChangedRuleClosesExistingTunnel(t *testing.T) {
	destination, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	go func() {
		if c, err := destination.Accept(); err == nil {
			defer c.Close()
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	var active activePolicy
	active.current.Store(&policy{profiles: map[string]profile{"test": {mode: direct}}})
	server := httptest.NewServer(proxy{name: "test", policy: &active, direct: (&net.Dialer{}).DialContext})
	defer server.Close()
	c, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	target := destination.Addr().String()
	_, _ = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	reader := bufio.NewReader(c)
	if response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect}); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT failed: %v", err)
	}
	active.apply(&policy{profiles: map[string]profile{"test": {mode: block}}})
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Fatalf("existing tunnel was not closed: %v", err)
	}
}

func TestHTTPProxyUsesProfileAndHotReload(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "local "+r.Host)
	}))
	defer local.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "upstream "+r.Host)
	}))
	defer upstream.Close()
	geo, err := loadGeo(".")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rules.json")
	write := func(document string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"default":"ur","profiles":{"test":{"port":18790,"mode":"auto","rules":[]}}}`)
	first, snapshot, err := readPolicy(path, geo)
	if err != nil {
		t.Fatal(err)
	}
	var active activePolicy
	active.current.Store(first)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reloadRules(ctx, path, geo, &active, snapshot)
	dial := func(server *httptest.Server) dialFunc {
		return func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
		}
	}
	server := httptest.NewServer(proxy{name: "test", policy: &active, direct: dial(local), upstream: dial(upstream)})
	defer server.Close()
	proxyURL, _ := url.Parse(server.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	check := func(target, want string, status int) {
		t.Helper()
		response, err := client.Get("http://" + target)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status || string(body) != want {
			t.Fatalf("%s: %d %q %v", target, response.StatusCode, body, err)
		}
	}
	check("meeting.tencent.com", "local meeting.tencent.com", 200)
	check("223.5.5.5", "local 223.5.5.5", 200)
	check("example.org", "upstream example.org", 200)
	write(`{"default":"ur","rules":[{"target":"223.5.5.5","action":"block"}],"profiles":{"test":{"port":18790,"mode":"direct","rules":[]}}}`)
	deadline := time.Now().Add(4 * time.Second)
	for active.current.Load() == first && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	check("223.5.5.5", "Blocked by local proxy policy\n", 403)
	check("example.org", "local example.org", 200)
	updated := active.current.Load()
	write(`{"default":"invalid"}`)
	time.Sleep(1200 * time.Millisecond)
	if active.current.Load() != updated {
		t.Fatal("invalid edit replaced the last valid rules")
	}
}

func TestConnectUsesProxyStream(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "TLS passed through")
	}))
	defer server.Close()
	var active activePolicy
	active.current.Store(&policy{fallback: ur, profiles: map[string]profile{"test": {mode: ur}}})
	local := httptest.NewServer(proxy{name: "test", policy: &active, upstream: (&net.Dialer{}).DialContext})
	defer local.Close()
	proxyURL, _ := url.Parse(local.URL)
	client := server.Client()
	client.Timeout = 5 * time.Second
	client.Transport.(*http.Transport).Proxy = http.ProxyURL(proxyURL)
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || string(data) != "TLS passed through" {
		t.Fatalf("unexpected TLS response: %q %v", data, err)
	}
}
