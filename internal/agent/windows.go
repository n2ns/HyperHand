package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"hyperhand/internal/proto"
)

var (
	dwmapi                     = windows.NewLazySystemDLL("dwmapi.dll")
	pDwmGetWindowAttribute     = dwmapi.NewProc("DwmGetWindowAttribute")
	pGetClassNameW             = user32.NewProc("GetClassNameW")
	pGetWindowRect             = user32.NewProc("GetWindowRect")
	pIsWindowEnabled           = user32.NewProc("IsWindowEnabled")
	pGetWindow                 = user32.NewProc("GetWindow")
	pQueryFullProcessImageName = kernel32.NewProc("QueryFullProcessImageNameW")
	pWindowFromPoint           = user32.NewProc("WindowFromPoint")
	pGetAncestor               = user32.NewProc("GetAncestor")
	pGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
)

const (
	gwOwner                  = 4
	gaRoot                   = 2
	smXVirtualScreen         = 76 // SM_XVIRTUALSCREEN, then SM_YVIRTUALSCREEN, SM_CXVIRTUALSCREEN, SM_CYVIRTUALSCREEN
	dwmwaExtendedFrameBounds = 9
	dwmwaCloaked             = 14
)

// One callback for all list_windows calls (callbacks are never freed), with its state guarded by listMu.
var (
	listMu  sync.Mutex
	listOut []windows.HWND
	listCB  = syscall.NewCallback(func(h windows.HWND, _ uintptr) uintptr {
		listOut = append(listOut, h)
		return 1
	})
)

// topWindows returns the visible, uncloaked top-level windows from the top of the Z order down.
func topWindows() []windows.HWND {
	listMu.Lock()
	defer listMu.Unlock()
	listOut = nil
	windows.EnumWindows(listCB, nil)
	var out []windows.HWND
	for _, h := range listOut {
		if windows.IsWindowVisible(h) && !cloaked(h) {
			out = append(out, h)
		}
	}
	return out
}

// cloaked: DWM hides the window (suspended store apps, windows on other virtual desktops).
func cloaked(h windows.HWND) bool {
	var v uint32
	r, _, _ := pDwmGetWindowAttribute.Call(uintptr(h), dwmwaCloaked, uintptr(unsafe.Pointer(&v)), unsafe.Sizeof(v))
	return r == 0 && v != 0
}

// windowRect is the visible frame (without the invisible resize borders), falling back to GetWindowRect.
func windowRect(h windows.HWND) proto.Rect {
	var r proto.Rect
	if hr, _, _ := pDwmGetWindowAttribute.Call(uintptr(h), dwmwaExtendedFrameBounds, uintptr(unsafe.Pointer(&r)), unsafe.Sizeof(r)); hr != 0 {
		pGetWindowRect.Call(uintptr(h), uintptr(unsafe.Pointer(&r)))
	}
	return r
}

func className(h windows.HWND) string {
	buf := make([]uint16, 256)
	n, _, _ := pGetClassNameW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func processName(pid uint32) string {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(p)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if r, _, _ := pQueryFullProcessImageName.Call(uintptr(p), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}

func enabled(h windows.HWND) bool {
	r, _, _ := pIsWindowEnabled.Call(uintptr(h))
	return r != 0
}

func windowInfo(h, fg windows.HWND, names map[uint32]string) proto.WindowInfo {
	var pid uint32
	windows.GetWindowThreadProcessId(h, &pid)
	name, ok := names[pid]
	if !ok {
		name = processName(pid)
		names[pid] = name
	}
	owner, _, _ := pGetWindow.Call(uintptr(h), gwOwner)
	iconic, _, _ := pIsIconic.Call(uintptr(h))
	return proto.WindowInfo{
		Handle:     uint64(h),
		Title:      windowText(h),
		Class:      className(h),
		PID:        pid,
		Process:    name,
		Rect:       windowRect(h),
		Enabled:    enabled(h),
		Foreground: h == fg,
		Minimized:  iconic != 0,
		Owner:      uint64(owner),
		Modal:      owner != 0 && !enabled(windows.HWND(owner)),
	}
}

func listWindows(context.Context, json.RawMessage, []byte) (any, []byte, error) {
	fg := windows.GetForegroundWindow()
	names := map[uint32]string{}
	r := proto.WindowsResult{Windows: []proto.WindowInfo{}}
	for _, h := range topWindows() {
		r.Windows = append(r.Windows, windowInfo(h, fg, names))
	}
	return r, nil, nil
}

// windowAt returns the top-level window under a screen point, which is the window a click there reaches.
func windowAt(_ context.Context, args json.RawMessage, _ []byte) (any, []byte, error) {
	var a proto.PointArgs
	if err := decode(args, &a); err != nil {
		return nil, nil, err
	}
	var m [4]int
	for i := range m {
		r, _, _ := pGetSystemMetrics.Call(uintptr(smXVirtualScreen + i))
		m[i] = int(int32(r))
	}
	if a.X < m[0] || a.Y < m[1] || a.X >= m[0]+m[2] || a.Y >= m[1]+m[3] {
		return proto.HandleResult{}, nil, nil
	}
	h, _, _ := pWindowFromPoint.Call(uintptr(uint32(int32(a.X))) | uintptr(uint32(int32(a.Y)))<<32)
	if h == 0 {
		return proto.HandleResult{}, nil, nil
	}
	root, _, _ := pGetAncestor.Call(h, gaRoot)
	return proto.HandleResult{Handle: uint64(root)}, nil, nil
}
