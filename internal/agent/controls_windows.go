package agent

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
	"hyperhand/internal/proto"
)

// Slots follow Microsoft's UIAutomationClient.h (IUIAutomation, IUIAutomationElement,
// IUIAutomationTreeWalker); only read-only methods are exposed here.
const (
	uiaElementFromHandle = 6
	uiaControlViewWalker = 14
	uiaFirstChild        = 4
	uiaNextSibling       = 6
	uiaProcessID         = 20
	uiaControlType       = 21
	uiaName              = 23
	uiaEnabled           = 28
	uiaAutomationID      = 29
	uiaClassName         = 30
	uiaPassword          = 35
	uiaOffscreen         = 38
	uiaBounds            = 43
)

//go:uintptrescapes
func uiaCall(obj *ole.IUnknown, slot int, args ...uintptr) error {
	method := *(*uintptr)(unsafe.Add(unsafe.Pointer(obj.RawVTable), uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	params := append([]uintptr{uintptr(unsafe.Pointer(obj))}, args...)
	hr, _, _ := syscall.SyscallN(method, params...)
	runtime.KeepAlive(obj)
	if int32(hr) < 0 {
		return fmt.Errorf("UI Automation method %d: HRESULT 0x%08x", slot, uint32(hr))
	}
	return nil
}

func validateControlsWindow(a proto.ControlsArgs) error {
	if uint64(uintptr(a.Handle)) != a.Handle {
		return errors.New("window handle exceeds native range")
	}
	h := windows.HWND(a.Handle)
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(h, &pid); err != nil {
		return fmt.Errorf("controls window: %w", err)
	}
	desktop, _, _ := user32.NewProc("GetDesktopWindow").Call()
	shell, _, _ := user32.NewProc("GetShellWindow").Call()
	root, _, _ := pGetAncestor.Call(uintptr(h), 2)
	if pid != a.PID || root != uintptr(h) || uintptr(h) == desktop || uintptr(h) == shell || !windows.IsWindowVisible(h) || cloaked(h) {
		return errors.New("controls require the specified visible top-level window and matching PID; desktop roots are not allowed")
	}
	return nil
}

// UIA reports physical screen coordinates. The helper owns no UI, and every COM call,
// including final Release and CoUninitialize, runs in this same MTA apartment.
func collectControls(a proto.ControlsArgs) (proto.ControlsResult, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := validateControlsWindow(a); err != nil {
		return proto.ControlsResult{}, err
	}
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		var oe *ole.OleError
		if !errors.As(err, &oe) || oe.Code() != 1 {
			return proto.ControlsResult{}, err
		}
	}
	defer ole.CoUninitialize()
	automation, err := ole.CreateInstance(ole.NewGUID("{FF48DBA4-60EF-4201-AA87-54103EEF594E}"), ole.NewGUID("{30CBE57D-D9D0-452A-AB13-7AC5AC4825EE}"))
	if err != nil {
		return proto.ControlsResult{}, err
	}
	defer automation.Release()
	var root, walker *ole.IUnknown
	if err := uiaCall(automation, uiaElementFromHandle, uintptr(a.Handle), uintptr(unsafe.Pointer(&root))); err != nil {
		return proto.ControlsResult{}, err
	}
	if root == nil {
		return proto.ControlsResult{}, errors.New("UI Automation returned no window element")
	}
	defer root.Release()
	if err := uiaCall(automation, uiaControlViewWalker, uintptr(unsafe.Pointer(&walker))); err != nil {
		return proto.ControlsResult{}, err
	}
	if walker == nil {
		return proto.ControlsResult{}, errors.New("UI Automation returned no control-view walker")
	}
	defer walker.Release()
	e := &nativeControl{element: root, walker: walker}
	pid, err := e.integer(uiaProcessID)
	if err != nil {
		return proto.ControlsResult{}, err
	}
	if uint32(pid) != a.PID {
		return proto.ControlsResult{}, errors.New("UI Automation root PID does not match the requested window")
	}
	r, err := walkControls(e, a)
	if err != nil {
		return proto.ControlsResult{}, err
	}
	if err := validateControlsWindow(a); err != nil {
		return proto.ControlsResult{}, err
	}
	return r, nil
}

type nativeControl struct{ element, walker *ole.IUnknown }

func (e *nativeControl) release() { e.element.Release() }
func (e *nativeControl) relative(slot int) (controlElement, error) {
	var out *ole.IUnknown
	if err := uiaCall(e.walker, slot, uintptr(unsafe.Pointer(e.element)), uintptr(unsafe.Pointer(&out))); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return &nativeControl{element: out, walker: e.walker}, nil
}
func (e *nativeControl) first() (controlElement, error) { return e.relative(uiaFirstChild) }
func (e *nativeControl) next() (controlElement, error)  { return e.relative(uiaNextSibling) }
func (e *nativeControl) integer(slot int) (int32, error) {
	var out int32
	err := uiaCall(e.element, slot, uintptr(unsafe.Pointer(&out)))
	return out, err
}
func (e *nativeControl) text(slot int) (string, bool, error) {
	var out *int16
	err := uiaCall(e.element, slot, uintptr(unsafe.Pointer(&out)))
	if out != nil {
		defer ole.SysFreeString(out)
	}
	if err != nil {
		return "", false, err
	}
	if out == nil {
		return "", false, nil
	}
	n := ole.SysStringLen(out)
	cut := n > 512
	if cut {
		n = 512
	}
	return string(utf16.Decode(unsafe.Slice((*uint16)(unsafe.Pointer(out)), int(n)))), cut, nil
}
func (e *nativeControl) info() (proto.ControlInfo, bool, bool, error) {
	var n proto.ControlInfo
	password, err := e.integer(uiaPassword)
	if err != nil {
		return n, false, false, err
	}
	pid, err := e.integer(uiaProcessID)
	if err != nil {
		return n, false, false, err
	}
	n.PID = uint32(pid)
	n.ControlType, err = e.integer(uiaControlType)
	if err != nil {
		return n, false, false, err
	}
	on, err := e.integer(uiaEnabled)
	if err != nil {
		return n, false, false, err
	}
	n.Enabled = on != 0
	off, err := e.integer(uiaOffscreen)
	if err != nil {
		return n, false, false, err
	}
	n.Offscreen = off != 0
	if err := uiaCall(e.element, uiaBounds, uintptr(unsafe.Pointer(&n.Rect))); err != nil {
		return n, false, false, err
	}
	cut := false
	fields := []struct {
		slot   int
		target *string
	}{{uiaAutomationID, &n.AutomationID}, {uiaClassName, &n.ClassName}}
	if password == 0 {
		fields = append(fields, struct {
			slot   int
			target *string
		}{uiaName, &n.Name})
	}
	for _, f := range fields {
		value, shortened, err := e.text(f.slot)
		if err != nil {
			return n, false, false, err
		}
		*f.target, cut = value, cut || shortened
	}
	return n, password != 0, cut, nil
}
