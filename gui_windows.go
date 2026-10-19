package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// Win32 bits for the window modes (all called on the UI thread).
var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	pGetWindowLongW  = user32.NewProc("GetWindowLongPtrW")
	pSetWindowLongW  = user32.NewProc("SetWindowLongPtrW")
	pCallWindowProcW = user32.NewProc("CallWindowProcW")
	pSetWindowPos    = user32.NewProc("SetWindowPos")
	pGetWindowRect   = user32.NewProc("GetWindowRect")
	pShowWindow      = user32.NewProc("ShowWindow")
	pSysParamInfo    = user32.NewProc("SystemParametersInfoW")
	pReleaseCapture  = user32.NewProc("ReleaseCapture")
	pSendMessageW    = user32.NewProc("SendMessageW")
	pSetForeground   = user32.NewProc("SetForegroundWindow")
	pSetDPIAware     = user32.NewProc("SetProcessDPIAware")
	pGetDPIForSystem = user32.NewProc("GetDpiForSystem")
	pMonitorFromRect = user32.NewProc("MonitorFromRect")
	pMessageBoxW     = user32.NewProc("MessageBoxW")
	pMoveMemory      = kernel32.NewProc("RtlMoveMemory")
)

const (
	gwlStyle        = ^uintptr(15) // -16
	gwlpWndProc     = ^uintptr(3)  // -4
	wsCaption       = 0x00C00000
	wsThickFrame    = 0x00040000
	wsSysMenu       = 0x00080000
	wsMinimizeBox   = 0x00020000
	wsMaximizeBox   = 0x00010000
	hwndTopmost     = ^uintptr(0) // -1
	hwndNoTopmost   = ^uintptr(1) // -2
	swpFrameChg     = 0x0020
	swpShow         = 0x0040
	swRestore       = 9
	wmGetMinMaxInfo = 0x0024
	wmExitSizeMove  = 0x0232
	wmNCLButton     = 0x00A1
	htCaption       = 2
	spiWorkArea     = 0x0030
)

// Window sizes in 96-dpi units; multiplied by the DPI scale at runtime.
const (
	miniW, miniH     = 330, 170
	barW, barPad     = 380, 16
	barMinW, barMinH = 260, 300
)

type rect struct{ left, top, right, bottom int32 }

// jrect is the persisted form of a rect.
type jrect struct{ L, T, R, B int32 }

// winMgr switches the single native window between three modes:
//
//	full - normal resizable window
//	mini - small borderless always-on-top stats window
//	bar  - tall borderless always-on-top window, edge-resizable
//
// Each mode remembers its own geometry (persisted in window.json).
type winMgr struct {
	w         webview2.WebView
	hwnd      uintptr
	oldProc   uintptr
	scale     float64
	mode      string
	fullStyle uintptr
	geo       map[string]jrect
	geoFile   string
}

func (m *winMgr) workArea() rect {
	var wa rect
	pSysParamInfo.Call(spiWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0)
	return wa
}

func (m *winMgr) px(v int) int32 { return int32(float64(v) * m.scale) }

func (m *winMgr) curRect() rect {
	var r rect
	pGetWindowRect.Call(m.hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func (m *winMgr) load() {
	m.geo = map[string]jrect{}
	if b, err := os.ReadFile(m.geoFile); err == nil {
		json.Unmarshal(b, &m.geo)
	}
}

func (m *winMgr) save() {
	if m.geoFile == "" {
		return
	}
	r := m.curRect()
	m.geo[m.mode] = jrect{r.left, r.top, r.right, r.bottom}
	if b, err := json.Marshal(m.geo); err == nil {
		os.MkdirAll(filepath.Dir(m.geoFile), 0o755)
		os.WriteFile(m.geoFile, b, 0o644)
	}
}

// saved returns a remembered rect for mode if it is still on a monitor.
func (m *winMgr) saved(mode string) (rect, bool) {
	j, ok := m.geo[mode]
	if !ok || j.R-j.L < 100 || j.B-j.T < 80 {
		return rect{}, false
	}
	r := rect{j.L, j.T, j.R, j.B}
	if h, _, _ := pMonitorFromRect.Call(uintptr(unsafe.Pointer(&r)), 0); h == 0 {
		return rect{}, false // monitor layout changed
	}
	return r, true
}

func (m *winMgr) defaultRect(mode string) rect {
	wa := m.workArea()
	pad := m.px(barPad)
	switch mode {
	case "mini":
		w, h := m.px(miniW), m.px(miniH)
		return rect{wa.right - w - pad, wa.bottom - h - pad, wa.right - pad, wa.bottom - pad}
	case "bar":
		return rect{wa.right - m.px(barW) - pad, wa.top + pad, wa.right - pad, wa.bottom - pad}
	}
	return m.curRect()
}

func (m *winMgr) set(mode string) {
	if mode == m.mode || (mode != "full" && mode != "mini" && mode != "bar") {
		return
	}
	pShowWindow.Call(m.hwnd, swRestore) // leave maximized/iconic state first
	m.save()                            // remember the geometry of the mode we leave
	m.mode = mode
	style := m.fullStyle
	switch mode {
	case "mini":
		style &^= wsCaption | wsThickFrame | wsSysMenu | wsMinimizeBox | wsMaximizeBox
	case "bar": // keep the thick frame: it provides the native resize edges
		style = (style &^ (wsCaption | wsSysMenu | wsMinimizeBox | wsMaximizeBox)) | wsThickFrame
	}
	pSetWindowLongW.Call(m.hwnd, gwlStyle, style)
	r, ok := m.saved(mode)
	if !ok {
		r = m.defaultRect(mode)
	}
	if mode == "mini" { // fixed size
		r.right, r.bottom = r.left+m.px(miniW), r.top+m.px(miniH)
	}
	order := hwndTopmost
	if mode == "full" {
		order = hwndNoTopmost
	}
	pSetWindowPos.Call(m.hwnd, order, uintptr(r.left), uintptr(r.top), uintptr(r.right-r.left), uintptr(r.bottom-r.top), swpFrameChg|swpShow)
	if mode == "full" {
		pSetForeground.Call(m.hwnd)
	}
	m.w.Eval(fmt.Sprintf("setUIMode(%q)", mode))
}

func (m *winMgr) wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	r, _, _ := pCallWindowProcW.Call(m.oldProc, hwnd, msg, wp, lp)
	switch msg {
	case wmGetMinMaxInfo:
		if m.mode == "bar" { // MINMAXINFO.ptMinTrackSize = ints [6],[7]
			var mm [10]int32
			pMoveMemory.Call(uintptr(unsafe.Pointer(&mm[0])), lp, 40)
			mm[6], mm[7] = m.px(barMinW), m.px(barMinH)
			pMoveMemory.Call(lp, uintptr(unsafe.Pointer(&mm[0])), 40)
		}
	case wmExitSizeMove:
		m.save()
	}
	return r
}

// initLogging sends log output to a file when the exe has no console.
func initLogging() {
	if defaultGUI != "1" {
		return
	}
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		return
	}
	dir = filepath.Join(dir, "agent-viewer")
	os.MkdirAll(dir, 0o755)
	if f, err := os.OpenFile(filepath.Join(dir, "agent-viewer.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(f)
	}
}

// fatal reports an unrecoverable error: a message box in GUI mode, else stderr.
func fatal(msg string) {
	log.Print(msg)
	if guiMode {
		t, _ := windows.UTF16PtrFromString(msg)
		c, _ := windows.UTF16PtrFromString("agent-viewer")
		pMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x10)
	} else {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(1)
}

// runGUI serves the UI on a loopback port and shows it in an embedded
// WebView2 window; closing the window ends the process.
func runGUI(srv *Server, addr string, explicit bool) {
	runtime.LockOSThread() // the window and its message loop must stay on one thread
	// Per-process DPI awareness: crisp rendering, and window sizes are in real pixels.
	pSetDPIAware.Call()
	scale := 1.0
	if dpi, _, _ := pGetDPIForSystem.Call(); dpi >= 96 {
		scale = float64(dpi) / 96
	}
	if !explicit {
		// fixed default port keeps the page origin (and its saved settings) stable
		addr = "127.0.0.1:9181"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil && !explicit {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		fatal("cannot listen: " + err.Error())
	}
	go http.Serve(ln, srv.Handler())
	url := "http://" + ln.Addr().String() + "/"
	fmt.Println("gui:", url)

	dir := os.Getenv("LOCALAPPDATA")
	data := ""
	if dir != "" {
		dir = filepath.Join(dir, "agent-viewer")
		data = filepath.Join(dir, "webview2")
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath: data, AutoFocus: true,
		WindowOptions: webview2.WindowOptions{Title: "agent-viewer", Width: uint(1400 * scale), Height: uint(900 * scale), Center: true},
	})
	if w == nil {
		fatal("Cannot start WebView2. Install the Microsoft Edge WebView2 Runtime (preinstalled on Windows 11).")
	}
	defer w.Destroy()

	m := &winMgr{w: w, hwnd: uintptr(w.Window()), scale: scale, mode: "full"}
	if dir != "" {
		m.geoFile = filepath.Join(dir, "window.json")
	}
	m.load()
	m.fullStyle, _, _ = pGetWindowLongW.Call(m.hwnd, gwlStyle)
	if r, ok := m.saved("full"); ok {
		pSetWindowPos.Call(m.hwnd, hwndNoTopmost, uintptr(r.left), uintptr(r.top), uintptr(r.right-r.left), uintptr(r.bottom-r.top), swpShow)
	}
	m.oldProc, _, _ = pSetWindowLongW.Call(m.hwnd, gwlpWndProc, windows.NewCallback(m.wndProc))

	w.Bind("avSetMode", func(mode string) { w.Dispatch(func() { m.set(mode) }) })
	w.Bind("avQuit", func() { w.Dispatch(func() { m.save(); w.Terminate() }) })
	w.Bind("avDrag", func() { // start a native window drag from the page
		w.Dispatch(func() {
			pReleaseCapture.Call()
			pSendMessageW.Call(m.hwnd, wmNCLButton, htCaption, 0)
		})
	})
	w.Navigate(url)
	w.Run()
	m.save()
}
