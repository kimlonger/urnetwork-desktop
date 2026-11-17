package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSDKInitializesPersistentNetworkSpace(t *testing.T) {
	storage := t.TempDir()
	for range 2 {
		connection, err := loadSDK("URnetworkSdk.dll")
		if err != nil {
			t.Fatal(err)
		}
		err = connection.init(storage)
		connection.close()
		if err != nil {
			t.Fatal(err)
		}
		state, err := os.ReadFile(filepath.Join(storage, ".network_spaces"))
		if err != nil || !strings.Contains(string(state), "bringyour.com") {
			t.Fatalf("SDK network space was not persisted: %v", err)
		}
	}
}

func TestDashboardTrafficCountsBothDirectionsBeforeClose(t *testing.T) {
	var counters trafficCounters
	local, peer := net.Pipe()
	defer peer.Close()
	dial := counters.count(func(context.Context, string, string) (net.Conn, error) { return local, nil })
	conn, err := dial(context.Background(), "tcp", "example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		_, _ = io.ReadFull(peer, make([]byte, 2))
		_, _ = peer.Write([]byte("reply"))
	}()
	if _, err := conn.Write([]byte("up")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	sample := counters.sample(trafficSample{}, time.Second)
	if sample != (trafficSample{Upload: 2, Download: 5, UpRate: 2, DownRate: 5}) {
		t.Fatalf("unexpected sample: %+v", sample)
	}
}

func TestDashboardExitTrace(t *testing.T) {
	exit, err := parseExitTrace(strings.NewReader("ip=2001:db8::1\nloc=US\n"))
	if err != nil || exit.IP != "2001:db8::1" || exit.Country != "US" {
		t.Fatalf("parse trace: %+v, %v", exit, err)
	}
	if _, err := parseExitTrace(strings.NewReader("ip=127.0.0.1\nloc=US\n")); err == nil {
		t.Fatal("loopback address accepted as a public exit")
	}
}

func TestDashboardDirectRulesTakePriority(t *testing.T) {
	host, err := websiteTarget("https://Example.Test/docs")
	if err != nil || host != "example.test" {
		t.Fatalf("parse website: %s, %v", host, err)
	}
	ip, err := ipTarget("192.0.2.45/24")
	if err != nil || ip != "192.0.2.0/24" {
		t.Fatalf("parse network: %s, %v", ip, err)
	}
	p, err := (config{Default: ur, Profiles: map[string]profileConfig{
		"auto": {Port: 18790, Mode: auto, Rules: []ruleConfig{{Target: "+.example.test", Action: block}}},
	}}).compile(&policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := (bypassEntries{Websites: []string{host}, IPs: []string{ip}}).applyTo(p); err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]action{"cdn.example.test": direct, "192.0.2.37": direct, "other.test": ur} {
		if got, _ := p.choose("auto", host); got != want {
			t.Errorf("%s route = %s, want %s", host, got, want)
		}
	}
}
