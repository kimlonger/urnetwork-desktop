package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unsafe"
)

const (
	idTitle = iota + 100
	idState
	idConnect
	idSystem
	idBrowser
	idCountryLabel
	idCountry
	idRefresh
	idCountryApply
	idCheckExit
	idCountryStatus
	idRulesTab
	idLogsTab
	idHint
	idRuleList
	idRuleTarget
	idAction
	idSave
	idDelete
	idUp
	idDown
	idTestInput
	idTest
	idRuleStatus
	idLogFolder
	idLogText
	idRuleScope
	idNewRule
	idHomeTab
	idPageTitle
	idPageSubtitle
	idExitIP
	idExitCaption
	idDownloadLabel
	idDownload
	idUploadLabel
	idUpload
	idTrafficLabel
	idTrafficTotal
	idAdvanced
	idWebLabel
	idWebInput
	idWebAdd
	idWebList
	idWebRemove
	idIPLabel
	idIPInput
	idIPAdd
	idIPList
	idIPRemove
	idDirectStatus
	idWebEmpty
	idIPEmpty
	idBuiltins
	idBuiltinGroup
	idBuiltinSearch
	idBuiltinFind
	idBuiltinList
	idBuiltinPrev
	idBuiltinNext
	idBuiltinCount
	idBuiltinAction
	idBuiltinSave
	idBuiltinStatus
	idPortsTab
	idPortsSave
	idPortsReset
	idPortsStatus
	idPortAutoLabel
	idPortAutoHelp
	idPortAuto
	idPortDirectLabel
	idPortDirectHelp
	idPortDirect
	idPortProxyLabel
	idPortProxyHelp
	idPortProxy
	idChosenExit
	idReconnect
	idOpenMatchedRule
	idSpeedMode
)

const (
	pageHome = iota
	pageRules
	pageLogs
	pagePorts
)

const (
	cbAdd    = 0x143
	cbGet    = 0x147
	cbSelect = 0x14e
	cbReset  = 0x14b
	lbAdd    = 0x180
	lbGet    = 0x188
	lbSelect = 0x186
	lbReset  = 0x184
	bmGet    = 0xf0
	bmSet    = 0xf1
)

var ruleActions = []action{direct, ur, block}

type desktop struct {
	window, instance, font, titleFont, monoFont, brandFont, valueFont uintptr
	dpi                                                               int
	controls                                                          map[int]uintptr
	controlIDs                                                        map[uintptr]int
	root, logs, logPath, rulesPath                                    string
	bypassPath                                                        string
	geo                                                               *policy
	rules                                                             config
	bypass                                                            bypassEntries
	page                                                              int
	advanced                                                          bool
	showBuiltins                                                      bool
	builtinPath                                                       string
	builtinEdits                                                      builtinSettings
	builtinLists                                                      map[builtinGroup][]string
	builtinMatches                                                    []string
	builtinPage                                                       int
	clientWidth, clientHeight                                         int
	palette                                                           desktopPalette
	traffic                                                           trafficSample
	history                                                           [60]trafficSample
	exit                                                              exitObservation
	lastExitProbe                                                     time.Time
	exitVerifiedAt                                                    time.Time
	exitProbeFailed, exitCurrent                                      bool
	sdkStatus                                                         sdkConnectionStatus
	scopes                                                            []string
	ruleSelection                                                     int
	matchedRuleKey, matchedReason                                     string
	logSize                                                           int64
	countries                                                         []exitCountry
	wanted                                                            exitCountry
	refreshing, probing, switching                                    bool
	speedMode                                                         bool
	worker                                                            *proxyWorker
	ready, stopping, closing, systemBusy, systemOn                    bool
	errorShown                                                        bool
	inbox                                                             chan desktopEvent
}

type desktopEvent struct {
	workerEvent
	countries []exitCountry
	err       error
}

func runDesktop() error {
	root, err := appDirectory()
	if err != nil {
		return err
	}
	logs := filepath.Join(root, "logs")
	logPath, err := startLog(logs)
	if err != nil {
		return err
	}
	log.Printf("URnetwork 桌面版 v%s", appVersion)
	assets := filepath.Join(root, "runtime", appVersion)
	if err := prepareRuleFiles(assets); err != nil {
		return err
	}
	if err := extractFile(root, "rules.json", true); err != nil {
		return err
	}
	geo, err := loadGeo(assets)
	if err != nil {
		return err
	}
	rulesPath := filepath.Join(root, "rules.json")
	rules, _, err := readRulesConfig(rulesPath)
	if err != nil {
		return fmt.Errorf("读取分流规则：%w", err)
	}
	if _, err := rules.compile(geo); err != nil {
		return fmt.Errorf("检查分流规则：%w", err)
	}
	bypassPath := filepath.Join(root, "bypass.json")
	bypass, _, err := readBypass(bypassPath)
	if err != nil {
		return err
	}
	builtinPath := filepath.Join(root, "builtin-rules.json")
	builtinEdits, _, err := readBuiltinSettings(builtinPath)
	if err != nil {
		return err
	}
	d := &desktop{
		root: root, logs: logs, logPath: logPath, rulesPath: rulesPath,
		bypassPath: bypassPath, bypass: bypass,
		builtinPath: builtinPath, builtinEdits: builtinEdits, builtinLists: make(map[builtinGroup][]string),
		geo: geo, rules: rules, ruleSelection: -1, logSize: -1,
		controls: make(map[int]uintptr), controlIDs: make(map[uintptr]int), inbox: make(chan desktopEvent, 32),
		wanted:    readExitPreference(root),
		speedMode: initialSpeedPreference(root, os.Getenv("LOCALAPPDATA")),
	}
	return d.makeWindow()
}

func (d *desktop) scale(n int) int { return (n*d.dpi + 48) / 96 }

func (d *desktop) buildControls() error {
	const button = wsTab
	const field = wsTab | 0x80          // ES_AUTOHSCROLL
	const combo = wsTab | wsVScroll | 3 // CBS_DROPDOWNLIST
	controls := []struct {
		id           int
		kind, text   string
		style, extra uintptr
	}{
		{idTitle, "STATIC", "网络代理", 0, 0},
		{idState, "STATIC", "已停止", 0, 0},
		{idConnect, "BUTTON", "启动代理", button, 0},
		{idSystem, "BUTTON", "使用系统代理", button | 3, 0},
		{idBrowser, "BUTTON", "打开独立浏览器", button, 0},
		{idCountryLabel, "STATIC", "出口国家", 0, 0},
		{idCountry, "COMBOBOX", "", combo, 0},
		{idRefresh, "BUTTON", "刷新国家", button, 0},
		{idCountryApply, "BUTTON", "应用出口", button, 0},
		{idCheckExit, "BUTTON", "刷新出口", button, 0},
		{idReconnect, "BUTTON", "重连当前地区", button, 0},
		{idCountryStatus, "STATIC", "", 0, 0},
		{idSpeedMode, "BUTTON", "速度优先（单出口，停止后可切换）", button | 3, 0},
		{idRulesTab, "BUTTON", "分流规则", button, 0},
		{idLogsTab, "BUTTON", "运行日志", button, 0},
		{idHomeTab, "BUTTON", "连接概览", button, 0},
		{idPageTitle, "STATIC", "", 0, 0},
		{idPageSubtitle, "STATIC", "", 0, 0},
		{idExitCaption, "STATIC", "代理出口地址", 0, 0},
		{idChosenExit, "STATIC", "", 0, 0},
		{idExitIP, "STATIC", "—", 0, 0},
		{idDownloadLabel, "STATIC", "下载速度", 0, 0},
		{idDownload, "STATIC", "0 B/s", 0, 0},
		{idUploadLabel, "STATIC", "上传速度", 0, 0},
		{idUpload, "STATIC", "0 B/s", 0, 0},
		{idTrafficLabel, "STATIC", "最近 60 秒", 0, 0},
		{idTrafficTotal, "STATIC", "↓ 0 B   ·   ↑ 0 B", 0, 0},
		{idAdvanced, "BUTTON", "高级规则", button, 0},
		{idBuiltins, "BUTTON", "内置规则", button, 0},
		{idBuiltinGroup, "COMBOBOX", "", combo, 0},
		{idBuiltinSearch, "EDIT", "", field, edgeClient},
		{idBuiltinFind, "BUTTON", "查找", button, 0},
		{idBuiltinList, "LISTBOX", "", wsTab | wsVScroll | 1 | 0x100, edgeClient},
		{idBuiltinPrev, "BUTTON", "上一页", button, 0},
		{idBuiltinNext, "BUTTON", "下一页", button, 0},
		{idBuiltinCount, "STATIC", "", 0, 0},
		{idBuiltinAction, "COMBOBOX", "", combo, 0},
		{idBuiltinSave, "BUTTON", "保存修改", button, 0},
		{idBuiltinStatus, "STATIC", "", 0, 0},
		{idPortsTab, "BUTTON", "代理端口", button, 0},
		{idPortsSave, "BUTTON", "保存端口", button, 0},
		{idPortsReset, "BUTTON", "填入默认端口", button, 0},
		{idPortsStatus, "STATIC", "", 0, 0},
		{idPortAutoLabel, "STATIC", "自动分流", 0, 0},
		{idPortAutoHelp, "STATIC", "按规则决定直接连接、走代理或阻止连接。平时用这一项。", 0, 0},
		{idPortAuto, "EDIT", "", field | 0x2000, edgeClient},
		{idPortDirectLabel, "STATIC", "直接连接", 0, 0},
		{idPortDirectHelp, "STATIC", "默认使用电脑当前的网络。", 0, 0},
		{idPortDirect, "EDIT", "", field | 0x2000, edgeClient},
		{idPortProxyLabel, "STATIC", "走代理", 0, 0},
		{idPortProxyHelp, "STATIC", "默认经你选的代理出口连接。", 0, 0},
		{idPortProxy, "EDIT", "", field | 0x2000, edgeClient},
		{idWebLabel, "STATIC", "网站直连", 0, 0},
		{idWebInput, "EDIT", "", field, edgeClient},
		{idWebAdd, "BUTTON", "添加直连", button, 0},
		{idWebList, "LISTBOX", "", wsTab | wsVScroll | 1 | 0x100, edgeClient},
		{idWebRemove, "BUTTON", "移除选中", button, 0},
		{idWebEmpty, "STATIC", "还没有添加网站", 1, 0},
		{idIPLabel, "STATIC", "IP / 网段直连", 0, 0},
		{idIPInput, "EDIT", "", field, edgeClient},
		{idIPAdd, "BUTTON", "添加直连", button, 0},
		{idIPList, "LISTBOX", "", wsTab | wsVScroll | 1 | 0x100, edgeClient},
		{idIPRemove, "BUTTON", "移除选中", button, 0},
		{idIPEmpty, "STATIC", "还没有添加 IP / 网段", 1, 0},
		{idDirectStatus, "STATIC", "", 0, 0},
		{idHint, "STATIC", "规则从上到下匹配。输入域名、IP 或网段；+.example.com 包含子域。", 0, 0},
		{idRuleScope, "COMBOBOX", "", combo, 0},
		{idRuleList, "LISTBOX", "", wsTab | wsVScroll | 1 | 0x100, edgeClient},
		{idRuleTarget, "EDIT", "", field, edgeClient},
		{idAction, "COMBOBOX", "", combo, 0},
		{idNewRule, "BUTTON", "新建", button, 0},
		{idSave, "BUTTON", "保存规则", button, 0},
		{idDelete, "BUTTON", "删除", button, 0},
		{idUp, "BUTTON", "上移", button, 0},
		{idDown, "BUTTON", "下移", button, 0},
		{idTestInput, "EDIT", "", field, edgeClient},
		{idTest, "BUTTON", "测试匹配", button, 0},
		{idOpenMatchedRule, "BUTTON", "打开命中规则", button, 0},
		{idRuleStatus, "STATIC", "", 0, 0},
		{idLogFolder, "BUTTON", "打开日志文件夹", button, 0},
		{idLogText, "EDIT", "", wsTab | wsVScroll | 0x0004 | 0x0800 | 0x0040, edgeClient},
	}
	for _, c := range controls {
		style := c.style
		if c.kind == "BUTTON" && c.id != idSystem && c.id != idSpeedMode {
			style |= 0x0b // BS_OWNERDRAW
		}
		if err := d.control(c.id, c.kind, c.text, style, c.extra); err != nil {
			return err
		}
	}
	for _, a := range ruleActions {
		addText(d.controls[idAction], cbAdd, actionLabel(a))
	}
	send(d.controls[idRuleTarget], 0xc5, 254, 0) // EM_SETLIMITTEXT
	send(d.controls[idTestInput], 0xc5, 2048, 0)
	sendMessage.Call(d.controls[idRuleTarget], 0x1501, 1, uintptr(unsafe.Pointer(wide("输入域名、地址或网段"))))
	sendMessage.Call(d.controls[idTestInput], 0x1501, 1, uintptr(unsafe.Pointer(wide("输入域名或 IP，例如 chatgpt.com"))))
	sendMessage.Call(d.controls[idWebInput], 0x1501, 1, uintptr(unsafe.Pointer(wide("域名或网址，如 example.com"))))
	sendMessage.Call(d.controls[idIPInput], 0x1501, 1, uintptr(unsafe.Pointer(wide("IP 或网段，如 192.0.2.0/24"))))
	send(d.controls[idWebInput], 0xc5, 2048, 0)
	send(d.controls[idIPInput], 0xc5, 64, 0)
	if settings, err := queryProxySettings(); shouldEnableSystemProxyByDefault(settings, err) {
		send(d.controls[idSystem], bmSet, 1, 0)
	}
	if d.speedMode {
		send(d.controls[idSpeedMode], bmSet, 1, 0)
	}
	d.initRules()
	enable(d.controls[idOpenMatchedRule], false)
	d.initBuiltins()
	d.fillPorts(false)
	d.fillCountries()
	d.showChosenExit()
	d.fillDirectLists()
	d.selectPage(pageHome)
	d.updateButtons()
	return nil
}

func (d *desktop) layout(width, height int) {
	if len(d.controls) == 0 || width == 0 || height == 0 {
		return
	}
	w := width * 96 / d.dpi
	h := height * 96 / d.dpi
	d.clientWidth, d.clientHeight = w, h
	place := func(id, x, y, w, h int) { move(d.controls[id], d.scale(x), d.scale(y), d.scale(w), d.scale(h)) }
	const x = 194
	right := w - 30
	body := right - x
	place(idTitle, 22, 31, 132, 35)
	place(idHomeTab, 13, 103, 137, 44)
	place(idRulesTab, 13, 155, 137, 44)
	place(idLogsTab, 13, 207, 137, 44)
	place(idPortsTab, 13, 259, 137, 44)
	place(idPageTitle, x, 29, body-260, 40)
	place(idPageSubtitle, x, 73, body, 24)
	place(idState, x+21, 131, body-192, 26)
	place(idSystem, x+21, 178, 179, 31)
	place(idBrowser, x+209, 178, 144, 31)
	place(idConnect, right-144, 128, 124, 40)
	place(idCountryLabel, x+21, 239, 94, 22)
	place(idCountry, x+121, 233, body-338, 330)
	place(idRefresh, right-206, 230, 82, 35)
	place(idCountryApply, right-114, 230, 93, 35)
	place(idCountryStatus, x+21, 277, body-42, 24)
	place(idSpeedMode, x+21, 298, body-42, 20)
	place(idExitCaption, x+20, 340, body-166, 22)
	place(idExitIP, x+20, 363, body-151, 32)
	place(idChosenExit, x+20, 397, body-160, 21)
	place(idCheckExit, right-116, 342, 94, 29)
	place(idReconnect, right-136, 378, 114, 29)
	metric := (body - 16) / 2
	place(idDownloadLabel, x+20, 441, metric-35, 22)
	place(idDownload, x+20, 466, metric-35, 33)
	place(idUploadLabel, x+metric+36, 441, metric-35, 22)
	place(idUpload, x+metric+36, 466, metric-35, 33)
	place(idTrafficLabel, x+20, 544, 140, 22)
	place(idTrafficTotal, right-294, 544, 275, 22)
	place(idAdvanced, right-119, 37, 119, 34)
	place(idBuiltins, right-248, 37, 119, 34)
	place(idBuiltinGroup, x+18, 125, 170, 260)
	place(idBuiltinSearch, x+199, 125, body-309, 32)
	place(idBuiltinFind, right-100, 123, 81, 34)
	place(idBuiltinList, x+18, 176, body-36, h-358)
	place(idBuiltinPrev, x+18, h-168, 78, 32)
	place(idBuiltinNext, x+104, h-168, 78, 32)
	place(idBuiltinCount, x+194, h-162, body-215, 24)
	place(idBuiltinAction, x+18, h-117, 180, 215)
	place(idBuiltinSave, x+209, h-119, 104, 34)
	place(idBuiltinStatus, x+18, h-69, body-36, 34)
	for i, port := range desktopPorts {
		y := 134 + i*111
		place(port.label, x+20, y, body-180, 23)
		place(port.help, x+20, y+31, body-175, 28)
		place(port.field, right-139, y+9, 119, 33)
	}
	place(idPortsSave, x+20, 477, 108, 35)
	place(idPortsReset, x+140, 477, 130, 35)
	place(idPortsStatus, x+20, 532, body-40, 50)
	panel := (body - 16) / 2
	directListHeight := max(100, h-326)
	for index, ids := range [][5]int{{idWebLabel, idWebInput, idWebAdd, idWebList, idWebRemove}, {idIPLabel, idIPInput, idIPAdd, idIPList, idIPRemove}} {
		left := x + index*(panel+16) + 17
		place(ids[0], left, 130, panel-34, 25)
		place(ids[1], left, 169, panel-130, 33)
		place(ids[2], left+panel-122, 167, 88, 36)
		place(ids[3], left, 220, panel-34, directListHeight)
		place(ids[4], left, h-93, 92, 34)
		empty := [...]int{idWebEmpty, idIPEmpty}[index]
		place(empty, left, 220+directListHeight/2-13, panel-34, 26)
	}
	place(idDirectStatus, x+2, h-41, body-4, 24)
	place(idRuleScope, right-195, 125, 174, 230)
	place(idHint, x+20, 172, body-40, 24)
	place(idRuleList, x+20, 208, body-40, max(60, h-380))
	place(idRuleTarget, x+20, h-157, body-419, 31)
	place(idAction, right-392, h-157, 102, 170)
	place(idNewRule, right-282, h-157, 50, 31)
	place(idSave, right-225, h-157, 73, 31)
	place(idDelete, right-145, h-157, 36, 31)
	place(idUp, right-103, h-157, 36, 31)
	place(idDown, right-61, h-157, 41, 31)
	place(idTestInput, x+20, h-104, body-168, 31)
	place(idTest, right-139, h-105, 119, 34)
	place(idRuleStatus, x+20, h-54, body-172, 28)
	place(idOpenMatchedRule, right-139, h-62, 119, 34)
	place(idLogFolder, right-148, 36, 148, 35)
	place(idLogText, x+15, 128, body-30, h-153)
	d.invalidate()
}

func (d *desktop) selectPage(page int) {
	d.page = page
	groups := []struct {
		visible bool
		ids     []int
	}{
		{page == pageHome, []int{idState, idConnect, idSystem, idBrowser, idCountryLabel, idCountry, idRefresh, idCountryApply, idCheckExit, idReconnect, idCountryStatus, idSpeedMode, idExitIP, idExitCaption, idChosenExit, idDownloadLabel, idDownload, idUploadLabel, idUpload, idTrafficLabel, idTrafficTotal}},
		{page == pageRules, []int{idAdvanced, idBuiltins}},
		{page == pageRules && d.advanced && !d.showBuiltins, []int{idHint, idRuleList, idRuleTarget, idAction, idNewRule, idSave, idDelete, idUp, idDown, idTestInput, idTest, idOpenMatchedRule, idRuleStatus, idRuleScope}},
		{page == pageRules && !d.advanced && !d.showBuiltins, []int{idWebLabel, idWebInput, idWebAdd, idWebList, idWebRemove, idIPLabel, idIPInput, idIPAdd, idIPList, idIPRemove, idDirectStatus}},
		{page == pageRules && d.showBuiltins, []int{idBuiltinGroup, idBuiltinSearch, idBuiltinFind, idBuiltinList, idBuiltinPrev, idBuiltinNext, idBuiltinCount, idBuiltinAction, idBuiltinSave, idBuiltinStatus}},
		{page == pageLogs, []int{idLogText, idLogFolder}},
		{page == pagePorts, []int{idPortsSave, idPortsReset, idPortsStatus, idPortAutoLabel, idPortAutoHelp, idPortAuto, idPortDirectLabel, idPortDirectHelp, idPortDirect, idPortProxyLabel, idPortProxyHelp, idPortProxy}},
	}
	for _, group := range groups {
		for _, id := range group.ids {
			showControl(d.controls[id], group.visible)
		}
	}
	d.showDirectEmpty()
	titles := []string{"连接概览", "分流规则", "运行日志", "代理端口"}
	subtitles := []string{"查看连接、出口和通过本地代理的流量。", "以下规则优先匹配。网站包含子域，IP 规则匹配连接中的 IP 地址。", "最新日志在上方。完整日志保存在本机。", "代理地址为 127.0.0.1。系统代理使用自动分流端口。"}
	setText(d.controls[idPageTitle], titles[page])
	setText(d.controls[idPageSubtitle], subtitles[page])
	if page == pageRules {
		text := "高级规则"
		if d.advanced && !d.showBuiltins {
			text = "返回直连设置"
			setText(d.controls[idPageSubtitle], "按端口设置直接连接、走代理或阻止连接。")
		}
		builtins := "内置规则"
		if d.showBuiltins {
			builtins = "返回直连设置"
			setText(d.controls[idPageSubtitle], "选择一条规则后可调整连接方式，或恢复默认。自定义规则优先。")
		}
		setText(d.controls[idBuiltins], builtins)
		setText(d.controls[idAdvanced], text)
	}
	d.invalidate()
	if page == pageLogs {
		d.updateLog(true)
	}
}

func showControl(hwnd uintptr, visible bool) {
	flag := uintptr(0)
	if visible {
		flag = 5
	}
	showWindow.Call(hwnd, flag)
}

func (d *desktop) command(id, notify int) {
	if id == idBuiltinGroup && notify == 1 {
		d.searchBuiltins()
		return
	}
	if id == idBuiltinList && notify == 1 {
		d.pickBuiltin()
		return
	}
	if id == idBuiltinSearch && notify == 0x300 && d.showBuiltins {
		d.searchBuiltins()
		return
	}
	if notify == 1 && (id == idWebList || id == idIPList) {
		d.updateDirectButtons()
		return
	}
	if id == idRuleList && notify == 1 {
		d.pickRule()
		return
	}
	if id == idRuleScope && notify == 1 {
		d.loadRuleList(-1)
		return
	}
	if id == idCountry && notify == 1 {
		d.updateButtons()
		return
	}
	if notify != 0 {
		return
	}
	switch id {
	case idHomeTab:
		d.selectPage(pageHome)
	case idConnect:
		if d.worker == nil {
			d.connect()
		} else {
			d.stop()
		}
	case idSystem:
		if d.ready && !d.stopping {
			if send(d.controls[idSystem], bmGet, 0, 0) == 0 {
				if d.systemOn {
					d.setSystem(false)
				}
			} else if d.sdkStatus.connected() && !d.switching {
				d.setSystem(true)
			}
		}
	case idBrowser:
		if d.worker != nil {
			d.send("browser", "")
		}
	case idSpeedMode:
		enabled := send(d.controls[idSpeedMode], bmGet, 0, 0) == 1
		if err := saveSpeedPreference(d.root, enabled); err != nil {
			if d.speedMode {
				send(d.controls[idSpeedMode], bmSet, 1, 0)
			} else {
				send(d.controls[idSpeedMode], bmSet, 0, 0)
			}
			d.alert("无法保存速度模式，请检查运行日志。")
			log.Print("保存速度模式失败。")
			return
		}
		d.speedMode = enabled
	case idRefresh:
		d.refreshCountries()
	case idCountryApply:
		d.applyCountry()
	case idCheckExit:
		d.requestExit()
	case idReconnect:
		if d.ready && !d.stopping && !d.switching && d.send("reconnect", d.wanted.ID) {
			d.switching = true
			setText(d.controls[idCountryStatus], "正在重连 "+displayExit(d.wanted)+"…")
			d.updateButtons()
		}
	case idRulesTab:
		d.selectPage(pageRules)
	case idLogsTab:
		d.selectPage(pageLogs)
	case idPortsTab:
		d.selectPage(pagePorts)
	case idAdvanced:
		d.advanced = d.showBuiltins || !d.advanced
		d.showBuiltins = false
		d.selectPage(pageRules)
	case idBuiltins:
		d.advanced = false
		d.showBuiltins = !d.showBuiltins
		d.selectPage(pageRules)
		if d.showBuiltins {
			d.searchBuiltins()
		}
	case idBuiltinFind:
		d.searchBuiltins()
	case idBuiltinPrev:
		d.builtinPage--
		d.fillBuiltins(-1)
	case idBuiltinNext:
		d.builtinPage++
		d.fillBuiltins(-1)
	case idBuiltinSave:
		d.saveBuiltin()
	case idPortsSave:
		d.savePorts()
	case idPortsReset:
		d.fillPorts(true)
	case idWebAdd, idIPAdd, idWebRemove, idIPRemove:
		d.changeDirect(id)
	case idNewRule:
		send(d.controls[idRuleList], lbSelect, ^uintptr(0), 0)
		d.pickRule()
		setFocus.Call(d.controls[idRuleTarget])
	case idSave, idDelete, idUp, idDown:
		d.changeRules(id)
	case idTest:
		d.testRule()
	case idOpenMatchedRule:
		d.openMatchedRule()
	case idLogFolder:
		if err := openLogFolder(d.logs); err != nil {
			messageBox(err.Error(), 0x10)
		}
	}
}

func (d *desktop) post(event desktopEvent) {
	d.inbox <- event
	postMessage.Call(d.window, wmEvent, 0, 0)
}

func (d *desktop) receive() {
	for {
		select {
		case event := <-d.inbox:
			d.handle(event)
		default:
			return
		}
	}
}

func (d *desktop) handle(event desktopEvent) {
	switch event.Kind {
	case "countries":
		d.refreshing = false
		if event.err != nil {
			setText(d.controls[idCountryStatus], event.err.Error())
			log.Print(event.err)
		} else {
			d.countries = event.countries
			d.fillCountries()
			setText(d.controls[idCountryStatus], fmt.Sprintf("已加载 %d 个有可用节点的国家和地区。", len(d.countries)))
		}
	case "log":
		d.logPath, d.logSize = event.Value, -1
		d.updateLog(true)
	case "ready":
		if !d.stopping {
			d.ready = true
			d.showConnectionState()
		}
	case "speed-fallback":
		setText(d.controls[idCountryStatus], "速度优先连接失败，已切回自动模式。")
	case "speed-fallback-error":
		setText(d.controls[idCountryStatus], "速度模式连接失败，切换自动模式也失败，请查看日志。")
	case "sdk-status":
		if event.SDK != nil && !d.stopping {
			wasConnected := d.sdkStatus.connected()
			d.sdkStatus = *event.SDK
			d.showConnectionState()
			if d.sdkStatus.connected() && !d.switching {
				d.enableRequestedSystemProxy()
				if !wasConnected {
					d.requestExit()
				}
			} else if wasConnected {
				d.exitCurrent = false
				d.showExit()
			}
		}
	case "system-on", "system-off":
		d.systemOn, d.systemBusy = event.Kind == "system-on", false
		if d.ready && !d.stopping {
			d.showConnectionState()
		}
	case "system-wait":
		d.systemBusy = false
		if d.ready && !d.stopping {
			d.showConnectionState()
		}
	case "system-error":
		d.systemOn, d.systemBusy = false, false
		send(d.controls[idSystem], bmSet, 0, 0)
		setText(d.controls[idState], event.Value)
		d.alert(event.Value)
	case "location", "location-error":
		d.switching = false
		if event.Kind == "location" {
			setText(d.controls[idCountryStatus], "正在验证 "+displayExit(d.wanted)+" 的出口。")
			d.exit = exitObservation{}
			d.exitVerifiedAt, d.lastExitProbe = time.Time{}, time.Time{}
			d.exitProbeFailed, d.exitCurrent, d.probing = false, false, false
			d.sdkStatus = sdkConnectionStatus{}
			d.showConnectionState()
			d.showExit()
		} else {
			setText(d.controls[idCountryStatus], event.Value)
			d.enableRequestedSystemProxy()
		}
	case "probe", "probe-error":
		d.probing = false
		if !d.stopping && !d.switching {
			if event.Exit != nil {
				d.exit = *event.Exit
				d.exitVerifiedAt = time.Now()
				d.exitProbeFailed = false
				d.exitCurrent = d.sdkStatus.connected()
				if strings.HasPrefix(getText(d.controls[idCountryStatus]), "正在验证 ") {
					setText(d.controls[idCountryStatus], "已完成出口检测。")
				}
				if d.wanted.Code != "" && d.exit.Country != "" && d.wanted.Code != d.exit.Country {
					log.Printf("出口地区与所选地区不同：选择 %s，检测 %s。", d.wanted.Code, d.exit.Country)
				}
			} else {
				d.exitProbeFailed, d.exitCurrent = true, false
			}
			d.showExit()
		}
	case "traffic":
		if event.Traffic != nil && !d.stopping {
			d.traffic = *event.Traffic
			copy(d.history[:], d.history[1:])
			d.history[len(d.history)-1] = d.traffic
			d.showTraffic()
		}
	case "error":
		d.errorShown = true
		setText(d.controls[idState], event.Value)
		if !d.closing && event.Value != errGuestCancelled.Error() {
			d.alert(event.Value + "\n\n可在「运行日志」查看详情。")
		}
	case "exit":
		if d.worker != nil {
			_ = d.worker.input.Close()
		}
		d.worker = nil
		d.ready, d.stopping, d.systemBusy, d.systemOn, d.probing, d.switching = false, false, false, false, false, false
		d.exit = exitObservation{}
		d.exitVerifiedAt, d.lastExitProbe = time.Time{}, time.Time{}
		d.exitProbeFailed, d.exitCurrent = false, false
		d.sdkStatus = sdkConnectionStatus{}
		d.traffic = trafficSample{}
		d.history = [60]trafficSample{}
		d.showExit()
		d.showTraffic()
		if d.closing {
			destroyWindow.Call(d.window)
			return
		}
		if !d.errorShown {
			text := "已停止"
			if event.Value != "" {
				text = "代理已退出，请查看运行日志"
			}
			setText(d.controls[idState], text)
		}
	}
	d.updateButtons()
}

func (d *desktop) requestExit() {
	if d.ready && d.sdkStatus.connected() && !d.probing && !d.stopping && !d.switching && d.send("probe", "") {
		d.lastExitProbe = time.Now()
		d.probing = true
		if d.exit.IP == "" {
			setText(d.controls[idExitIP], "正在检测…")
		}
		d.updateButtons()
	}
}

func (d *desktop) enableRequestedSystemProxy() {
	if d.sdkStatus.connected() && !d.stopping && !d.systemOn && !d.systemBusy && send(d.controls[idSystem], bmGet, 0, 0) == 1 {
		d.setSystem(true)
	}
}

func (d *desktop) showConnectionState() {
	text := "正在连接 URnetwork 节点"
	if d.sdkStatus.connected() {
		text = "URnetwork 节点已连接"
	} else if d.sdkStatus.State == "CONNECT_FAILED" {
		text = "暂未连到节点，可点击重新连接"
	}
	if d.systemOn {
		text += "   ·   系统代理开启"
	}
	setText(d.controls[idState], text)
}

func (d *desktop) showExit() {
	if d.exit.IP == "" {
		text := "—"
		if d.exitProbeFailed {
			text = "暂未检测到"
		}
		setText(d.controls[idExitIP], text)
		setText(d.controls[idExitCaption], "代理出口地址")
		return
	}
	setText(d.controls[idExitIP], d.exit.IP)
	caption := "检测到的地区："
	if d.exit.Country != "" {
		caption += countryName(d.exit.Country, d.exit.Country)
	} else {
		caption += "未知"
	}
	if d.wanted.Code != "" && d.exit.Country != "" && d.wanted.Code != d.exit.Country {
		caption += "（与所选地区不同）"
	}
	if !d.exitCurrent {
		caption = "上次成功 " + d.exitVerifiedAt.Format("15:04:05") + " · " + caption
	}
	setText(d.controls[idExitCaption], caption)
}

func (d *desktop) showChosenExit() {
	label := "自动选择"
	if d.wanted.ID != "" {
		label = countryName(d.wanted.Code, d.wanted.Name)
	}
	setText(d.controls[idChosenExit], "所选出口："+label)
}

func formatBytes(n float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}

func (d *desktop) showTraffic() {
	setText(d.controls[idDownload], formatBytes(d.traffic.DownRate)+"/s")
	setText(d.controls[idUpload], formatBytes(d.traffic.UpRate)+"/s")
	setText(d.controls[idTrafficTotal], "↓ "+formatBytes(float64(d.traffic.Download))+"   ·   ↑ "+formatBytes(float64(d.traffic.Upload)))
	if d.page == pageHome {
		d.invalidateTraffic()
	}
}

func (d *desktop) alert(message string) {
	user32.NewProc("MessageBoxW").Call(d.window, uintptr(unsafe.Pointer(wide(message))), uintptr(unsafe.Pointer(wide("网络代理"))), 0x10)
}

func (d *desktop) updateButtons() {
	text := "启动代理"
	if d.worker != nil {
		text = "停止代理"
	}
	setText(d.controls[idConnect], text)
	enable(d.controls[idConnect], !d.stopping)
	enable(d.controls[idSystem], (d.worker == nil || d.ready) && !d.stopping && !d.systemBusy)
	enable(d.controls[idBrowser], d.ready && !d.stopping)
	enable(d.controls[idRefresh], !d.refreshing && !d.switching)
	enable(d.controls[idCountry], !d.switching && !d.stopping)
	enable(d.controls[idCountryApply], !d.switching && !d.stopping && (d.worker == nil || d.ready))
	enable(d.controls[idSpeedMode], d.worker == nil && !d.stopping)
	enable(d.controls[idCheckExit], d.ready && d.sdkStatus.connected() && !d.stopping && !d.probing && !d.switching)
	enable(d.controls[idReconnect], d.ready && !d.stopping && !d.switching)
	d.updatePortControls()
}

func (d *desktop) connect() {
	d.errorShown = false
	worker, err := startWorker(d.wanted.ID, d.speedMode, func(event workerEvent) { d.post(desktopEvent{workerEvent: event}) })
	if err != nil {
		messageBox("启动代理："+err.Error(), 0x10)
		return
	}
	d.worker = worker
	setText(d.controls[idState], "正在启动代理，请稍候…")
	d.updateButtons()
}

func (d *desktop) send(kind, id string) bool {
	if d.worker == nil {
		return false
	}
	if err := d.worker.send(kind, id); err != nil {
		setText(d.controls[idState], "无法与代理进程通信，请查看日志")
		log.Print(err)
		return false
	}
	return true
}

func (d *desktop) setSystem(on bool) {
	command := "system-off"
	if on {
		command = "system-on"
	}
	d.systemBusy = d.send(command, "")
	if !d.systemBusy {
		value := uintptr(0)
		if d.systemOn {
			value = 1
		}
		send(d.controls[idSystem], bmSet, value, 0)
	}
	d.updateButtons()
}

func (d *desktop) stop() {
	if d.worker == nil || d.stopping {
		return
	}
	d.stopping = true
	setText(d.controls[idState], "正在停止代理并恢复系统设置…")
	d.updateButtons()
	_ = d.worker.input.Close()
	process := d.worker.cmd.Process
	go func() {
		time.Sleep(4 * time.Second)
		_ = process.Kill() // May already have exited. The proxy watcher also handles EOF.
	}()
}

func (d *desktop) closeWindow() {
	if d.worker == nil {
		destroyWindow.Call(d.window)
		return
	}
	d.closing = true
	d.stop()
}

func (d *desktop) updateLog(force bool) {
	if d.page != pageLogs || d.logPath == "" {
		return
	}
	f, err := os.Open(d.logPath)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || (!force && info.Size() == d.logSize) {
		return
	}
	offset := max(0, info.Size()-24000)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, 24000))
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	d.logSize = info.Size()
	setText(d.controls[idLogText], newestLogFirst(string(data), offset > 0))
	send(d.controls[idLogText], 0xb1, 0, 0)
	send(d.controls[idLogText], 0x00b7, 0, 0) // EM_SCROLLCARET
}

func newestLogFirst(text string, startsMidFile bool) string {
	if startsMidFile {
		_, text, _ = strings.Cut(text, "\n")
	}
	text = strings.ReplaceAll(strings.ToValidUTF8(text, "�"), "\x00", "")
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n")
	// Reverse records while keeping any continuation lines with their entry.
	var records []string
	start := 0
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		goLog := len(line) >= 6 && strings.ContainsRune("IWEF", rune(line[0])) && line[1] >= '0' && line[1] <= '9' && line[5] == ' '
		appLog := len(line) >= 11 && line[4] == '/' && line[7] == '/' && line[10] == ' '
		if goLog || appLog {
			records = append(records, strings.Join(lines[start:i], "\r\n"))
			start = i
		}
	}
	records = append(records, strings.Join(lines[start:], "\r\n"))
	slices.Reverse(records)
	return strings.Join(records, "\r\n")
}
