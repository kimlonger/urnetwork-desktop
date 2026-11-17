package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"
)

type speedPreference struct {
	Enabled *bool `json:"enabled"`
}

type sdkPerformanceProfile struct {
	WindowType string `json:"window_type"`
	WindowSize *struct {
		Min int `json:"window_size_min"`
		Max int `json:"window_size_max"`
	} `json:"window_size"`
	AllowDirect bool `json:"allow_direct"`
	PostQuantum bool `json:"post_quantum_encryption"`
}

func readSmallJSON(path string, out any) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	return err == nil && len(data) <= 4096 && json.Unmarshal(data, out) == nil
}

// Explicit proxy preferences win. On first use only, offer the same speed and
// fixed-exit mode already selected in the official desktop app. This does not
// copy credentials or the desktop app's privacy settings.
func initialSpeedPreference(root, localAppData string) bool {
	path := filepath.Join(root, "desktop-performance.json")
	var saved speedPreference
	if _, err := os.Stat(path); err == nil {
		return readSmallJSON(path, &saved) && saved.Enabled != nil && *saved.Enabled
	} else if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if localAppData == "" {
		return false
	}
	path = filepath.Join(localAppData, "URnetwork", "app", "storage", "network_spaces", "bringyour.com", "main", ".by", ".performance_profile")
	var profile sdkPerformanceProfile
	if !readSmallJSON(path, &profile) || profile.WindowSize == nil {
		return false
	}
	return profile.WindowType == "speed" && profile.WindowSize.Min == 1 && profile.WindowSize.Max == 1
}

func saveSpeedPreference(root string, enabled bool) error {
	return writeJSONFile(filepath.Join(root, "desktop-performance.json"), speedPreference{Enabled: &enabled})
}

// Keep anonymization enabled. Speed mode selects a single speed-oriented exit,
// and never enables direct peer connections or traffic sharing.
func (s *sdk) useSpeedProfile() error {
	data := cString(`{"window_type":"speed","window_size":{"window_size_min":1,"window_size_max":1},"allow_direct":false,"post_quantum_encryption":false}`)
	s.call("device_set_performance_profile", s.device, uintptr(unsafe.Pointer(&data[0])))
	runtime.KeepAlive(data)
	ptr := s.call("device_get_performance_profile", s.device)
	value := copyCString(ptr)
	if ptr != 0 {
		s.call("free_string", ptr)
	}
	var profile sdkPerformanceProfile
	if json.Unmarshal([]byte(value), &profile) != nil || profile.WindowType != "speed" || profile.WindowSize == nil || profile.WindowSize.Min != 1 || profile.WindowSize.Max != 1 || profile.AllowDirect || profile.PostQuantum {
		return errors.New("无法确认速度优先设置，已停止启动")
	}
	return nil
}

func (s *sdk) useAutoProfile() error {
	s.call("device_set_performance_profile", s.device, 0)
	ptr := s.call("device_get_performance_profile", s.device)
	if ptr != 0 {
		s.call("free_string", ptr)
		return errors.New("无法确认自动模式，已停止启动")
	}
	return nil
}

func shouldFallbackFromSpeed(enabled bool, status sdkConnectionStatus) bool {
	return enabled && status.State == "CONNECT_FAILED" && !status.connected()
}
