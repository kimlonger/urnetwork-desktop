package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"slices"
	"strings"
)

type builtinGroup string

const (
	builtinAI      builtinGroup = "ai"
	builtinDomains builtinGroup = "domestic-domains"
	builtinIPs     builtinGroup = "domestic-ips"
	builtinLocal   builtinGroup = "local"
	builtinDefault builtinGroup = "default"
	disabledRule   action       = "disabled"
)

var aiDomains = []string{"chatgpt.com", "openai.com", "oaistatic.com", "oaiusercontent.com", "google.com", "gstatic.com", "googleapis.com", "claude.ai", "anthropic.com"}

type builtinSettings map[builtinGroup]map[string]action

func (settings builtinSettings) actionFor(group builtinGroup, target string, fallback action) action {
	if value, ok := settings[group][target]; ok {
		return value
	}
	return fallback
}

func readBuiltinSettings(path string) (builtinSettings, []byte, error) {
	settings := make(builtinSettings)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	const maxSize = 16 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil || len(data) > maxSize || json.Unmarshal(data, &settings) != nil {
		return nil, nil, errors.New("无法读取内置规则的修改记录")
	}
	if settings == nil {
		settings = make(builtinSettings)
	}
	for group, rows := range settings {
		switch group {
		case builtinAI, builtinDomains, builtinIPs, builtinLocal, builtinDefault:
		default:
			return nil, nil, fmt.Errorf("未知的内置规则分类 %q", group)
		}
		for target, choice := range rows {
			if !validAction(choice) && !(choice == disabledRule && group != builtinDefault) {
				return nil, nil, fmt.Errorf("内置规则 %q 的处理方式无效", target)
			}
		}
	}
	return settings, data, nil
}

func (p *policy) builtinTargets(group builtinGroup) []string {
	var list []string
	switch group {
	case builtinAI:
		for _, host := range aiDomains {
			list = append(list, "+."+host)
		}
	case builtinDomains:
		list = make([]string, 0, len(p.domains.exact)+len(p.domains.suffix))
		for host := range p.domains.exact {
			list = append(list, host)
		}
		for host := range p.domains.suffix {
			list = append(list, "+."+host)
		}
	case builtinIPs:
		list = make([]string, 0, len(p.ipv4)+len(p.ipv6))
		for _, prefixes := range []map[netip.Prefix]struct{}{p.ipv4, p.ipv6} {
			for prefix := range prefixes {
				list = append(list, prefix.String())
			}
		}
	case builtinLocal, builtinDefault:
		list = []string{"*"}
	}
	slices.Sort(list)
	return list
}

func (p *policy) domesticDomain(host string) (action, string) {
	if _, ok := p.domains.exact[host]; ok {
		if a := p.builtins.actionFor(builtinDomains, host, direct); a != disabledRule {
			return a, host
		}
	}
	for {
		if _, ok := p.domains.suffix[host]; ok {
			if a := p.builtins.actionFor(builtinDomains, "+."+host, direct); a != disabledRule {
				return a, "+." + host
			}
		}
		_, tail, ok := strings.Cut(host, ".")
		if !ok {
			return "", ""
		}
		host = tail
	}
}
