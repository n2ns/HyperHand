package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
	"hyperhand/internal/proto"
)

const keyUp = 2
const keyUnicode = 4

// INPUT's union is sized for MOUSEINPUT, with native pointer alignment.
type keyboardInput struct {
	VK, Scan    uint16
	Flags, Time uint32
	Extra       uintptr
}
type inputEvent struct {
	Type    uint32
	Key     keyboardInput
	Padding [8]byte
}

var pGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")

type inputBackend struct {
	validate func(proto.TypeKeysArgs) error
	pressed  func(uint16) bool
	send     func([]inputEvent) (int, error)
}

func typeKeys(ctx context.Context, args json.RawMessage, _ []byte) (any, []byte, error) {
	var a proto.TypeKeysArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, nil, err
	}
	r, err := typeKeysWith(ctx, a, inputBackend{validate: validateInputTarget, pressed: inputKeyPressed, send: sendInputEvents})
	return r, nil, err
}

func validateInputTarget(a proto.TypeKeysArgs) error {
	var id uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &id); err != nil {
		return err
	}
	locked, err := sessionLocked(id)
	if err != nil {
		return err
	}
	secure, err := secureInputDesktop()
	if err != nil {
		return err
	}
	if locked || secure {
		return errors.New("keyboard input requires an unlocked, non-secure desktop")
	}
	h := windows.HWND(a.Handle)
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(h, &pid); err != nil {
		return err
	}
	iconic, _, _ := pIsIconic.Call(uintptr(h))
	if pid != a.PID || windows.GetForegroundWindow() != h || !windows.IsWindowVisible(h) || !enabled(h) || iconic != 0 || cloaked(h) {
		return errors.New("keyboard input target is no longer the visible, enabled foreground window with the requested PID")
	}
	return nil
}

func inputKeyPressed(vk uint16) bool {
	r, _, _ := pGetAsyncKeyState.Call(uintptr(vk))
	return r&0x8000 != 0
}

func sendInputEvents(events []inputEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	n, _, err := pSendInput.Call(uintptr(len(events)), uintptr(unsafe.Pointer(&events[0])), unsafe.Sizeof(inputEvent{}))
	runtime.KeepAlive(events)
	if int(n) == len(events) {
		return int(n), nil
	}
	return int(n), err
}

func runeInputs(r rune) []inputEvent {
	var units []uint16
	var vk uint16
	if r == '\n' || r == '\r' {
		vk = 0x0d
	} else if r == '\t' {
		vk = 0x09
	}
	if vk != 0 {
		units = []uint16{0}
	} else {
		units = utf16.Encode([]rune{r})
	}
	var events []inputEvent
	for _, unit := range units {
		k := keyboardInput{VK: vk, Scan: unit}
		if vk == 0 {
			k.Flags = keyUnicode
		}
		events = append(events, inputEvent{Type: 1, Key: k})
		k.Flags |= keyUp
		events = append(events, inputEvent{Type: 1, Key: k})
	}
	return events
}

func typeKeysWith(ctx context.Context, a proto.TypeKeysArgs, b inputBackend) (proto.TypeKeysResult, error) {
	r := proto.TypeKeysResult{}
	if a.Handle == 0 || a.PID == 0 {
		return r, errors.New("keyboard input requires handle and PID")
	}
	if !utf8.ValidString(a.Text) {
		return r, errors.New("keyboard input requires valid UTF-8")
	}
	if len(a.Text) > 16*1024 {
		return r, errors.New("keyboard input text exceeds 16384 UTF-8 bytes")
	}
	fail := func(err error) (proto.TypeKeysResult, error) {
		return r, fmt.Errorf("keyboard input stopped after %d injected events; text may be partial, do not retry automatically: %w", r.Events, err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := b.validate(a); err != nil {
		return fail(err)
	}
	for _, ch := range strings.ReplaceAll(a.Text, "\r\n", "\n") {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := b.validate(a); err != nil {
			return fail(err)
		}
		events := runeInputs(ch)
		for _, vk := range []uint16{0x10, 0x11, 0x12, 0x5b, 0x5c, events[0].Key.VK} {
			if vk != 0 && b.pressed(vk) {
				return fail(fmt.Errorf("key 0x%02x is already held; release it before typing", vk))
			}
		}
		n, err := b.send(events)
		if n < 0 || n > len(events) {
			return fail(errors.New("SendInput returned an invalid event count"))
		}
		r.Events += n
		if n != len(events) {
			// Every batch consists of adjacent down/up pairs. Only an odd prefix
			// leaves our own last key down; never release unrelated user keys.
			cleanup := ""
			if n%2 != 0 {
				if released, _ := b.send(events[n : n+1]); released != 1 {
					cleanup = "; injected key release also failed"
				}
			}
			return fail(fmt.Errorf("SendInput inserted %d/%d events (UIPI may block input): %v%s", n, len(events), err, cleanup))
		}
	}
	return r, nil
}
