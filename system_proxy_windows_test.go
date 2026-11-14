package main

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

func TestProxyWatchRestoresOnlyItsOwnSettings(t *testing.T) {
	previous := proxySettings{Flags: 5, PAC: "https://example.test/proxy.pac", Bypass: "example.test"}
	active := proxySettings{Flags: 3, Server: "127.0.0.1:18790", Bypass: "<local>"}
	withCachedPAC := active
	withCachedPAC.PAC = previous.PAC
	for _, tt := range []struct {
		name    string
		current proxySettings
		restore bool
	}{
		{name: "restore original PAC after exit", current: active, restore: true},
		{name: "restore when Windows retains the disabled PAC URL", current: withCachedPAC, restore: true},
		{name: "keep settings edited after launch", current: previous},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// The production watcher's input reaches EOF after either a normal
			// shutdown or an unexpected exit of the launcher.
			var input, output bytes.Buffer
			if err := json.NewEncoder(&input).Encode(proxyRecovery{Before: previous, Active: active}); err != nil {
				t.Fatal(err)
			}
			writes := 0
			err := proxyWatch(&input, &output, func() (proxySettings, error) {
				if output.String() != "ready\n" {
					t.Fatal("expected the watcher to acknowledge the settings first")
				}
				return tt.current, nil
			}, func(settings proxySettings) error {
				writes++
				if settings != previous {
					t.Errorf("restore = %+v, want %+v", settings, previous)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if (writes == 1) != tt.restore {
				t.Fatalf("restored %d times, expected restore = %v", writes, tt.restore)
			}
		})
	}
}

func TestCanReadWindowsProxySettings(t *testing.T) {
	// The sentinel flags cannot match a Windows configuration, so this
	// exercises the real read without writing any system settings.
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(proxyRecovery{Active: proxySettings{Flags: ^uint32(0)}}); err != nil {
		t.Fatal(err)
	}
	if err := proxyWatch(&input, io.Discard, queryProxySettings, func(proxySettings) error {
		t.Fatal("read-only proxy check attempted to change Windows settings")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
