// Package tray shows a notification area icon with a menu, for the host tray and the guest agent.
//
// It follows "The Taskbar - Taskbar Creation Notification" and the Shell_NotifyIcon documentation: it adds the icon
// at once without waiting for the taskbar, retries a failed NIM_ADD, adds the icon again whenever the taskbar
// broadcasts TaskbarCreated (explorer restart, or a DPI change on Windows 10), and selects NOTIFYICON_VERSION_4 after
// every NIM_ADD. The icon is identified by hWnd + uID, not a GUID, because GUID icons are bound to the executable path.
package tray

import (
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
	trayTip        = co.WM_APP + 2 // posted by SetTip from other goroutines
	trayRetryTimer = 1
	trayRetryMs    = 5000
	trayClass      = "HyperHandTray"
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
	user32             = windows.NewLazySystemDLL("user32.dll")
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

// Item is a menu item; ID 0 is a separator.
type Item struct {
	ID       int
	Text     string
	Disabled bool
}

// Tray is the notification area icon and the hidden top-level window that owns it (a message-only window would not
// receive the TaskbarCreated broadcast). Run runs on one locked OS thread, which also calls OnSelect and OnCommand;
// SetTip, SetItems and Quit may be called from any goroutine. There is one per process.
type Tray struct {
	Icon      win.HICON    // the small icon to show; the caller destroys it after Run
	OnSelect  func()       // left click, or Space / Enter on the icon; nil shows the menu
	OnCommand func(id int) // a menu item was chosen

	mu    sync.Mutex
	hwnd  win.HWND // the window while it exists
	tip   string
	items []Item
	stop  bool // Quit was called

	// Tray thread only.
	icon           *notifyIcon
	taskbarCreated co.WM
}

var theTray *Tray // the tray of trayWndProc

// New makes a tray with an initial tooltip and right-click menu.
func New(tip string, items []Item) *Tray { return &Tray{tip: tip, items: items} }

// SetTip sets the tooltip; before the window exists it becomes the initial tooltip.
func (t *Tray) SetTip(tip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tip = tip
	if t.hwnd != 0 {
		t.hwnd.PostMessage(trayTip, 0, 0)
	}
}

// SetItems replaces the menu items; the menu shows them the next time it opens.
func (t *Tray) SetItems(items []Item) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.items = items
}

// Quit removes the icon and ends Run.
func (t *Tray) Quit() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stop = true
	if t.hwnd != 0 {
		t.hwnd.PostMessage(co.WM_CLOSE, 0, 0)
	}
}

// Run shows the icon until Quit. If the window cannot be created it returns the error at once.
func (t *Tray) Run() error {
	runtime.LockOSThread() // the window and its message loop belong to this thread
	defer runtime.UnlockOSThread()
	if err := t.create(); err != nil {
		return err
	}
	var msg win.MSG
	for {
		r, err := win.GetMessage(&msg, 0, 0, 0)
		if err != nil {
			return fmt.Errorf("GetMessage: %w", err)
		}
		if r == 0 { // WM_QUIT
			return nil
		}
		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}
}

// create makes the hidden window and adds the icon.
func (t *Tray) create() error {
	var err error
	if t.taskbarCreated, err = win.RegisterWindowMessage("TaskbarCreated"); err != nil {
		return fmt.Errorf("RegisterWindowMessage: %w", err)
	}
	inst, err := win.GetModuleHandle("")
	if err != nil {
		return fmt.Errorf("GetModuleHandle: %w", err)
	}
	class, _ := windows.UTF16PtrFromString(trayClass)
	wc := win.WNDCLASSEX{LpfnWndProc: trayWndProcPointer, HInstance: inst, LpszClassName: class}
	if _, err := win.RegisterClassEx(&wc); err != nil {
		return fmt.Errorf("RegisterClassEx: %w", err)
	}
	theTray = t
	// A top-level window without WS_VISIBLE: hidden, but it receives broadcasts.
	hwnd, err := win.CreateWindowEx(0, win.ClassNameStr(trayClass), "HyperHand", co.WS_OVERLAPPED,
		win.POINT{}, win.SIZE{}, 0, 0, inst, 0)
	if err != nil {
		return fmt.Errorf("CreateWindowEx: %w", err)
	}
	t.mu.Lock()
	t.hwnd = hwnd
	tip, stop := t.tip, t.stop
	t.mu.Unlock()
	t.icon = newNotifyIcon(hwnd, t.Icon, tip, shellNotifyIcon)
	if stop { // Quit came before the window existed
		hwnd.DestroyWindow()
		return nil
	}
	t.retryTimer(hwnd, t.icon.add())
	return nil
}

func (t *Tray) retryTimer(hwnd win.HWND, retry bool) {
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

func (t *Tray) handle(hwnd win.HWND, msg co.WM, wParam, lParam uintptr) bool {
	switch msg {
	case t.taskbarCreated:
		t.retryTimer(hwnd, t.icon.taskbarCreated())
	case trayCallback:
		// Version 4: LOWORD(lParam) is the event; wParam holds the anchor point as GET_X_LPARAM / GET_Y_LPARAM.
		switch uint16(lParam) {
		case ninSelect, ninKeySelect: // left click, or Space / Enter on the selected icon
			if t.OnSelect != nil {
				t.OnSelect()
			} else { // the anchor point is in wParam for these events too
				t.showMenu(hwnd, int(int16(wParam)), int(int16(wParam>>16)))
			}
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

// showMenu shows the current menu items at (x, y) as TrackPopupMenu documents for notification icons: the window
// comes to the foreground first, so the menu closes when the user clicks elsewhere, and WM_NULL is posted afterwards.
func (t *Tray) showMenu(hwnd win.HWND, x, y int) {
	t.mu.Lock()
	items := t.items
	t.mu.Unlock()
	menu, err := win.CreatePopupMenu()
	if err != nil {
		log.Print("tray: CreatePopupMenu: ", err)
		return
	}
	defer menu.DestroyMenu()
	for _, item := range items {
		const mfString, mfGrayed, mfSeparator = 0x0000, 0x0001, 0x0800
		flags, text := uintptr(mfString), (*uint16)(nil)
		switch {
		case item.ID == 0:
			flags = mfSeparator
		case item.Disabled:
			flags = mfString | mfGrayed
		}
		if item.ID != 0 {
			text, _ = windows.UTF16PtrFromString(item.Text)
		}
		if r, _, e := pAppendMenuW.Call(uintptr(menu), flags, uintptr(item.ID), uintptr(unsafe.Pointer(text))); r == 0 {
			log.Print("tray: AppendMenu: ", e)
			return
		}
	}
	hwnd.SetForegroundWindow()
	flags := co.TPM_RIGHTBUTTON | co.TPM_RETURNCMD | co.TPM_NONOTIFY
	if win.GetSystemMetrics(co.SM_MENUDROPALIGNMENT) != 0 {
		flags |= co.TPM_RIGHTALIGN
	}
	cmd, _ := menu.TrackPopupMenu(flags, x, y, hwnd) // 0 when cancelled
	hwnd.PostMessage(co.WM_NULL, 0, 0)
	if cmd != 0 && t.OnCommand != nil {
		t.OnCommand(cmd)
	}
}
