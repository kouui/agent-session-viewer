package main

import (
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

// Win32 bits for the mini window (all called on the UI thread).
var (
	user32           = windows.NewLazySystemDLL("user32.dll")
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
)

const (
	gwlStyle      = ^uintptr(15) // -16
	gwlpWndProc   = ^uintptr(3)  // -4
	wsCaption     = 0x00C00000
	wsThickFrame  = 0x00040000
	wsSysMenu     = 0x00080000
	hwndTopmost   = ^uintptr(0) // -1
	hwndNoTopmost = ^uintptr(1) // -2
	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoActivate = 0x0010
	swpFrameChg   = 0x0020
	swpShow       = 0x0040
	swRestore     = 9
	wmSysCommand  = 0x0112
	wmNCLButton   = 0x00A1
	scMinimize    = 0xF020
	htCaption     = 2
	spiWorkArea   = 0x0030

	miniW, miniH = 330, 170 // in 96-dpi units, scaled by miniState.scale
)

type rect struct{ left, top, right, bottom int32 }

// miniState tracks the window between full and mini modes.
type miniState struct {
	w         webview2.WebView
	hwnd      uintptr
	oldProc   uintptr
	mini      bool
	fullRect  rect
	fullStyle uintptr
	miniRect  *rect   // remembered mini position
	scale     float64 // dpi / 96
}

func (m *miniState) enter() {
	if m.mini {
		return
	}
	m.mini = true
	pGetWindowRect.Call(m.hwnd, uintptr(unsafe.Pointer(&m.fullRect)))
	m.fullStyle, _, _ = pGetWindowLongW.Call(m.hwnd, gwlStyle)
	pShowWindow.Call(m.hwnd, swRestore) // in case it is already iconic
	pSetWindowLongW.Call(m.hwnd, gwlStyle, m.fullStyle&^(wsCaption|wsThickFrame|wsSysMenu))
	r := m.miniRect
	if r == nil {
		var wa rect
		pSysParamInfo.Call(spiWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0)
		mw, mh, pad := int32(miniW*m.scale), int32(miniH*m.scale), int32(16*m.scale)
		r = &rect{wa.right - mw - pad, wa.bottom - mh - pad, wa.right - pad, wa.bottom - pad}
	}
	pSetWindowPos.Call(m.hwnd, hwndTopmost, uintptr(r.left), uintptr(r.top), uintptr(float64(miniW)*m.scale), uintptr(float64(miniH)*m.scale), swpFrameChg|swpShow)
	m.w.Eval("setMini(true)")
}

func (m *miniState) exit() {
	if !m.mini {
		return
	}
	m.mini = false
	var cur rect
	pGetWindowRect.Call(m.hwnd, uintptr(unsafe.Pointer(&cur)))
	m.miniRect = &cur
	pSetWindowLongW.Call(m.hwnd, gwlStyle, m.fullStyle)
	f := m.fullRect
	pSetWindowPos.Call(m.hwnd, hwndNoTopmost, uintptr(f.left), uintptr(f.top), uintptr(f.right-f.left), uintptr(f.bottom-f.top), swpFrameChg|swpShow)
	pSetForeground.Call(m.hwnd)
	m.w.Eval("setMini(false)")
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
		log.Fatal(err)
	}
	go http.Serve(ln, srv.Handler())
	url := "http://" + ln.Addr().String() + "/"
	fmt.Println("gui:", url)

	data := os.Getenv("LOCALAPPDATA")
	if data != "" {
		data = filepath.Join(data, "agent-viewer", "webview2")
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath: data, AutoFocus: true,
		WindowOptions: webview2.WindowOptions{Title: "agent-viewer", Width: uint(1400 * scale), Height: uint(900 * scale), Center: true},
	})
	if w == nil {
		log.Fatal("cannot start WebView2 (is the WebView2 runtime installed?)")
	}
	defer w.Destroy()

	m := &miniState{w: w, hwnd: uintptr(w.Window()), scale: scale}
	// Intercept minimize: instead of iconizing, switch to the small always-on-top mini window.
	cb := windows.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		if msg == wmSysCommand && wp&0xFFF0 == scMinimize && !m.mini {
			m.enter()
			return 0
		}
		r, _, _ := pCallWindowProcW.Call(m.oldProc, hwnd, msg, wp, lp)
		return r
	})
	m.oldProc, _, _ = pSetWindowLongW.Call(m.hwnd, gwlpWndProc, cb)

	w.Bind("avEnterMini", func() { w.Dispatch(m.enter) })
	w.Bind("avRestore", func() { w.Dispatch(m.exit) })
	w.Bind("avQuit", func() { w.Dispatch(w.Terminate) })
	w.Bind("avDrag", func() { // start a native window drag from the page
		w.Dispatch(func() {
			pReleaseCapture.Call()
			pSendMessageW.Call(m.hwnd, wmNCLButton, htCaption, 0)
		})
	})
	w.Navigate(url)
	w.Run()
}
