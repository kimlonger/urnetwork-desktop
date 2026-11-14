package main

import (
	"bytes"
	"fmt"
	"log"
	"net/netip"
	"slices"
	"strings"
	"unsafe"
)

const builtinPageSize = 150

var builtinGroups = []struct {
	group    builtinGroup
	name     string
	fallback action
}{
	{builtinAI, "海外智能服务", ur},
	{builtinDomains, "国内网站", direct},
	{builtinIPs, "国内地址网段", direct},
	{builtinLocal, "本机和局域网", direct},
	{builtinDefault, "其他连接", ur},
}

func (d *desktop) initBuiltins() {
	for _, g := range builtinGroups {
		rows := d.geo.builtinTargets(g.group)
		d.builtinLists[g.group] = rows
		addText(d.controls[idBuiltinGroup], cbAdd, fmt.Sprintf("%s（%d）", g.name, len(rows)))
	}
	send(d.controls[idBuiltinGroup], cbSelect, 0, 0)
	sendMessage.Call(d.controls[idBuiltinSearch], 0x1501, 1, uintptr(unsafe.Pointer(wide("搜索域名、地址或网段"))))
	send(d.controls[idBuiltinSearch], 0xc5, 512, 0)
}

func (d *desktop) selectedBuiltinGroup() (builtinGroup, action) {
	i := int(send(d.controls[idBuiltinGroup], cbGet, 0, 0))
	if i < 0 || i >= len(builtinGroups) {
		i = 0
	}
	g := builtinGroups[i]
	if g.group == builtinDefault {
		return g.group, d.rules.Default
	}
	return g.group, g.fallback
}

func builtinSearchMatch(group builtinGroup, target, query string, addr netip.Addr) bool {
	if strings.Contains(target, query) {
		return true
	}
	switch group {
	case builtinAI, builtinDomains:
		if base, suffix := strings.CutPrefix(target, "+."); suffix {
			return strings.HasSuffix(query, "."+base)
		}
	case builtinIPs:
		if addr.IsValid() {
			prefix, err := netip.ParsePrefix(target)
			return err == nil && prefix.Contains(addr.Unmap())
		}
	}
	return false
}

func (d *desktop) searchBuiltins() {
	group, _ := d.selectedBuiltinGroup()
	query := normalizeHost(getText(d.controls[idBuiltinSearch]))
	if domain, err := websiteTarget(query); (group == builtinDomains || group == builtinAI) && err == nil {
		query = domain
	}
	addr, _ := netip.ParseAddr(query)
	d.builtinMatches = d.builtinMatches[:0]
	for _, target := range d.builtinLists[group] {
		if builtinSearchMatch(group, target, query, addr) {
			d.builtinMatches = append(d.builtinMatches, target)
		}
	}
	d.builtinPage = 0
	d.fillBuiltins(-1)
}

func (d *desktop) fillBuiltins(selection int) {
	group, fallback := d.selectedBuiltinGroup()
	count := len(d.builtinMatches)
	pages := max(1, (count+builtinPageSize-1)/builtinPageSize)
	d.builtinPage = max(0, min(d.builtinPage, pages-1))
	start := d.builtinPage * builtinPageSize
	list := d.controls[idBuiltinList]
	send(list, 0x000b, 0, 0) // WM_SETREDRAW
	send(list, lbReset, 0, 0)
	for _, target := range d.builtinMatches[start:min(count, start+builtinPageSize)] {
		label := target
		if group == builtinDefault {
			label = "以上均未匹配"
		} else if group == builtinLocal {
			label = "本机和局域网地址"
		} else if host, suffix := strings.CutPrefix(target, "+."); suffix {
			label = host + "（含子域名）"
		}
		action := d.builtinEdits.actionFor(group, target, fallback)
		addText(list, lbAdd, label+"     →     "+actionLabel(action))
	}
	send(list, lbSelect, uintptr(selection), 0)
	send(list, 0x000b, 1, 0)
	invalidateRect.Call(list, 0, 1)
	setText(d.controls[idBuiltinCount], fmt.Sprintf("共 %d 条  ·  第 %d / %d 页", count, d.builtinPage+1, pages))
	enable(d.controls[idBuiltinPrev], d.builtinPage > 0)
	enable(d.controls[idBuiltinNext], d.builtinPage+1 < pages)
	d.pickBuiltin()
}

func (d *desktop) selectedBuiltin() (string, int) {
	index := int(send(d.controls[idBuiltinList], lbGet, 0, 0))
	i := d.builtinPage*builtinPageSize + index
	if index < 0 || i >= len(d.builtinMatches) {
		return "", -1
	}
	return d.builtinMatches[i], index
}

func (d *desktop) pickBuiltin() {
	group, fallback := d.selectedBuiltinGroup()
	target, _ := d.selectedBuiltin()
	combo := d.controls[idBuiltinAction]
	send(combo, cbReset, 0, 0)
	addText(combo, cbAdd, "恢复默认（"+actionLabel(fallback)+"）")
	for _, a := range ruleActions {
		addText(combo, cbAdd, actionLabel(a))
	}
	if group != builtinDefault {
		addText(combo, cbAdd, "停用此规则")
	}
	i := 0
	if a, ok := d.builtinEdits[group][target]; ok {
		i = slices.Index(ruleActions, a) + 1
		if a == disabledRule {
			i = len(ruleActions) + 1
		}
	}
	send(combo, cbSelect, uintptr(i), 0)
	enable(combo, target != "")
	enable(d.controls[idBuiltinSave], target != "")
	text := "请选择一条规则。"
	if target != "" {
		text = "停用后将继续匹配后续规则。"
		if group == builtinDefault {
			text = "所有其他规则均未匹配时使用此方式。"
		}
	}
	setText(d.controls[idBuiltinStatus], text)
}

func (d *desktop) saveBuiltin() {
	group, _ := d.selectedBuiltinGroup()
	target, selected := d.selectedBuiltin()
	index := int(send(d.controls[idBuiltinAction], cbGet, 0, 0))
	if target == "" || index < 0 || index > len(ruleActions)+1 || (group == builtinDefault && index == len(ruleActions)+1) {
		return
	}
	settings, snapshot, err := readBuiltinSettings(d.builtinPath)
	if err == nil {
		if index == 0 {
			delete(settings[group], target)
		} else {
			if settings[group] == nil {
				settings[group] = make(map[string]action)
			}
			value := disabledRule
			if index <= len(ruleActions) {
				value = ruleActions[index-1]
			}
			settings[group][target] = value
		}
		var newest []byte
		_, newest, err = readBuiltinSettings(d.builtinPath)
		if err == nil && !bytes.Equal(snapshot, newest) {
			setText(d.controls[idBuiltinStatus], "修改记录已在其他窗口更改，请重试。")
			return
		}
	}
	if err == nil {
		err = writeJSONFile(d.builtinPath, settings)
	}
	if err != nil {
		log.Print(err)
		setText(d.controls[idBuiltinStatus], "保存失败，请查看运行日志。")
		return
	}
	d.builtinEdits = settings
	d.fillBuiltins(selected)
	setText(d.controls[idBuiltinStatus], "已保存，运行中的代理会自动应用。")
}
