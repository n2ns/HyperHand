package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"hyperhand/internal/proto"
)

var (
	pCreateWindowExW = user32.NewProc("CreateWindowExW")
	pDestroyWindow   = user32.NewProc("DestroyWindow")
	pEnableWindow    = user32.NewProc("EnableWindow")
)

// testWindow creates a visible, unactivated popup window ("STATIC" with SS_NOTIFY, so hit tests do not pass through it).
func testWindow(t *testing.T, title string, owner windows.HWND, exStyle uintptr, x, y, w, h int32) windows.HWND {
	t.Helper()
	const wsPopup, wsVisible, ssNotify = 0x80000000, 0x10000000, 0x100
	cls, _ := windows.UTF16PtrFromString("STATIC")
	name, _ := windows.UTF16PtrFromString(title)
	hw, _, err := pCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(name)),
		wsPopup|wsVisible|ssNotify, uintptr(x), uintptr(y), uintptr(w), uintptr(h), uintptr(owner), 0, 0, 0)
	if hw == 0 {
		t.Fatal(err)
	}
	t.Cleanup(func() { pDestroyWindow.Call(hw) })
	return windows.HWND(hw)
}

// offscreenWindow creates a visible, unactivated popup window far off screen so the test does not disturb the desktop.
func offscreenWindow(t *testing.T, title string, owner windows.HWND, x int32) windows.HWND {
	y := int32(-30000)
	t.Helper()
	const wsPopup, wsVisible, wsExNoActivate, wsExToolWindow = 0x80000000, 0x10000000, 0x08000000, 0x80
	cls, _ := windows.UTF16PtrFromString("STATIC")
	name, _ := windows.UTF16PtrFromString(title)
	h, _, err := pCreateWindowExW.Call(wsExNoActivate|wsExToolWindow, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(name)),
		wsPopup|wsVisible, uintptr(x), uintptr(y), 300, 200, uintptr(owner), 0, 0, 0)
	if h == 0 {
		t.Fatal(err)
	}
	t.Cleanup(func() { pDestroyWindow.Call(h) })
	return windows.HWND(h)
}

func TestListWindows(t *testing.T) {
	runtime.LockOSThread() // the windows belong to this thread
	defer runtime.UnlockOSThread()
	tag := fmt.Sprintf("hyperhand-test-%d", os.Getpid())
	owner := offscreenWindow(t, tag+"-owner", 0, -30000)
	dialog := offscreenWindow(t, tag+"-dialog", owner, -29000)
	pEnableWindow.Call(uintptr(owner), 0) // what a modal dialog does to its owner

	r, _, err := listWindows(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]proto.WindowInfo{}
	for _, w := range r.(proto.WindowsResult).Windows {
		if strings.HasPrefix(w.Title, tag) {
			got[w.Title] = w
		}
	}
	exe, _ := os.Executable()
	o, d := got[tag+"-owner"], got[tag+"-dialog"]
	if o.Handle != uint64(owner) || o.Enabled || o.Modal || o.Owner != 0 || o.Class != "Static" || o.PID != uint32(os.Getpid()) || !strings.EqualFold(o.Process, filepath.Base(exe)) {
		t.Errorf("owner: %+v", o)
	}
	if o.Rect != (proto.Rect{Left: -30000, Top: -30000, Right: -29700, Bottom: -29800}) {
		t.Errorf("owner rect: %+v", o.Rect)
	}
	if d.Handle != uint64(dialog) || !d.Enabled || !d.Modal || d.Owner != uint64(owner) {
		t.Errorf("dialog: %+v", d)
	}
}

func TestFocusWindowBadHandle(t *testing.T) {
	_, _, err := focusWindow(context.Background(), mustJSON(proto.TitleArgs{Handle: 1}), nil)
	if err == nil || !strings.Contains(err.Error(), "handle 1") {
		t.Fatalf("want a missing-handle error, got %v", err)
	}
}

func TestWindowAtOffScreen(t *testing.T) {
	r, _, err := windowAt(context.Background(), mustJSON(proto.PointArgs{X: -30000, Y: -30000}), nil)
	if err != nil || r.(proto.HandleResult).Handle != 0 {
		t.Fatalf("off screen: %+v %v", r, err)
	}
}

// A small always-on-top window on screen: window_at finds it at a point inside it, and not with x and y swapped.
func TestWindowAt(t *testing.T) {
	runtime.LockOSThread() // window_at sends WM_NCHITTEST to this thread's window
	defer runtime.UnlockOSThread()
	const wsExTopmost, wsExNoActivate, wsExToolWindow = 0x8, 0x08000000, 0x80
	h := testWindow(t, "hyperhand-test-hit", 0, wsExTopmost|wsExNoActivate|wsExToolWindow, 40, 300, 30, 30)
	for _, c := range []struct {
		x, y int
		want bool
	}{{55, 315, true}, {315, 55, false}} {
		r, _, err := windowAt(context.Background(), mustJSON(proto.PointArgs{X: c.x, Y: c.y}), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.(proto.HandleResult).Handle == uint64(h); got != c.want {
			t.Errorf("(%d, %d): handle %d, test window %d", c.x, c.y, r.(proto.HandleResult).Handle, h)
		}
	}
}

func TestMouseInputSize(t *testing.T) {
	if n := unsafe.Sizeof(mouseInput{}); n != 40 { // sizeof(INPUT) on 64-bit Windows; SendInput rejects other sizes
		t.Fatalf("mouseInput is %d bytes", n)
	}
}
