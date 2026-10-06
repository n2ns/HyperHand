package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows"
)

// The tray follows "The Taskbar - Taskbar Creation Notification" and the Shell_NotifyIcon documentation: it adds the
// icon at once without waiting for the taskbar, retries a failed NIM_ADD, adds the icon again whenever the taskbar
// broadcasts TaskbarCreated (explorer restart, or a DPI change on Windows 10), and selects NOTIFYICON_VERSION_4 after
// every NIM_ADD. The icon is identified by hWnd + uID, not a GUID, because GUID icons are bound to the executable path.

// Shell_NotifyIcon messages and NOTIFYICONDATA values from shellapi.h.
const (
	nimAdd, nimModify, nimDelete, nimSetVersion = 0, 1, 2, 4
	nifMessage, nifIcon, nifTip, nifShowTip     = 0x01, 0x02, 0x04, 0x80
	notifyIconVersion4                          = 4
	ninSelect                                   = 0x0400 // WM_USER
	ninKeySelect                                = ninSelect | 1
)

const (
	trayCallback   = co.WM_APP + 1 // the icon's uCallbackMessage
	trayTip        = co.WM_APP + 2 // posted by setTip from other goroutines
	trayRetryTimer = 1
	trayRetryMs    = 5000
	trayClass      = "HyperHandTray"
)

const (
	cmdSettings = iota + 1
	cmdRestart
	cmdQuit
)

// notifyIconData is NOTIFYICONDATAW. windigo's winsh.NOTIFYICONDATA cannot set uVersion, which NIM_SETVERSION needs.
type notifyIconData struct {
	CbSize           uint32
	HWnd             win.HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            win.HICON
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32 // union with uTimeout
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     win.HICON
}

var (
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	pShellNotifyIconW  = shell32.NewProc("Shell_NotifyIconW")
	pAppendMenuW       = user32.NewProc("AppendMenuW")
	trayWndProcPointer = syscall.NewCallback(trayWndProc)
)

func shellNotifyIcon(msg uint32, d *notifyIconData) bool {
	r, _, _ := pShellNotifyIconW.Call(uintptr(msg), uintptr(unsafe.Pointer(d)))
	return r != 0
}

// notifyIcon decides when to add the icon again. It runs on the tray thread; shell is Shell_NotifyIconW, or a fake in
// tests. Its methods report whether the retry timer should run.
type notifyIcon struct {
	data     notifyIconData // what every NIM_ADD sends: callback message, icon and tooltip
	shell    func(msg uint32, d *notifyIconData) bool
	added    bool // NIM_ADD succeeded and no TaskbarCreated arrived since
	tried    bool // a NIM_ADD was sent, so an icon may exist
	failures int  // failed NIM_ADDs since the icon was last added
}

func newNotifyIcon(hwnd win.HWND, icon win.HICON, tip string, shell func(uint32, *notifyIconData) bool) *notifyIcon {
	n := &notifyIcon{shell: shell}
	n.data.CbSize = uint32(unsafe.Sizeof(n.data))
	n.data.HWnd = hwnd
	n.data.UID = 1
	// NIF_SHOWTIP: version 4 suppresses the standard tooltip without it.
	n.data.UFlags = nifMessage | nifIcon | nifTip | nifShowTip
	n.data.UCallbackMessage = uint32(trayCallback)
	n.data.HIcon = icon
	n.data.UVersion = notifyIconVersion4
	n.setTipText(tip)
	return n
}

func (n *notifyIcon) setTipText(tip string) {
	u, _ := windows.UTF16FromString(tip)
	n.data.SzTip = [128]uint16{}
	copy(n.data.SzTip[:len(n.data.SzTip)-1], u)
}

// add sends NIM_ADD with the full data and then NIM_SETVERSION, which must follow every NIM_ADD.
func (n *notifyIcon) add() (retry bool) {
	if n.tried {
		// A NIM_ADD that timed out may still have taken effect, and the icon may survive a TaskbarCreated sent for
		// a DPI change; NIM_ADD fails for an icon that exists, so remove it first.
		n.shell(nimDelete, &n.data)
	}
	n.tried = true
	// Without version 4 the icon reports clicks in a format the tray does not handle, so that counts as a failure
	// too: the next retry deletes the icon and adds it again.
	if !n.shell(nimAdd, &n.data) || !n.shell(nimSetVersion, &n.data) {
		n.added = false
		n.failures++
		if n.failures == 1 {
			log.Printf("tray: adding the notification icon failed; retrying every %d s and when the taskbar is created", trayRetryMs/1000)
		}
		return true
	}
	if n.failures > 0 {
		log.Printf("tray: notification icon added after %d failed attempts", n.failures)
		n.failures = 0
	}
	n.added = true
	return false
}

// retry runs on the retry timer.
func (n *notifyIcon) retry() bool {
	if n.added {
		return false
	}
	return n.add()
}

// taskbarCreated runs when the taskbar broadcasts TaskbarCreated: any icon it had is assumed removed.
func (n *notifyIcon) taskbarCreated() bool {
	n.added = false
	return n.add()
}

// setTip changes the tooltip; a later NIM_ADD sends it too.
func (n *notifyIcon) setTip(tip string) {
	n.setTipText(tip)
	if n.added {
		n.shell(nimModify, &n.data) // on failure, the next TaskbarCreated adds it with this tooltip
	}
}

func (n *notifyIcon) remove() {
	if n.tried {
		n.shell(nimDelete, &n.data)
		n.added, n.tried = false, false
	}
}

// tray is the notification area icon, its menu and the hidden top-level window that owns them (a message-only
// window would not receive the TaskbarCreated broadcast). run runs on one locked OS thread; setTip and quit may be
// called from any goroutine.
type tray struct {
	info *trayInfo

	mu      sync.Mutex
	hwnd    win.HWND // the window while it exists
	tip     string
	stop    bool // quit was called
	restart bool

	// Tray thread only.
	icon           *notifyIcon
	menu           win.HMENU
	taskbarCreated co.WM
}

var theTray *tray // the tray of trayWndProc; there is one per process

// setTip sets the tooltip; before the window exists it becomes the initial tooltip.
func (t *tray) setTip(tip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tip = tip
	if t.hwnd != 0 {
		t.hwnd.PostMessage(trayTip, 0, 0)
	}
}

// quit ends run; with restart, run reports that a replacement should start.
func (t *tray) quit(restart bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stop = true
	t.restart = t.restart || restart
	if t.hwnd != 0 {
		t.hwnd.PostMessage(co.WM_CLOSE, 0, 0)
	}
}

// run shows the icon until quit and reports whether to restart. If the window cannot be created it logs why and
// returns at once.
func (t *tray) run() (restart bool) {
	runtime.LockOSThread() // the window and its message loop belong to this thread
	defer runtime.UnlockOSThread()
	hIcon, err := t.create()
	if hIcon != 0 {
		defer hIcon.DestroyIcon()
	}
	if t.menu != 0 {
		defer t.menu.DestroyMenu()
	}
	if err != nil {
		log.Print("tray: ", err)
	} else {
		var msg win.MSG
		for {
			r, err := win.GetMessage(&msg, 0, 0, 0)
			if err != nil {
				log.Print("tray: GetMessage: ", err)
				break
			}
			if r == 0 { // WM_QUIT
				break
			}
			win.TranslateMessage(&msg)
			win.DispatchMessage(&msg)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.restart
}

// create makes the menu and the hidden window and adds the icon.
func (t *tray) create() (win.HICON, error) {
	var err error
	if t.taskbarCreated, err = win.RegisterWindowMessage("TaskbarCreated"); err != nil {
		return 0, fmt.Errorf("RegisterWindowMessage: %w", err)
	}
	// CreateIconFromResourceEx takes the RT_ICON image, which the .ico holds at the offset in its one directory entry,
	// in a DWORD-aligned buffer; a fresh allocation is.
	ico := icon()
	bits := append([]byte(nil), ico[binary.LittleEndian.Uint32(ico[18:]):]...)
	hIcon, err := win.CreateIconFromResourceEx(bits, 0x00030000, win.SIZE{}, co.LR_DEFAULTCOLOR)
	if err != nil {
		return 0, fmt.Errorf("CreateIconFromResourceEx: %w", err)
	}
	if t.menu, err = win.CreatePopupMenu(); err != nil {
		return hIcon, fmt.Errorf("CreatePopupMenu: %w", err)
	}
	for _, item := range []struct {
		id   int
		text string
	}{{cmdSettings, "Settings..."}, {0, ""}, {cmdRestart, "Restart"}, {cmdQuit, "Quit"}} {
		const mfString, mfSeparator = 0x0000, 0x0800
		flags, text := uintptr(mfString), (*uint16)(nil)
		if item.id == 0 {
			flags = mfSeparator
		} else {
			text, _ = windows.UTF16PtrFromString(item.text)
		}
		if r, _, e := pAppendMenuW.Call(uintptr(t.menu), flags, uintptr(item.id), uintptr(unsafe.Pointer(text))); r == 0 {
			return hIcon, fmt.Errorf("AppendMenu: %w", e)
		}
	}
	inst, err := win.GetModuleHandle("")
	if err != nil {
		return hIcon, fmt.Errorf("GetModuleHandle: %w", err)
	}
	class, _ := windows.UTF16PtrFromString(trayClass)
	wc := win.WNDCLASSEX{LpfnWndProc: trayWndProcPointer, HInstance: inst, LpszClassName: class}
	if _, err := win.RegisterClassEx(&wc); err != nil {
		return hIcon, fmt.Errorf("RegisterClassEx: %w", err)
	}
	theTray = t
	// A top-level window without WS_VISIBLE: hidden, but it receives broadcasts.
	hwnd, err := win.CreateWindowEx(0, win.ClassNameStr(trayClass), "HyperHand", co.WS_OVERLAPPED,
		win.POINT{}, win.SIZE{}, 0, 0, inst, 0)
	if err != nil {
		return hIcon, fmt.Errorf("CreateWindowEx: %w", err)
	}
	t.mu.Lock()
	t.hwnd = hwnd
	tip, stop := t.tip, t.stop
	t.mu.Unlock()
	t.icon = newNotifyIcon(hwnd, hIcon, tip, shellNotifyIcon)
	if stop { // quit came before the window existed
		hwnd.DestroyWindow()
		return hIcon, nil
	}
	t.retryTimer(hwnd, t.icon.add())
	return hIcon, nil
}

func (t *tray) retryTimer(hwnd win.HWND, retry bool) {
	if retry {
		hwnd.SetTimer(trayRetryTimer, trayRetryMs) // replaces a running one
	} else {
		hwnd.KillTimer(trayRetryTimer)
	}
}

func trayWndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	if t := theTray; t != nil && t.icon != nil && t.handle(hwnd, co.WM(msg), wParam, lParam) {
		return 0
	}
	return hwnd.DefWindowProc(co.WM(msg), win.WPARAM(wParam), win.LPARAM(lParam))
}

func (t *tray) handle(hwnd win.HWND, msg co.WM, wParam, lParam uintptr) bool {
	switch msg {
	case t.taskbarCreated:
		t.retryTimer(hwnd, t.icon.taskbarCreated())
	case trayCallback:
		// Version 4: LOWORD(lParam) is the event; wParam holds the anchor point as GET_X_LPARAM / GET_Y_LPARAM.
		switch uint16(lParam) {
		case ninSelect, ninKeySelect: // left click, or Space / Enter on the selected icon
			showSettings(t.info)
		case uint16(co.WM_CONTEXTMENU): // right click, Shift+F10 or the menu key
			t.showMenu(hwnd, int(int16(wParam)), int(int16(wParam>>16)))
		}
	case trayTip:
		t.mu.Lock()
		tip := t.tip
		t.mu.Unlock()
		t.icon.setTip(tip)
	case co.WM_TIMER:
		if wParam != trayRetryTimer {
			return false
		}
		t.retryTimer(hwnd, t.icon.retry())
	case co.WM_DESTROY:
		hwnd.KillTimer(trayRetryTimer)
		t.icon.remove()
		t.mu.Lock()
		t.hwnd = 0
		t.mu.Unlock()
		win.PostQuitMessage(0)
	default:
		return false
	}
	return true
}

// showMenu shows the tray menu at (x, y) as TrackPopupMenu documents for notification icons: the window comes to
// the foreground first, so the menu closes when the user clicks elsewhere, and WM_NULL is posted afterwards.
func (t *tray) showMenu(hwnd win.HWND, x, y int) {
	hwnd.SetForegroundWindow()
	flags := co.TPM_RIGHTBUTTON | co.TPM_RETURNCMD | co.TPM_NONOTIFY
	if win.GetSystemMetrics(co.SM_MENUDROPALIGNMENT) != 0 {
		flags |= co.TPM_RIGHTALIGN
	}
	cmd, _ := t.menu.TrackPopupMenu(flags, x, y, hwnd) // 0 when cancelled
	hwnd.PostMessage(co.WM_NULL, 0, 0)
	switch cmd {
	case cmdSettings:
		showSettings(t.info)
	case cmdRestart:
		t.quit(true)
	case cmdQuit:
		t.quit(false)
	}
}
