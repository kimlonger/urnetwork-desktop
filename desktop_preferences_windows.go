package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(append(data, '\n'))
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readExitPreference(root string) exitCountry {
	data, err := os.ReadFile(filepath.Join(root, "desktop-exit.json"))
	var country exitCountry
	if err == nil && len(data) < 4096 && json.Unmarshal(data, &country) == nil && validID(country.ID) && validCountryCode(country.Code) {
		return country
	}
	return exitCountry{}
}

func displayExit(c exitCountry) string {
	if c.ID == "" {
		return "自动选择"
	}
	return fmt.Sprintf("%s（%s）", countryName(c.Code, c.Name), c.Code)
}

func (d *desktop) refreshCountries() {
	d.refreshing = true
	setText(d.controls[idCountryStatus], "正在获取可用的国家和地区…")
	d.updateButtons()
	go func() {
		countries, err := fetchCountries()
		d.post(desktopEvent{workerEvent: workerEvent{Kind: "countries"}, countries: countries, err: err})
	}()
}

func (d *desktop) fillCountries() {
	combo := d.controls[idCountry]
	send(combo, cbReset, 0, 0)
	addText(combo, cbAdd, "自动选择")
	selected := 0
	for i, country := range d.countries {
		addText(combo, cbAdd, country.label())
		if country.ID == d.wanted.ID {
			selected = i + 1
		}
	}
	if selected == 0 && d.wanted.ID != "" {
		addText(combo, cbAdd, displayExit(d.wanted)+" · 已保存")
		selected = len(d.countries) + 1
	}
	send(combo, cbSelect, uintptr(selected), 0)
}

func (d *desktop) applyCountry() {
	i := int(send(d.controls[idCountry], cbGet, 0, 0))
	if i < 0 {
		return
	}
	var choice exitCountry
	if i > 0 && i <= len(d.countries) {
		choice = d.countries[i-1]
	} else if i > 0 {
		choice = d.wanted
	}
	if err := writeJSONFile(filepath.Join(d.root, "desktop-exit.json"), choice); err != nil {
		messageBox("保存出口设置："+err.Error(), 0x10)
		return
	}
	d.wanted = choice
	d.showChosenExit()
	if d.worker == nil {
		setText(d.controls[idCountryStatus], "已保存 "+displayExit(choice)+"，下次启动代理时生效。")
	} else if d.send("location", choice.ID) {
		d.switching = true
		setText(d.controls[idCountryStatus], "正在切换到 "+displayExit(choice)+"…")
	}
	d.updateButtons()
}

func actionLabel(a action) string {
	switch a {
	case direct:
		return "直接连接"
	case ur:
		return "走代理"
	case block:
		return "阻止连接"
	case disabledRule:
		return "停用此规则"
	default:
		return string(a)
	}
}

func reasonLabel(reason string) string {
	switch reason {
	case "direct exception":
		return "优先直连规则"
	case "custom IP rule", "custom domain rule":
		return "自定义规则"
	case "CN domain list":
		return "国内域名规则"
	case "CN IP list":
		return "国内 IP 规则"
	case "local network":
		return "本地网络"
	case "overseas AI service":
		return "海外智能服务"
	case "profile mode":
		return "端口分流模式"
	default:
		return "默认路由"
	}
}

func (d *desktop) initRules() {
	d.scopes = []string{""}
	for profile := range d.rules.Profiles {
		d.scopes = append(d.scopes, profile)
	}
	sort.Strings(d.scopes)
	combo := d.controls[idRuleScope]
	send(combo, cbReset, 0, 0)
	addText(combo, cbAdd, "全局规则")
	for _, profile := range d.scopes[1:] {
		p := d.rules.Profiles[profile]
		addText(combo, cbAdd, fmt.Sprintf("%s · %d", profileLabel(profile, p.Mode), p.Port))
	}
	send(combo, cbSelect, 0, 0)
	d.loadRuleList(-1)
}

func (d *desktop) ruleScope() string {
	i := int(send(d.controls[idRuleScope], cbGet, 0, 0))
	if i < 0 || i >= len(d.scopes) {
		return ""
	}
	return d.scopes[i]
}

func (d *desktop) ruleRows() []ruleConfig {
	scope := d.ruleScope()
	if scope == "" {
		return d.rules.Rules
	}
	return d.rules.Profiles[scope].Rules
}

func (d *desktop) loadRuleList(index int) {
	hwnd := d.controls[idRuleList]
	send(hwnd, lbReset, 0, 0)
	for _, row := range d.ruleRows() {
		addText(hwnd, lbAdd, fmt.Sprintf("%s     →     %s", row.Target, actionLabel(row.Action)))
	}
	send(hwnd, lbSelect, uintptr(index), 0)
	d.pickRule()
}

func (d *desktop) pickRule() {
	i := int(send(d.controls[idRuleList], lbGet, 0, 0))
	rows := d.ruleRows()
	if i < 0 || i >= len(rows) {
		d.ruleSelection = -1
		setText(d.controls[idRuleTarget], "")
		send(d.controls[idAction], cbSelect, 1, 0)
	} else {
		d.ruleSelection = i
		setText(d.controls[idRuleTarget], rows[i].Target)
		send(d.controls[idAction], cbSelect, uintptr(slices.Index(ruleActions, rows[i].Action)), 0)
	}
	enable(d.controls[idDelete], d.ruleSelection >= 0)
	enable(d.controls[idUp], d.ruleSelection > 0)
	enable(d.controls[idDown], d.ruleSelection >= 0 && d.ruleSelection < len(rows)-1)
}

func (d *desktop) changeRules(op int) {
	scope := d.ruleScope()
	current, snapshot, err := readRulesConfig(d.rulesPath)
	if err != nil {
		setText(d.controls[idRuleStatus], "无法读取现有分流规则，请查看日志。")
		log.Print(err)
		return
	}
	if !sameConfig(current, d.rules) {
		if _, err := current.compile(d.geo); err == nil {
			d.rules = current
			d.initRules()
		}
		setText(d.controls[idRuleStatus], "规则文件已在外部修改，请检查当前列表后重试。")
		return
	}
	rows := slices.Clone(d.ruleRows())
	i := d.ruleSelection
	switch op {
	case idSave:
		target := normalizeHost(getText(d.controls[idRuleTarget]))
		index := int(send(d.controls[idAction], cbGet, 0, 0))
		if target == "" || index < 0 || index >= len(ruleActions) {
			setText(d.controls[idRuleStatus], "请填写域名、地址或网段，并选择连接方式。")
			return
		}
		for j, row := range rows {
			if j != i && normalizeHost(row.Target) == target {
				if i < 0 {
					i = j
					break
				}
				setText(d.controls[idRuleStatus], "该目标已经有一条规则，请先选中那条规则修改。")
				return
			}
		}
		row := ruleConfig{Target: target, Action: ruleActions[index]}
		if _, err := compileRules([]ruleConfig{row}); err != nil {
			setText(d.controls[idRuleStatus], "目标格式无效。示例：+.example.com、192.0.2.1、192.0.2.0/24。")
			return
		}
		if i < 0 {
			rows = append(rows, row)
		} else {
			rows[i] = row
		}
		i = -1
	case idDelete:
		if i < 0 || i >= len(rows) {
			return
		}
		rows = slices.Delete(rows, i, i+1)
		i = min(i, len(rows)-1)
	case idUp, idDown:
		next := i + 1
		if op == idUp {
			next = i - 1
		}
		if i < 0 || i >= len(rows) || next < 0 || next >= len(rows) {
			return
		}
		rows[i], rows[next] = rows[next], rows[i]
		i = next
	}
	if scope == "" {
		current.Rules = rows
	} else {
		p := current.Profiles[scope]
		p.Rules = rows
		current.Profiles[scope] = p
	}
	_, newest, err := readRulesConfig(d.rulesPath)
	if err != nil || !bytes.Equal(snapshot, newest) {
		setText(d.controls[idRuleStatus], "规则文件正在被其他程序修改，请重试。")
		return
	}
	if err := writeJSONFile(d.rulesPath, current); err != nil {
		setText(d.controls[idRuleStatus], "保存规则失败，请查看日志。")
		log.Print(err)
		return
	}
	d.rules = current
	d.loadRuleList(i)
	setText(d.controls[idRuleStatus], "规则已保存，运行中的代理会自动重新加载。")
}

func sameConfig(a, b config) bool {
	if a.Default != b.Default || !slices.Equal(a.Rules, b.Rules) || len(a.Profiles) != len(b.Profiles) {
		return false
	}
	for key, x := range a.Profiles {
		y, ok := b.Profiles[key]
		if !ok || x.Port != y.Port || x.Mode != y.Mode || !slices.Equal(x.Rules, y.Rules) {
			return false
		}
	}
	return true
}

func (d *desktop) testRule() {
	d.matchedRuleKey, d.matchedReason = "", ""
	enable(d.controls[idOpenMatchedRule], false)
	host, _, err := destination(strings.TrimSpace(getText(d.controls[idTestInput])), "443")
	if err != nil {
		setText(d.controls[idRuleStatus], "请输入有效的域名或 IP。")
		return
	}
	p, err := d.rules.compile(d.geo)
	if err == nil {
		err = d.bypass.applyTo(p)
		p.builtins = d.builtinEdits
	}
	if err != nil {
		setText(d.controls[idRuleStatus], "分流规则无效，请查看日志。")
		log.Print(err)
		return
	}
	name := d.ruleScope()
	if name == "" {
		name = "auto"
	}
	route, reason, target := p.match(name, host)
	d.matchedRuleKey, d.matchedReason = target, reason
	if base, suffix := strings.CutPrefix(target, "+."); suffix {
		target = base + " 及子域名"
	}
	if target != "" {
		setText(d.controls[idRuleStatus], fmt.Sprintf("%s  →  %s  →  %s", host, target, actionLabel(route)))
	} else {
		setText(d.controls[idRuleStatus], fmt.Sprintf("%s  →  %s（%s）", host, actionLabel(route), reasonLabel(reason)))
	}
	enable(d.controls[idOpenMatchedRule], matchedBuiltinGroup(reason) != "" && d.matchedRuleKey != "")
}

func matchedBuiltinGroup(reason string) builtinGroup {
	switch reason {
	case "overseas AI service":
		return builtinAI
	case "CN domain list":
		return builtinDomains
	case "CN IP list":
		return builtinIPs
	default:
		return ""
	}
}

func (d *desktop) openMatchedRule() {
	group := matchedBuiltinGroup(d.matchedReason)
	target := d.matchedRuleKey
	for i, option := range builtinGroups {
		if option.group != group {
			continue
		}
		d.advanced, d.showBuiltins = false, true
		d.selectPage(pageRules)
		send(d.controls[idBuiltinGroup], cbSelect, uintptr(i), 0)
		setText(d.controls[idBuiltinSearch], strings.TrimPrefix(target, "+."))
		d.searchBuiltins()
		if index := slices.Index(d.builtinMatches, target); index >= 0 {
			d.builtinPage = index / builtinPageSize
			d.fillBuiltins(index % builtinPageSize)
		}
		return
	}
}
