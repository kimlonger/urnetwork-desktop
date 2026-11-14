package main

import "unsafe"

type desktopPalette struct {
	canvas, side, white, border, blue, active, navy, sky, disabled uintptr
}

const (
	ink         = 0x292218
	mutedInk    = 0x77695b
	blueInk     = 0xb24827
	downloadInk = 0xca6831
	uploadInk   = 0x879f14
)

var (
	makeBrush      = gdi32.NewProc("CreateSolidBrush")
	makePen        = gdi32.NewProc("CreatePen")
	selectObject   = gdi32.NewProc("SelectObject")
	roundRect      = gdi32.NewProc("RoundRect")
	fillRect       = user32.NewProc("FillRect")
	drawText       = user32.NewProc("DrawTextW")
	textColor      = gdi32.NewProc("SetTextColor")
	backgroundMode = gdi32.NewProc("SetBkMode")
	invalidateRect = user32.NewProc("InvalidateRect")
)

func (p *desktopPalette) init() {
	colors := []uintptr{0xf9f7f3, 0x3d281c, 0xffffff, 0xeee8df, 0xa85029, 0x674531, 0x5b3d2b, 0xfcf4eb, 0xf4f1eb}
	brushes := []*uintptr{&p.canvas, &p.side, &p.white, &p.border, &p.blue, &p.active, &p.navy, &p.sky, &p.disabled}
	for i, target := range brushes {
		*target, _, _ = makeBrush.Call(colors[i])
	}
}

func (p *desktopPalette) free() {
	for _, brush := range []uintptr{p.canvas, p.side, p.white, p.border, p.blue, p.active, p.navy, p.sky, p.disabled} {
		deleteObject.Call(brush)
	}
}

type paintState struct {
	DC              uintptr
	Erase           int32
	Area            winRect
	Restore, Update int32
	Reserved        [32]byte
}

type drawItem struct {
	Kind, ID, Index, Action, State uint32
	Window, DC                     uintptr
	Area                           winRect
	Data                           uintptr
}

func (d *desktop) rect(x, y, right, bottom int) winRect {
	return winRect{int32(d.scale(x)), int32(d.scale(y)), int32(d.scale(right)), int32(d.scale(bottom))}
}

func (d *desktop) invalidate() {
	invalidateRect.Call(d.window, 0, 0)
}

func (d *desktop) invalidateTraffic() {
	area := d.rect(193, 574, d.clientWidth-29, d.clientHeight)
	invalidateRect.Call(d.window, uintptr(unsafe.Pointer(&area)), 0)
}

func fill(dc uintptr, area winRect, brush uintptr) {
	fillRect.Call(dc, uintptr(unsafe.Pointer(&area)), brush)
}

func rounded(dc uintptr, area winRect, brush uintptr, radius int) {
	nullPen, _, _ := gdi32.NewProc("GetStockObject").Call(8)
	oldPen, _, _ := selectObject.Call(dc, nullPen)
	oldBrush, _, _ := selectObject.Call(dc, brush)
	roundRect.Call(dc, uintptr(area.Left), uintptr(area.Top), uintptr(area.Right), uintptr(area.Bottom), uintptr(radius), uintptr(radius))
	selectObject.Call(dc, oldBrush)
	selectObject.Call(dc, oldPen)
}

func (d *desktop) card(dc uintptr, x, y, right, bottom int) {
	rounded(dc, d.rect(x, y, right, bottom), d.palette.border, d.scale(20))
	rounded(dc, d.rect(x+1, y+1, right-1, bottom-1), d.palette.white, d.scale(20))
}

func (d *desktop) paint() {
	var state paintState
	beginPaint := user32.NewProc("BeginPaint")
	dc, _, _ := beginPaint.Call(d.window, uintptr(unsafe.Pointer(&state)))
	defer user32.NewProc("EndPaint").Call(d.window, uintptr(unsafe.Pointer(&state)))
	if dc == 0 || d.clientWidth == 0 {
		return
	}
	w, h := d.clientWidth, d.clientHeight
	fill(dc, d.rect(0, 0, w+2, h+2), d.palette.canvas)
	fill(dc, d.rect(0, 0, 164, h+2), d.palette.side)
	right := w - 30
	const x = 194
	switch d.page {
	case pageHome:
		d.card(dc, x, 110, right, 320)
		fill(dc, d.rect(x+20, 222, right-20, 223), d.palette.border)
		d.card(dc, x, 328, right, 421)
		half := (right - x - 16) / 2
		d.card(dc, x, 424, x+half, 514)
		d.card(dc, x+half+16, 424, right, 514)
		d.card(dc, x, 527, right, h-20)
		d.paintGraph(dc, x+20, 576, right-20, h-38)
	case pageRules:
		if d.advanced || d.showBuiltins {
			d.card(dc, x, 107, right, h-19)
		} else {
			half := (right - x - 16) / 2
			d.card(dc, x, 107, x+half, h-49)
			d.card(dc, x+half+16, 107, right, h-49)
		}
	case pageLogs:
		d.card(dc, x, 111, right, h-19)
	case pagePorts:
		for i := range desktopPorts {
			top := 111 + i*111
			d.card(dc, x, top, right, top+94)
		}
	}
}

func (d *desktop) paintGraph(dc uintptr, left, top, right, bottom int) {
	if right-left < 10 || bottom-top < 8 {
		return
	}
	maxRate := 1.0
	for _, point := range d.history {
		maxRate = max(maxRate, point.UpRate, point.DownRate)
	}
	for i := 1; i <= 3; i++ {
		y := top + (bottom-top)*i/3
		fill(dc, d.rect(left, y, right, y+1), d.palette.border)
	}
	for _, upload := range []bool{false, true} {
		var points [60]winPoint
		color := uintptr(downloadInk)
		if upload {
			color = uploadInk
		}
		for i, sample := range d.history {
			rate := sample.DownRate
			if upload {
				rate = sample.UpRate
			}
			x := left + (right-left)*i/(len(points)-1)
			y := bottom - int(float64(bottom-top)*rate/maxRate)
			points[i] = winPoint{int32(d.scale(x)), int32(d.scale(y))}
		}
		pen, _, _ := makePen.Call(0, uintptr(d.scale(2)), color)
		old, _, _ := selectObject.Call(dc, pen)
		gdi32.NewProc("Polyline").Call(dc, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
		selectObject.Call(dc, old)
		deleteObject.Call(pen)
	}
}

func (d *desktop) buttonStyle(item *drawItem) {
	id := int(item.ID)
	bg, border, color := d.palette.white, d.palette.border, uintptr(ink)
	parent := d.palette.white
	align := uintptr(1)
	rect := item.Area
	switch id {
	case idHomeTab, idRulesTab, idLogsTab, idPortsTab:
		parent = d.palette.side
		bg, border, color, align = d.palette.side, d.palette.side, 0xeee5d8, 0
		if id == [...]int{idHomeTab, idRulesTab, idLogsTab, idPortsTab}[d.page] {
			bg, border, color = d.palette.active, d.palette.active, 0xffffff
		}
	case idConnect, idWebAdd, idIPAdd, idSave, idBuiltinSave:
		bg, border, color = d.palette.blue, d.palette.blue, 0xffffff
	case idAdvanced, idBuiltins, idLogFolder, idPortsReset:
		parent = d.palette.canvas
	case idPortsSave:
		parent = d.palette.canvas
		bg, border, color = d.palette.blue, d.palette.blue, 0xffffff
	}
	if item.State&4 != 0 {
		bg, border, color = d.palette.disabled, d.palette.disabled, mutedInk
	} else if item.State&1 != 0 {
		bg, border, color = d.palette.navy, d.palette.navy, 0xffffff
	}
	fill(item.DC, rect, parent)
	rounded(item.DC, rect, border, d.scale(10))
	rounded(item.DC, winRect{rect.Left + 1, rect.Top + 1, rect.Right - 1, rect.Bottom - 1}, bg, d.scale(10))
	if align == 0 {
		rect.Left += int32(d.scale(15))
	}
	backgroundMode.Call(item.DC, 1)
	textColor.Call(item.DC, color)
	oldFont, _, _ := selectObject.Call(item.DC, d.font)
	label := getText(item.Window)
	drawText.Call(item.DC, uintptr(unsafe.Pointer(wide(label))), ^uintptr(0), uintptr(unsafe.Pointer(&rect)), 0x824|align)
	selectObject.Call(item.DC, oldFont)
	if item.State&0x10 != 0 && item.State&0x200 == 0 {
		rect = winRect{rect.Left + 4, rect.Top + 4, rect.Right - 4, rect.Bottom - 4}
		user32.NewProc("DrawFocusRect").Call(item.DC, uintptr(unsafe.Pointer(&rect)))
	}
}

func (d *desktop) colorControl(dc, hwnd uintptr) uintptr {
	brush, color := d.palette.white, uintptr(ink)
	switch d.controlIDs[hwnd] {
	case idTitle:
		brush, color = d.palette.side, 0xffffff
	case idPageTitle, idPageSubtitle, idPortsStatus:
		brush = d.palette.canvas
	case idCountryStatus, idExitCaption, idChosenExit, idTrafficTotal, idTrafficLabel, idHint, idWebEmpty, idIPEmpty:
		color = mutedInk
	case idDownload:
		color = downloadInk
	case idUpload:
		color = uploadInk
	case idState:
		if d.ready && d.confirmedExit {
			color = 0x675a07
		} else if d.ready {
			color = blueInk
		}
	case idLogText:
		brush = d.palette.sky
	case idDirectStatus:
		brush, color = d.palette.canvas, blueInk
	}
	if d.controlIDs[hwnd] == idPageSubtitle {
		color = mutedInk
	}
	backgroundMode.Call(dc, 1)
	textColor.Call(dc, color)
	return brush
}
