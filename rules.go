package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

type action string

const (
	direct action = "direct"
	ur     action = "ur"
	block  action = "block"
	auto   action = "auto"
)

type ruleConfig struct {
	Target string `json:"target"`
	Action action `json:"action"`
}

type profileConfig struct {
	Port  int          `json:"port"`
	Mode  action       `json:"mode"`
	Rules []ruleConfig `json:"rules"`
}

type config struct {
	Default  action                   `json:"default"`
	Rules    []ruleConfig             `json:"rules"`
	Profiles map[string]profileConfig `json:"profiles"`
}

type rule struct {
	target string
	prefix netip.Prefix
	suffix bool
	action action
}

type profile struct {
	port  int
	mode  action
	rules []rule
}

type policy struct {
	fallback action
	builtins builtinSettings
	bypass   []rule
	rules    []rule
	profiles map[string]profile
	domains  domainSet
	ipv4     map[netip.Prefix]struct{}
	ipv6     map[netip.Prefix]struct{}
}

type domainSet struct {
	exact  map[string]struct{}
	suffix map[string]struct{}
}

func normalizeHost(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
}

func validHost(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func validAction(a action) bool { return a == direct || a == ur || a == block }

func compileRules(src []ruleConfig) ([]rule, error) {
	var out []rule
	for i, cfg := range src {
		if !validAction(cfg.Action) {
			return nil, fmt.Errorf("rule %d: invalid action %q", i+1, cfg.Action)
		}
		target := normalizeHost(cfg.Target)
		item := rule{target: target, action: cfg.Action}
		if addr, err := netip.ParseAddr(target); err == nil && addr.Zone() == "" {
			addr = addr.Unmap()
			item.prefix = netip.PrefixFrom(addr, addr.BitLen())
		} else if prefix, err := netip.ParsePrefix(target); err == nil && !prefix.Addr().Is4In6() {
			item.prefix = prefix.Masked()
		} else {
			item.target, item.suffix = strings.CutPrefix(target, "+.")
			if !validHost(item.target) {
				return nil, fmt.Errorf("rule %d: invalid target %q", i+1, cfg.Target)
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func readPolicy(path string, geo *policy) (*policy, []byte, error) {
	c, data, err := readRulesConfig(path)
	if err != nil {
		return nil, data, err
	}
	p, err := c.compile(geo)
	return p, data, err
}

func readRulesConfig(path string) (config, []byte, error) {
	var c config
	f, err := os.Open(path)
	if err != nil {
		return c, nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return c, nil, fmt.Errorf("cannot read rules file (maximum 1 MiB)")
	}
	d := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, data, err
	}
	if d.Decode(new(any)) != io.EOF {
		return c, data, fmt.Errorf("unexpected data after rules document")
	}
	return c, data, nil
}

func (c config) compile(geo *policy) (*policy, error) {
	if !validAction(c.Default) || len(c.Profiles) == 0 {
		return nil, fmt.Errorf("default action and at least one profile are required")
	}
	p := *geo
	p.fallback = c.Default
	rules, err := compileRules(c.Rules)
	if err != nil {
		return nil, err
	}
	p.rules = rules
	p.profiles = make(map[string]profile, len(c.Profiles))
	ports := make(map[int]bool)
	for name, v := range c.Profiles {
		if name == "" || v.Port < 1024 || v.Port > 65535 || ports[v.Port] || (v.Mode != auto && !validAction(v.Mode)) {
			return nil, fmt.Errorf("invalid profile name, port or mode: %q", name)
		}
		items, err := compileRules(v.Rules)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
		ports[v.Port] = true
		p.profiles[name] = profile{v.Port, v.Mode, items}
	}
	return &p, nil
}

func sameListeners(a, b *policy) bool {
	if len(a.profiles) != len(b.profiles) {
		return false
	}
	for name, v := range a.profiles {
		if b.profiles[name].port != v.port {
			return false
		}
	}
	return true
}

type activePolicy struct {
	current        atomic.Pointer[policy]
	mu             sync.Mutex
	tunnels        map[*activeTunnel]struct{}
	exitGeneration atomic.Uint64
}

type activeTunnel struct {
	profile        string
	host           string
	route          action
	exitGeneration uint64
	close          func()
}

func (p *activePolicy) track(t *activeTunnel) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if route, _ := p.current.Load().choose(t.profile, t.host); route != t.route {
		return false
	}
	if t.route == ur && t.exitGeneration != p.exitGeneration.Load() {
		return false
	}
	if p.tunnels == nil {
		p.tunnels = make(map[*activeTunnel]struct{})
	}
	p.tunnels[t] = struct{}{}
	return true
}

func (p *activePolicy) untrack(t *activeTunnel) {
	p.mu.Lock()
	delete(p.tunnels, t)
	p.mu.Unlock()
}

func (p *activePolicy) apply(next *policy) {
	p.mu.Lock()
	p.current.Store(next)
	for t := range p.tunnels {
		if route, _ := next.choose(t.profile, t.host); route != t.route {
			delete(p.tunnels, t)
			go t.close()
		}
	}
	p.mu.Unlock()
}

func (p *activePolicy) closeTunnels(route action) {
	p.mu.Lock()
	var closing []*activeTunnel
	for t := range p.tunnels {
		if route == "" || t.route == route {
			delete(p.tunnels, t)
			closing = append(closing, t)
		}
	}
	p.mu.Unlock()
	for _, t := range closing {
		t.close()
	}
}

func (p *policy) choose(name, host string) (action, string) {
	route, reason, _ := p.match(name, host)
	return route, reason
}

func (p *policy) match(name, host string) (action, string, string) {
	profile := p.profiles[name]
	addr, _ := netip.ParseAddr(host)
	addr = addr.Unmap()
	for _, r := range p.bypass {
		if r.matches(host, addr) {
			return direct, "direct exception", r.pattern()
		}
	}
	for _, list := range [][]rule{profile.rules, p.rules} {
		for _, r := range list {
			if r.matches(host, addr) {
				if r.prefix.IsValid() {
					return r.action, "custom IP rule", r.pattern()
				}
				return r.action, "custom domain rule", r.pattern()
			}
		}
	}
	if profile.mode != auto {
		return profile.mode, "profile mode", ""
	}
	if addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		if a := p.builtins.actionFor(builtinLocal, "*", direct); a != disabledRule {
			return a, "local network", ""
		}
	}
	for _, aiDomain := range aiDomains {
		if host == aiDomain || strings.HasSuffix(host, "."+aiDomain) {
			if a := p.builtins.actionFor(builtinAI, "+."+aiDomain, ur); a != disabledRule {
				return a, "overseas AI service", "+." + aiDomain
			}
		}
	}
	if a, target := p.domesticDomain(host); target != "" {
		return a, "CN domain list", target
	}
	if addr.IsValid() {
		set := p.ipv6
		if addr.Is4() {
			set = p.ipv4
		}
		for bits := addr.BitLen(); bits >= 0; bits-- {
			prefix := netip.PrefixFrom(addr, bits).Masked()
			if _, ok := set[prefix]; ok {
				if a := p.builtins.actionFor(builtinIPs, prefix.String(), direct); a != disabledRule {
					return a, "CN IP list", prefix.String()
				}
			}
		}
	}
	return p.builtins.actionFor(builtinDefault, "*", p.fallback), "default", ""
}

func (r rule) pattern() string {
	if r.prefix.IsValid() {
		return r.prefix.String()
	}
	if r.suffix {
		return "+." + r.target
	}
	return r.target
}

func (r rule) matches(host string, addr netip.Addr) bool {
	if r.prefix.IsValid() {
		return r.prefix.Contains(addr)
	}
	return host == r.target || (r.suffix && strings.HasSuffix(host, "."+r.target))
}

func loadGeo(dir string) (*policy, error) {
	p := &policy{domains: domainSet{make(map[string]struct{}), make(map[string]struct{})}, ipv4: make(map[netip.Prefix]struct{}), ipv6: make(map[netip.Prefix]struct{})}
	if err := loadList(filepath.Join(dir, "cn-domain.list"), func(s string) bool {
		s, suffix := strings.CutPrefix(normalizeHost(s), "+.")
		if !validHost(s) {
			return false
		}
		if suffix {
			p.domains.suffix[s] = struct{}{}
		} else {
			p.domains.exact[s] = struct{}{}
		}
		return true
	}); err != nil {
		return nil, err
	}
	if err := loadList(filepath.Join(dir, "cn-ip.list"), func(s string) bool {
		prefix, err := netip.ParsePrefix(s)
		if err != nil {
			return false
		}
		if prefix.Addr().Is4() {
			p.ipv4[prefix.Masked()] = struct{}{}
		} else {
			p.ipv6[prefix.Masked()] = struct{}{}
		}
		return true
	}); err != nil {
		return nil, err
	}
	return p, nil
}

func loadList(path string, add func(string) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 32<<20 {
		return fmt.Errorf("cannot read rule list %s (maximum 32 MiB)", filepath.Base(path))
	}
	s := bufio.NewScanner(f)
	count := 0
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "//") {
			if !add(line) {
				return fmt.Errorf("unsupported entry in %s at line %d", filepath.Base(path), count+1)
			}
			count++
		}
	}
	if count == 0 {
		return fmt.Errorf("empty rule list: %s", filepath.Base(path))
	}
	return s.Err()
}
