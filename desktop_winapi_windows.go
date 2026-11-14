package main

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

const (
	wmDestroy     = 0x0002
	wmSize        = 0x0005
	wmClose       = 0x0010
	wmCommand     = 0x0111
	wmTimer       = 0x0113
	wmSetFont     = 0x0030
	wmMinMax      = 0x0024
	wmSetText     = 0x000c
	wmSetIcon     = 0x0080
	wmColorStatic = 0x0138
	wmPaint       = 0x000f
	wmErase       = 0x0014
	wmDrawItem    = 0x002b
	wmEvent       = 0x8001
	wsChild       = 0x40000000
	wsVisible     = 0x10000000
	wsTab         = 0x00010000
	wsVScroll     = 0x00200000
	edgeClient    = 0x200
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	gdi32               = syscall.NewLazyDLL("gdi32.dll")
	registerClass       = user32.NewProc("RegisterClassExW")
	createWindow        = user32.NewProc("CreateWindowExW")
	defWindow           = user32.NewProc("DefWindowProcW")
	sendMessage         = user32.NewProc("SendMessageW")
	postMessage         = user32.NewProc("PostMessageW")
	destroyWindow       = user32.NewProc("DestroyWindow")
	showWindow          = user32.NewProc("ShowWindow")
	moveWindow          = user32.NewProc("MoveWindow")
	enableWindow        = user32.NewProc("EnableWindow")
	getMessage          = user32.NewProc("GetMessageW")
	isDialogMessage     = user32.NewProc("IsDialogMessageW")
	translateMessage    = user32.NewProc("TranslateMessage")
	dispatchMessage     = user32.NewProc("DispatchMessageW")
	postQuit            = user32.NewProc("PostQuitMessage")
	setFocus            = user32.NewProc("SetFocus")
	getClientRect       = user32.NewProc("GetClientRect")
	getWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	getWindowText       = user32.NewProc("GetWindowTextW")
	createFont          = gdi32.NewProc("CreateFontW")
	deleteObject        = gdi32.NewProc("DeleteObject")
	appWindow           *desktop
)

type winPoint struct{ X, Y int32 }
type winRect struct{ Left, Top, Right, Bottom int32 }

type windowClass struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}

type winMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          winPoint
	Private        uint32
}

type minMaxInfo struct {
	Reserved, MaxSize, MaxPosition, MinTrackSize, MaxTrackSize winPoint
}

func wide(text string) *uint16 { return syscall.StringToUTF16Ptr(text) }

func send(hwnd uintptr, message uint32, w, l uintptr) uintptr {
	result, _, _ := sendMessage.Call(hwnd, uintptr(message), w, l)
	return result
}

func setText(hwnd uintptr, text string) {
	sendMessage.Call(hwnd, wmSetText, 0, uintptr(unsafe.Pointer(wide(text))))
}

func getText(hwnd uintptr) string {
	length, _, _ := getWindowTextLength.Call(hwnd)
	if length == 0 || length > 1<<20 {
		return ""
	}
	buf := make([]uint16, length+1)
	getWindowText.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func addText(hwnd uintptr, message uint32, text string) {
	sendMessage.Call(hwnd, uintptr(message), 0, uintptr(unsafe.Pointer(wide(text))))
}

func move(hwnd uintptr, x, y, w, h int) {
	moveWindow.Call(hwnd, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 1)
}

func enable(hwnd uintptr, on bool) {
	value := uintptr(0)
	if on {
		value = 1
	}
	enableWindow.Call(hwnd, value)
}

func desktopFont(size, weight, dpi int, name string) uintptr {
	font, _, _ := createFont.Call(uintptr(-size*dpi/96), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(wide(name))))
	return font
}

func desktopProc(hwnd uintptr, message uint32, w, l uintptr) uintptr {
	d := appWindow
	if d != nil && d.window != 0 {
		switch message {
		case wmSize:
			d.layout(int(uint16(l)), int(uint16(l>>16)))
			return 0
		case wmMinMax:
			info := (*minMaxInfo)(unsafe.Pointer(l))
			info.MinTrackSize = winPoint{int32(d.scale(870)), int32(d.scale(690))}
			return 0
		case wmCommand:
			d.command(int(uint16(w)), int(uint16(w>>16)))
			return 0
		case wmEvent:
			d.receive()
			return 0
		case wmTimer:
			d.updateLog(false)
			recheckAfter := time.Minute
			if !d.confirmedExit {
				recheckAfter = 30 * time.Second
			}
			if d.ready && !d.switching && !d.lastExitProbe.IsZero() && time.Since(d.lastExitProbe) > recheckAfter {
				d.requestExit()
			}
			return 0
		case wmPaint:
			d.paint()
			return 0
		case wmErase:
			return 1
		case wmDrawItem:
			d.buttonStyle((*drawItem)(unsafe.Pointer(l)))
			return 1
		case wmColorStatic, 0x0132, 0x0133, 0x0134, 0x0135:
			return d.colorControl(w, l)
		case wmClose:
			d.closeWindow()
			return 0
		case wmDestroy:
			postQuit.Call(0)
			return 0
		}
	}
	result, _, _ := defWindow.Call(hwnd, uintptr(message), w, l)
	return result
}

func (d *desktop) makeWindow() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	appWindow = d
	defer func() { appWindow = nil }()
	// Use the monitor's real pixel metrics even when launched by a DPI-virtualized process.
	dpiContext := user32.NewProc("SetProcessDpiAwarenessContext")
	if err := dpiContext.Find(); err == nil {
		if ok, _, _ := dpiContext.Call(^uintptr(1)); ok == 0 { // DPI_AWARENESS_CONTEXT_SYSTEM_AWARE
			user32.NewProc("SetProcessDPIAware").Call()
		}
	} else {
		user32.NewProc("SetProcessDPIAware").Call()
	}
	dpi, _, _ := user32.NewProc("GetDpiForSystem").Call()
	d.dpi = max(96, int(dpi))
	d.instance, _, _ = kernel32.NewProc("GetModuleHandleW").Call(0)
	cursor, _, _ := user32.NewProc("LoadCursorW").Call(0, 32512)
	icon, _, _ := user32.NewProc("LoadIconW").Call(d.instance, 1)
	if icon == 0 {
		icon, _, _ = user32.NewProc("LoadIconW").Call(0, 32512)
	}
	class := windowClass{
		Size: uint32(unsafe.Sizeof(windowClass{})), WndProc: syscall.NewCallback(desktopProc),
		Instance: d.instance, Cursor: cursor, Icon: icon, SmallIcon: icon, Background: 6,
		Name: wide("URnetworkOneClickDesktop"),
	}
	ok, _, err := registerClass.Call(uintptr(unsafe.Pointer(&class)))
	if ok == 0 {
		return fmt.Errorf("注册桌面窗口：%w", err)
	}
	const defaultPosition = 0x80000000
	var workArea winRect
	user32.NewProc("SystemParametersInfoW").Call(48, 0, uintptr(unsafe.Pointer(&workArea)), 0) // SPI_GETWORKAREA
	width, height := d.scale(1064), d.scale(746)
	if workArea.Right > workArea.Left && workArea.Bottom > workArea.Top {
		width = min(width, int(workArea.Right-workArea.Left)-d.scale(12))
		height = min(height, int(workArea.Bottom-workArea.Top)-d.scale(12))
	}
	window, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(class.Name)), uintptr(unsafe.Pointer(wide("网络代理"))),
		0x00cf0000|0x02000000, defaultPosition, defaultPosition, uintptr(width), uintptr(height), 0, 0, d.instance, 0)
	if window == 0 {
		return fmt.Errorf("创建桌面窗口：%w", err)
	}
	d.window = window
	send(window, wmSetIcon, 1, icon)
	smallIcon, _, _ := user32.NewProc("LoadImageW").Call(d.instance, 1, 1, uintptr(d.scale(16)), uintptr(d.scale(16)), 0)
	if smallIcon == 0 {
		smallIcon = icon
	} else {
		defer user32.NewProc("DestroyIcon").Call(smallIcon)
	}
	send(window, wmSetIcon, 0, smallIcon)
	d.palette.init()
	defer d.palette.free()
	d.font = desktopFont(15, 400, d.dpi, "Microsoft YaHei UI")
	d.titleFont = desktopFont(28, 700, d.dpi, "Microsoft YaHei UI")
	d.brandFont = desktopFont(23, 700, d.dpi, "Segoe UI")
	d.valueFont = desktopFont(25, 650, d.dpi, "Segoe UI")
	d.monoFont = desktopFont(14, 400, d.dpi, "Consolas")
	defer deleteObject.Call(d.font)
	defer deleteObject.Call(d.titleFont)
	defer deleteObject.Call(d.monoFont)
	defer deleteObject.Call(d.brandFont)
	defer deleteObject.Call(d.valueFont)
	if err := d.buildControls(); err != nil {
		destroyWindow.Call(window)
		return err
	}
	var area winRect
	getClientRect.Call(window, uintptr(unsafe.Pointer(&area)))
	d.layout(int(area.Right), int(area.Bottom))
	showWindow.Call(window, 5)
	setFocus.Call(d.controls[idConnect])
	user32.NewProc("SetTimer").Call(window, 1, 1000, 0)
	d.refreshCountries()
	var msg winMessage
	for {
		result, _, err := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		switch int32(result) {
		case 0:
			return nil
		case -1:
			return err
		}
		if handled, _, _ := isDialogMessage.Call(window, uintptr(unsafe.Pointer(&msg))); handled == 0 {
			translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}
}

func (d *desktop) control(id int, kind, text string, style, extra uintptr) error {
	hwnd, _, err := createWindow.Call(extra, uintptr(unsafe.Pointer(wide(kind))), uintptr(unsafe.Pointer(wide(text))),
		wsChild|wsVisible|style, 0, 0, 0, 0, d.window, uintptr(id), d.instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("创建控件 %s：%w", kind, err)
	}
	d.controls[id] = hwnd
	d.controlIDs[hwnd] = id
	font := d.font
	switch id {
	case idPageTitle:
		font = d.titleFont
	case idTitle:
		font = d.brandFont
	case idExitIP, idDownload, idUpload:
		font = d.valueFont
	case idLogText:
		font = d.monoFont
	}
	send(hwnd, wmSetFont, font, 1)
	return nil
}
