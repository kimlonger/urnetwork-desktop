package main

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
)

var desktopPorts = []struct {
	name                     string
	mode                     action
	label, help, field, port int
}{
	{"auto", auto, idPortAutoLabel, idPortAutoHelp, idPortAuto, 18790},
	{"direct", direct, idPortDirectLabel, idPortDirectHelp, idPortDirect, 18791},
	{"ur", ur, idPortProxyLabel, idPortProxyHelp, idPortProxy, 18792},
}

func profileLabel(name string, mode action) string {
	for _, p := range desktopPorts {
		if p.name == name {
			switch p.mode {
			case auto:
				return "自动分流"
			default:
				return actionLabel(p.mode)
			}
		}
	}
	if mode == auto {
		return "自定义分流"
	}
	return actionLabel(mode)
}

func (d *desktop) fillPorts(defaults bool) {
	for _, port := range desktopPorts {
		value := port.port
		if !defaults {
			if profile, ok := d.rules.Profiles[port.name]; ok {
				value = profile.Port
			}
		}
		field := d.controls[port.field]
		send(field, 0xc5, 5, 0)
		setText(field, strconv.Itoa(value))
	}
	if defaults {
		setText(d.controls[idPortsStatus], "已填入默认值，点击「保存端口」应用。")
	}
}

func (d *desktop) updatePortControls() {
	editable := d.worker == nil
	const running = "端口正在使用。停止代理后即可修改。"
	for _, port := range desktopPorts {
		enable(d.controls[port.field], editable)
	}
	enable(d.controls[idPortsSave], editable)
	enable(d.controls[idPortsReset], editable)
	if !editable {
		setText(d.controls[idPortsStatus], running)
	} else if getText(d.controls[idPortsStatus]) == running {
		setText(d.controls[idPortsStatus], "")
	}
}

func (d *desktop) savePorts() {
	if d.worker != nil {
		return
	}
	status := d.controls[idPortsStatus]
	current, snapshot, err := readRulesConfig(d.rulesPath)
	if err != nil {
		log.Print(err)
		setText(status, "无法读取当前设置，请查看运行日志。")
		return
	}
	changed := make(map[string]int)
	for _, field := range desktopPorts {
		port, err := strconv.Atoi(strings.TrimSpace(getText(d.controls[field.field])))
		if err != nil || port < 1024 || port > 65535 {
			setText(status, "请输入 1024 到 65535 之间的有效端口。")
			return
		}
		profile, exists := current.Profiles[field.name]
		if !exists {
			profile.Mode = field.mode
		}
		if profile.Port != port {
			changed[field.name] = port
		}
		profile.Port = port
		current.Profiles[field.name] = profile
	}
	seen := make(map[int]bool)
	for _, p := range current.Profiles {
		if seen[p.Port] {
			setText(status, "每种连接方式需要不同的端口。")
			return
		}
		seen[p.Port] = true
	}
	for _, port := range changed {
		listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			setText(status, fmt.Sprintf("端口 %d 无法使用，请换一个端口或退出占用它的程序。", port))
			return
		}
		_ = listener.Close()
	}
	_, newest, err := readRulesConfig(d.rulesPath)
	if err != nil || !bytes.Equal(newest, snapshot) {
		setText(status, "设置文件正在被其他程序修改，请重试。")
		return
	}
	if len(changed) != 0 {
		if err := writeJSONFile(d.rulesPath, current); err != nil {
			log.Print(err)
			setText(status, "保存失败，请查看运行日志。")
			return
		}
	}
	d.rules = current
	d.initRules()
	d.fillPorts(false)
	if d.showBuiltins {
		d.searchBuiltins()
	}
	setText(status, "端口已保存，下次启动代理时生效。")
}
