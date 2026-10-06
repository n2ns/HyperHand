package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"hyperhand/internal/proto"
)

var (
	wtsapi32                     = windows.NewLazySystemDLL("wtsapi32.dll")
	pWTSQuerySessionInformationW = wtsapi32.NewProc("WTSQuerySessionInformationW")
	pWTSFreeMemory               = wtsapi32.NewProc("WTSFreeMemory")
	pWTSEnumerateProcessesW      = wtsapi32.NewProc("WTSEnumerateProcessesW")
	pOpenInputDesktop            = user32.NewProc("OpenInputDesktop")
	pCloseDesktop                = user32.NewProc("CloseDesktop")
)

// sessionState reports the agent's session state that decides whether the host may type an unlock password.
func sessionState(context.Context, json.RawMessage, []byte) (any, []byte, error) {
	var id uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &id); err != nil {
		return nil, nil, err
	}
	locked, err := sessionLocked(id)
	if err != nil {
		return nil, nil, err
	}
	secure, err := secureInputDesktop()
	if err != nil {
		return nil, nil, err
	}
	logonUI, err := processInSession("LogonUI.exe", id)
	if err != nil {
		return nil, nil, err
	}
	consent, err := processInSession("consent.exe", id)
	if err != nil {
		return nil, nil, err
	}
	return proto.SessionStateResult{
		Locked:        locked,
		Console:       windows.WTSGetActiveConsoleSessionId() == id,
		SecureDesktop: secure,
		LogonUI:       logonUI,
		Consent:       consent,
	}, nil, nil
}

// sessionLocked reads SessionFlags from WTSSessionInfoEx. WTSINFOEXW is Level (DWORD) and then WTSINFOEX_LEVEL1_W,
// which holds LARGE_INTEGERs and so starts at offset 8: SessionId at 8, SessionState at 12, SessionFlags at 16.
// WTS_SESSIONSTATE_LOCK is 0, WTS_SESSIONSTATE_UNLOCK 1, WTS_SESSIONSTATE_UNKNOWN -1.
func sessionLocked(id uint32) (bool, error) {
	const wtsSessionInfoEx = 25
	var buf unsafe.Pointer
	var n uint32
	r, _, err := pWTSQuerySessionInformationW.Call(0, uintptr(id), wtsSessionInfoEx, uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return false, fmt.Errorf("WTSQuerySessionInformation: %w", err)
	}
	defer pWTSFreeMemory.Call(uintptr(buf))
	if n < 20 {
		return false, errors.New("WTSQuerySessionInformation: short WTSINFOEX")
	}
	switch flags := *(*int32)(unsafe.Add(buf, 16)); flags {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("Windows does not report the session's lock state (SessionFlags %d)", flags)
	}
}

// secureInputDesktop reports whether keyboard input goes to a desktop this user cannot open: the Winlogon desktop of
// the sign-in screen's password box or of a UAC prompt. The lock screen curtain and the user's programs run on the
// Default desktop, which the user can open.
func secureInputDesktop() (bool, error) {
	h, _, err := pOpenInputDesktop.Call(0, 0, 0)
	if h != 0 {
		pCloseDesktop.Call(h)
		return false, nil
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return true, nil
	}
	return false, fmt.Errorf("OpenInputDesktop: %w", err)
}

// processInSession reports whether a process with this exe name runs in session id. WTSEnumerateProcesses gives every
// process's session without opening it; the agent is not elevated and cannot open LogonUI.exe or consent.exe.
func processInSession(exe string, id uint32) (bool, error) {
	var buf unsafe.Pointer
	var n uint32
	if r, _, err := pWTSEnumerateProcessesW.Call(0, 0, 1, uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n))); r == 0 {
		return false, fmt.Errorf("WTSEnumerateProcesses: %w", err)
	}
	defer pWTSFreeMemory.Call(uintptr(buf))
	for _, p := range unsafe.Slice((*wtsProcessInfo)(buf), n) {
		if p.SessionID == id && p.ProcessName != nil && strings.EqualFold(windows.UTF16PtrToString(p.ProcessName), exe) {
			return true, nil
		}
	}
	return false, nil
}

// wtsProcessInfo is WTS_PROCESS_INFOW.
type wtsProcessInfo struct {
	SessionID   uint32
	ProcessID   uint32
	ProcessName *uint16
	UserSid     *windows.SID
}
