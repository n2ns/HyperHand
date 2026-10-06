package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"hyperhand/internal/proto"
)

func TestInputLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) == 8 && (unsafe.Sizeof(inputEvent{}) != 40 || unsafe.Offsetof(inputEvent{}.Key) != 8 || unsafe.Offsetof(keyboardInput{}.Extra) != 16) {
		t.Fatal("INPUT/KEYBDINPUT must match Windows amd64 ABI")
	}
}

func TestTypeKeysUnicodeAndControls(t *testing.T) {
	var got []inputEvent
	b := inputBackend{validate: func(proto.TypeKeysArgs) error { return nil }, pressed: func(uint16) bool { return false }, send: func(e []inputEvent) (int, error) { got = append(got, e...); return len(e), nil }}
	r, err := typeKeysWith(context.Background(), proto.TypeKeysArgs{Text: "A中😀\r\n\t", Handle: 1, PID: 2}, b)
	if err != nil || r.Events != 12 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	var scans []uint16
	for i := 0; i < len(got); i += 2 {
		if got[i].Key.Flags&keyUp != 0 || got[i+1].Key.Flags != got[i].Key.Flags|keyUp {
			t.Fatal("unbalanced key events")
		}
		scans = append(scans, got[i].Key.Scan)
	}
	if !reflect.DeepEqual(scans, []uint16{65, 0x4e2d, 0xd83d, 0xde00, 0, 0}) || got[8].Key.VK != 13 || got[10].Key.VK != 9 {
		t.Fatalf("events=%+v", got)
	}
}

func TestTypeKeysPartialSend(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3} {
		t.Run(string(rune('0'+n)), func(t *testing.T) {
			calls := 0
			b := inputBackend{validate: func(proto.TypeKeysArgs) error { return nil }, pressed: func(uint16) bool { return false }, send: func(e []inputEvent) (int, error) {
				calls++
				if calls == 1 {
					return n, errors.New("blocked")
				}
				if len(e) != 1 || e[0].Key.Flags&keyUp == 0 {
					t.Fatalf("cleanup released wrong events: %+v", e)
				}
				return 1, nil
			}}
			r, err := typeKeysWith(context.Background(), proto.TypeKeysArgs{Text: "😀more", Handle: 1, PID: 2}, b)
			if err == nil || !strings.Contains(err.Error(), "do not retry") || r.Events != n || calls != 1+n%2 {
				t.Fatalf("result=%+v err=%v calls=%d", r, err, calls)
			}
		})
	}
}

func TestTypeKeysStopsBeforeInput(t *testing.T) {
	for _, mode := range []string{"cancel", "target", "modifier", "handle", "pid"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := proto.TypeKeysArgs{Text: "abc", Handle: 1, PID: 2}
			if mode == "cancel" {
				cancel()
			}
			if mode == "handle" {
				a.Handle = 0
			}
			if mode == "pid" {
				a.PID = 0
			}
			b := inputBackend{validate: func(proto.TypeKeysArgs) error {
				if mode == "target" {
					return errors.New("changed")
				}
				return nil
			}, pressed: func(uint16) bool { return mode == "modifier" }, send: func([]inputEvent) (int, error) { t.Fatal("must not inject input"); return 0, nil }}
			if _, err := typeKeysWith(ctx, a, b); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestTypeKeysStopsBetweenCharacters(t *testing.T) {
	for _, mode := range []string{"cancel", "focus"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			b := inputBackend{validate: func(proto.TypeKeysArgs) error {
				if calls != 0 && mode == "focus" {
					return errors.New("focus changed")
				}
				return nil
			}, pressed: func(uint16) bool { return false }, send: func(e []inputEvent) (int, error) {
				calls++
				if mode == "cancel" {
					cancel()
				}
				return len(e), nil
			}}
			r, err := typeKeysWith(ctx, proto.TypeKeysArgs{Text: "ab", Handle: 1, PID: 2}, b)
			if err == nil || calls != 1 || r.Events != 2 {
				t.Fatalf("result=%+v err=%v calls=%d", r, err, calls)
			}
		})
	}
}
