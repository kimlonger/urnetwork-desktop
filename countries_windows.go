package main

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

type exitCountry struct {
	ID        string `json:"location_id"`
	Type      string `json:"location_type"`
	Name      string `json:"country"`
	Code      string `json:"country_code"`
	Providers int    `json:"provider_count"`
}

func fetchCountries() ([]exitCountry, error) {
	var result struct {
		Locations []exitCountry `json:"locations"`
	}
	if err := newAuthorization().request(http.MethodGet, "/network/provider-locations", "", "获取出口国家", nil, &result); err != nil {
		return nil, err
	}
	countries := result.Locations[:0]
	seen := make(map[string]bool)
	for _, country := range result.Locations {
		country.Code = strings.ToUpper(country.Code)
		if country.Type != "country" || !validID(country.ID) || !validCountryCode(country.Code) || country.Providers < 1 || seen[country.Code] {
			continue
		}
		seen[country.Code] = true
		country.Name = countryName(country.Code, country.Name)
		countries = append(countries, country)
	}
	if len(countries) == 0 {
		return nil, errors.New("URnetwork 暂未返回有可用节点的国家，请稍后刷新")
	}
	sort.Slice(countries, func(i, j int) bool { return countries[i].Code < countries[j].Code })
	return countries, nil
}

func validCountryCode(code string) bool {
	code = strings.ToUpper(code)
	return len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z'
}

func countryName(code, fallback string) string {
	if !validCountryCode(code) {
		return fallback
	}
	region := syscall.StringToUTF16Ptr(strings.ToUpper(code))
	var buf [128]uint16
	const geoFriendlyName = 8
	n, _, _ := kernel32.NewProc("GetGeoInfoEx").Call(uintptr(unsafe.Pointer(region)), geoFriendlyName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return fallback
	}
	// GetGeoInfoEx uses the current user's display language.
	return syscall.UTF16ToString(buf[:])
}

func (c exitCountry) label() string {
	return fmt.Sprintf("%s  ·  %s  ·  %d 个节点", c.Name, c.Code, c.Providers)
}
