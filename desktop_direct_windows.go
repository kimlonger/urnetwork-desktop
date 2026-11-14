package main

import (
	"bytes"
	"log"
	"slices"
)

func (d *desktop) fillDirectLists() {
	for id, rows := range map[int][]string{idWebList: d.bypass.Websites, idIPList: d.bypass.IPs} {
		list := d.controls[id]
		send(list, lbReset, 0, 0)
		for _, row := range rows {
			addText(list, lbAdd, row)
		}
	}
	d.updateDirectButtons()
	d.showDirectEmpty()
}

func (d *desktop) showDirectEmpty() {
	visible := d.page == pageRules && !d.advanced && !d.showBuiltins
	for _, panel := range []struct {
		list, empty int
		count       int
	}{{idWebList, idWebEmpty, len(d.bypass.Websites)}, {idIPList, idIPEmpty, len(d.bypass.IPs)}} {
		showControl(d.controls[panel.empty], visible && panel.count == 0)
		showControl(d.controls[panel.list], visible && panel.count != 0)
	}
}

func (d *desktop) updateDirectButtons() {
	enable(d.controls[idWebRemove], int(send(d.controls[idWebList], lbGet, 0, 0)) >= 0)
	enable(d.controls[idIPRemove], int(send(d.controls[idIPList], lbGet, 0, 0)) >= 0)
}

func (d *desktop) changeDirect(id int) {
	current, snapshot, err := readBypass(d.bypassPath)
	if err != nil {
		log.Print(err)
		setText(d.controls[idDirectStatus], err.Error())
		return
	}
	if !slices.Equal(current.Websites, d.bypass.Websites) || !slices.Equal(current.IPs, d.bypass.IPs) {
		d.bypass = current
		d.fillDirectLists()
		setText(d.controls[idDirectStatus], "规则已在其他窗口修改，请核对后重试。")
		return
	}
	rows, field, list := &current.Websites, idWebInput, idWebList
	parse := websiteTarget
	if id == idIPAdd || id == idIPRemove {
		rows, field, list = &current.IPs, idIPInput, idIPList
		parse = ipTarget
	}
	adding := id == idWebAdd || id == idIPAdd
	if adding {
		target, err := parse(getText(d.controls[field]))
		if err != nil {
			setText(d.controls[idDirectStatus], err.Error())
			return
		}
		if slices.Contains(*rows, target) {
			setText(d.controls[idDirectStatus], target+" 已在直连规则中。")
			return
		}
		*rows = append(*rows, target)
	} else {
		i := int(send(d.controls[list], lbGet, 0, 0))
		if i < 0 || i >= len(*rows) {
			return
		}
		*rows = slices.Delete(*rows, i, i+1)
	}
	_, newest, err := readBypass(d.bypassPath)
	if err != nil || !bytes.Equal(snapshot, newest) {
		setText(d.controls[idDirectStatus], "规则文件正在被修改，请重试。")
		return
	}
	if err := writeJSONFile(d.bypassPath, current); err != nil {
		log.Print(err)
		setText(d.controls[idDirectStatus], "保存失败，请查看运行日志。")
		return
	}
	d.bypass = current
	d.fillDirectLists()
	if adding {
		setText(d.controls[field], "")
		setFocus.Call(d.controls[field])
		setText(d.controls[idDirectStatus], "已添加直连规则。")
	} else {
		setText(d.controls[idDirectStatus], "已移除，目标将按其余分流规则处理。")
	}
}
