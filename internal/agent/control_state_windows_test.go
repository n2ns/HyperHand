package agent

import (
	"encoding/json"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"hyperhand/internal/proto"
)

func TestControlsNativeSemanticState(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h := offscreenWindow(t, "hyperhand-semantic-state", 0, -30000)
	create := func(class, name string, style uintptr, y int) uintptr {
		t.Helper()
		cls, _ := windows.UTF16PtrFromString(class)
		text, _ := windows.UTF16PtrFromString(name)
		child, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(text)),
			0x40000000|0x10000000|style, 10, uintptr(y), 200, 25, uintptr(h), 0, 0, 0)
		if child == 0 {
			t.Fatal(err)
		}
		return child
	}
	send := user32.NewProc("SendMessageW")
	var toggleWindow uintptr
	for i, state := range []string{"off", "on", "indeterminate"} {
		check := create("BUTTON", "check-"+state, 6, i*25) // BS_AUTO3STATE
		send.Call(check, 0x00F1, uintptr(i), 0)            // BM_SETCHECK
		if i == 0 {
			toggleWindow = check
		}
	}
	create("EDIT", "read-only-value", 0x0800, 75) // ES_READONLY
	create("EDIT", "editable-value", 0, 100)
	create("EDIT", "semantic-password-secret", 0x0020, 125) // ES_PASSWORD
	list := create("LISTBOX", "", 1, 150)
	for _, name := range []string{"selected-item", "unselected-item"} {
		text, _ := windows.UTF16PtrFromString(name)
		send.Call(list, 0x0180, 0, uintptr(unsafe.Pointer(text))) // LB_ADDSTRING
	}
	send.Call(list, 0x0186, 0, 0) // LB_SETCURSEL
	a := proto.ControlsArgs{Handle: uint64(h), PID: uint32(os.Getpid()), MaxDepth: 3, MaxNodes: 50}
	reply, err := pumpHelper(t, helperRequest{Controls: &a})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(reply.Controls)
	if strings.Contains(string(data), "semantic-password-secret") {
		t.Fatal("password leaked through semantic snapshot")
	}
	seen := make(map[string]bool)
	for _, n := range reply.Controls.Nodes {
		if n.State == nil || n.State.Offscreen == nil {
			t.Errorf("missing readable offscreen state: %+v", n)
		}
		if strings.HasPrefix(n.Name, "check-") {
			want := strings.TrimPrefix(n.Name, "check-")
			if n.State == nil || n.State.Toggle == nil || *n.State.Toggle != want || !slices.Contains(n.Actions, "Toggle") {
				t.Errorf("toggle state/actions: %+v", n)
			}
			seen[n.Name] = true
		}
		if n.Value == "read-only-value" || n.Value == "editable-value" {
			want := n.Value == "read-only-value"
			if n.State == nil || n.State.ReadOnly == nil || *n.State.ReadOnly != want || !slices.Contains(n.Actions, "SetValue") {
				t.Errorf("edit state/actions: %+v", n)
			}
			if n.State != nil && (n.State.Toggle != nil || n.State.Selected != nil || n.State.ExpandCollapse != nil) {
				t.Errorf("unsupported state must remain absent: %+v", n.State)
			}
			seen[n.Value] = true
		}
		if n.Name == "selected-item" || n.Name == "unselected-item" {
			want := n.Name == "selected-item"
			if n.State == nil || n.State.Selected == nil || *n.State.Selected != want || !slices.Contains(n.Actions, "Select") {
				t.Errorf("selection state/actions: %+v", n)
			}
			seen[n.Name] = true
		}
	}
	for _, name := range []string{"check-off", "check-on", "check-indeterminate", "read-only-value", "editable-value", "selected-item", "unselected-item"} {
		if !seen[name] {
			t.Errorf("missing %s in snapshot: %s", name, data)
		}
	}
	for _, node := range reply.Controls.Nodes {
		act := proto.ControlActionArgs{Handle: a.Handle, PID: a.PID, RuntimeID: node.RuntimeID}
		switch {
		case node.Name == "check-off":
			act.Action = "Toggle"
			for i, want := range []string{"on", "indeterminate", "off"} {
				r, err := pumpHelper(t, helperRequest{Action: &act})
				if err != nil {
					t.Fatal(err)
				}
				if r.Action == nil || r.Action.Verified == nil || !*r.Action.Verified || r.Action.State == nil || r.Action.State.Toggle == nil || *r.Action.State.Toggle != want {
					t.Fatalf("Toggle want %s, got %+v", want, r.Action)
				}
				if got, _, _ := send.Call(toggleWindow, 0x00F0, 0, 0); got != uintptr((i+1)%3) { // BM_GETCHECK
					t.Fatalf("native toggle state=%d, want %d", got, (i+1)%3)
				}
			}
		case node.Name == "unselected-item":
			act.Action = "Select"
			r, err := pumpHelper(t, helperRequest{Action: &act})
			if err != nil {
				t.Fatal(err)
			}
			if r.Action == nil || r.Action.Verified == nil || !*r.Action.Verified || r.Action.State == nil || r.Action.State.Selected == nil || !*r.Action.State.Selected {
				t.Fatalf("Select result: %+v", r.Action)
			}
			if got, _, _ := send.Call(list, 0x0188, 0, 0); got != 1 { // LB_GETCURSEL
				t.Fatalf("native selected index=%d, want 1", got)
			}
		case node.Value == "editable-value":
			act.Action = "SetValue"
			r, err := pumpHelper(t, helperRequest{Action: &act})
			if err != nil {
				t.Fatal(err)
			}
			if r.Action == nil || r.Action.Verified == nil || !*r.Action.Verified || !r.Action.HasValue || r.Action.Value != "" {
				t.Fatalf("SetValue empty result: %+v", r.Action)
			}
		}
	}
}
