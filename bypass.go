package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

type bypassEntries struct {
	Websites []string `json:"websites"`
	IPs      []string `json:"ips"`
}

func websiteTarget(text string) (string, error) {
	text = strings.TrimSpace(text)
	if !strings.Contains(text, "://") {
		text = "https://" + text
	}
	site, err := url.Parse(text)
	if err != nil || site.User != nil || (site.Scheme != "http" && site.Scheme != "https") {
		return "", errors.New("请输入网站域名或 HTTP(S) 网址")
	}
	host := normalizeHost(site.Hostname())
	if _, err := netip.ParseAddr(host); err == nil || !validHost(host) || !strings.Contains(host, ".") {
		return "", errors.New("请输入有效的网站域名，例如 example.com")
	}
	return host, nil
}

func ipTarget(text string) (string, error) {
	text = strings.TrimSpace(text)
	if addr, err := netip.ParseAddr(text); err == nil && addr.Zone() == "" {
		return addr.Unmap().String(), nil
	}
	if prefix, err := netip.ParsePrefix(text); err == nil && !prefix.Addr().Is4In6() {
		return prefix.Masked().String(), nil
	}
	return "", errors.New("请输入 IPv4、IPv6 或 CIDR 网段，例如 192.0.2.0/24")
}

func readBypass(path string) (bypassEntries, []byte, error) {
	var entries bypassEntries
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil, nil
	}
	if err != nil {
		return entries, nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil || len(data) > 256<<10 || json.Unmarshal(data, &entries) != nil {
		return entries, nil, errors.New("无法读取网站和 IP 直连规则")
	}
	for _, host := range entries.Websites {
		if parsed, err := websiteTarget(host); err != nil || parsed != host {
			return entries, nil, fmt.Errorf("无效的网站直连规则 %q", host)
		}
	}
	for _, ip := range entries.IPs {
		if parsed, err := ipTarget(ip); err != nil || parsed != ip {
			return entries, nil, fmt.Errorf("无效的 IP 直连规则 %q", ip)
		}
	}
	return entries, data, nil
}

func (entries bypassEntries) applyTo(p *policy) error {
	rows := make([]ruleConfig, 0, len(entries.Websites)+len(entries.IPs))
	for _, host := range entries.Websites {
		rows = append(rows, ruleConfig{Target: "+." + host, Action: direct})
	}
	for _, ip := range entries.IPs {
		rows = append(rows, ruleConfig{Target: ip, Action: direct})
	}
	var err error
	p.bypass, err = compileRules(rows)
	return err
}

func readPolicyWithBypass(rulesPath, bypassPath string, geo *policy) (*policy, []byte, error) {
	p, rules, err := readPolicy(rulesPath, geo)
	if err != nil {
		return nil, nil, err
	}
	entries, data, err := readBypass(bypassPath)
	if err != nil {
		return nil, nil, err
	}
	if err := entries.applyTo(p); err != nil {
		return nil, nil, err
	}
	return p, append(append(rules, 0), data...), nil
}

func readDesktopPolicy(rulesPath, bypassPath, builtinPath string, geo *policy) (*policy, []byte, error) {
	p, snapshot, err := readPolicyWithBypass(rulesPath, bypassPath, geo)
	if err != nil {
		return nil, nil, err
	}
	settings, data, err := readBuiltinSettings(builtinPath)
	if err != nil {
		return nil, nil, err
	}
	p.builtins = settings
	return p, append(append(snapshot, 0), data...), nil
}
