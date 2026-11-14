package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type dialFunc func(context.Context, string, string) (net.Conn, error)

type proxy struct {
	name     string
	policy   *activePolicy
	direct   dialFunc
	upstream dialFunc
	verbose  bool
}

func destination(authority, fallbackPort string) (string, string, error) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		host, port = strings.TrimPrefix(strings.TrimSuffix(authority, "]"), "["), fallbackPort
	}
	host = normalizeHost(host)
	ip, ipErr := netip.ParseAddr(host)
	number, portErr := strconv.ParseUint(port, 10, 16)
	if (ipErr != nil && !validHost(host)) || (ipErr == nil && ip.Zone() != "") || portErr != nil || number == 0 {
		return "", "", fmt.Errorf("invalid proxy target")
	}
	return host, net.JoinHostPort(host, port), nil
}

func (p proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var authority string
	if r.Method == http.MethodConnect {
		authority = r.Host
	} else if r.URL.IsAbs() && r.URL.Scheme == "http" && r.URL.User == nil {
		authority = r.URL.Host
	} else {
		http.Error(w, "Use this port as an HTTP proxy", http.StatusBadRequest)
		return
	}
	fallbackPort := "80"
	if r.Method == http.MethodConnect {
		fallbackPort = "443"
	}
	host, address, err := destination(authority, fallbackPort)
	if err != nil {
		http.Error(w, "Invalid destination", http.StatusBadRequest)
		return
	}
	route, reason := p.policy.current.Load().choose(p.name, host)
	if p.verbose {
		log.Printf("%s %s %s (%s)", p.name, route, address, reason)
	}
	if route == block {
		http.Error(w, "Blocked by local proxy policy", http.StatusForbidden)
		return
	}
	dial := p.direct
	if route == ur {
		dial = p.upstream
		if dial == nil {
			http.Error(w, "URnetwork session is unavailable", http.StatusBadGateway)
			return
		}
	}
	if r.Method == http.MethodConnect {
		p.tunnel(w, r, dial, host, address, route)
		return
	}
	p.forward(w, r, dial, address, route)
}

func (p proxy) forward(w http.ResponseWriter, r *http.Request, dial dialFunc, address string, route action) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(ctx, "tcp", address)
		},
		DisableKeepAlives:     true,
		DisableCompression:    true,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	defer transport.CloseIdleConnections()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Host = r.URL.Host
	out.Header = r.Header.Clone()
	stripHopHeaders(out.Header)
	start := time.Now()
	resp, err := transport.RoundTrip(out)
	if err != nil {
		p.logFailure(route, r.Method, start, err)
		http.Error(w, "Outbound connection failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	stripHopHeaders(resp.Header)
	for k, vals := range resp.Header {
		w.Header()[k] = vals
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p proxy) logFailure(route action, method string, start time.Time, err error) {
	var dns *net.DNSError
	var network net.Error
	reason := "连接失败"
	switch {
	case errors.Is(err, context.Canceled):
		return
	case errors.As(err, &dns):
		reason = "DNS 解析失败"
	case errors.As(err, &network) && network.Timeout():
		reason = "连接超时"
	}
	log.Printf("代理请求失败 profile=%s route=%s connect=%t elapsed=%s：%s", p.name, route, method == http.MethodConnect, time.Since(start).Round(time.Millisecond), reason)
}

func stripHopHeaders(headers http.Header) {
	for _, value := range headers.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			headers.Del(strings.TrimSpace(name))
		}
	}
	for _, k := range []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		headers.Del(k)
	}
}

func (p proxy) tunnel(w http.ResponseWriter, r *http.Request, dial dialFunc, host, address string, route action) {
	generation := p.policy.exitGeneration.Load()
	start := time.Now()
	up, err := dial(r.Context(), "tcp", address)
	if err != nil {
		p.logFailure(route, http.MethodConnect, start, err)
		http.Error(w, "Outbound connection failed", http.StatusBadGateway)
		return
	}
	defer up.Close()
	client, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "Cannot establish proxy tunnel", http.StatusInternalServerError)
		return
	}
	defer client.Close()
	t := &activeTunnel{profile: p.name, host: host, route: route, exitGeneration: generation, close: func() {
		_ = client.Close()
		_ = up.Close()
	}}
	if !p.policy.track(t) {
		return
	}
	defer p.policy.untrack(t)
	_ = client.SetDeadline(time.Time{})
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buffered.Flush(); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	copySide := func(dst net.Conn, src io.Reader) {
		_, err := io.Copy(dst, src)
		if half, ok := dst.(interface{ CloseWrite() error }); err == nil && ok {
			_ = half.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go copySide(up, buffered)
	go copySide(client, up)
	<-done
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = client.Close()
		_ = up.Close()
		<-done
	}
}
