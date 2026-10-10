package agent

import (
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"hyperhand/internal/proto"
)

func TestControlsNativeSemanticScroll(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h := offscreenWindow(t, "hyperhand-semantic-scroll", 0, -30000)
	class, _ := windows.UTF16PtrFromString("LISTBOX")
	const wsChild, wsVisible, wsVScroll = 0x40000000, 0x10000000, 0x00200000
	list, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), 0,
		wsChild|wsVisible|wsVScroll, 10, 10, 200, 80, uintptr(h), 0, 0, 0)
	if list == 0 {
		t.Fatal(err)
	}
	send := user32.NewProc("SendMessageW")
	for i := 0; i < 40; i++ {
		text, _ := windows.UTF16PtrFromString(fmt.Sprintf("scroll-item-%02d", i))
		send.Call(list, 0x0180, 0, uintptr(unsafe.Pointer(text))) // LB_ADDSTRING
	}
	a := proto.ControlsArgs{Handle: uint64(h), PID: uint32(os.Getpid()), MaxDepth: 3, MaxNodes: 100}
	reply, err := pumpHelper(t, helperRequest{Controls: &a})
	if err != nil {
		t.Fatal(err)
	}
	var target *proto.ControlInfo
	for i, n := range reply.Controls.Nodes {
		if n.ControlType == 50008 {
			target = &reply.Controls.Nodes[i]
			break
		}
	}
	if target == nil || target.State == nil || target.State.VerticallyScrollable == nil || !*target.State.VerticallyScrollable || target.State.HorizontallyScrollable == nil || *target.State.HorizontallyScrollable {
		t.Fatalf("missing scroll-axis state: %+v", target)
	}
	if target.State.VerticalScrollPercent == nil || *target.State.VerticalScrollPercent != 0 || target.State.HorizontalScrollPercent != nil {
		t.Fatalf("initial percentages: %+v", target.State)
	}
	if !slices.Contains(target.Actions, "ScrollUp") || !slices.Contains(target.Actions, "ScrollDown") || slices.Contains(target.Actions, "ScrollLeft") || slices.Contains(target.Actions, "ScrollRight") {
		t.Fatalf("axis-filtered actions: %v", target.Actions)
	}
	act := proto.ControlActionArgs{Handle: a.Handle, PID: a.PID, RuntimeID: target.RuntimeID}
	act.Action = "ScrollDown"
	down, err := pumpHelper(t, helperRequest{Action: &act})
	if err != nil {
		t.Fatal(err)
	}
	if r := down.Action; r == nil || r.Verified == nil || !*r.Verified || r.State == nil || r.State.VerticalScrollPercent == nil || *r.State.VerticalScrollPercent <= 0 {
		t.Fatalf("ScrollDown result: %+v", r)
	}
	if got, _, _ := send.Call(list, 0x018E, 0, 0); got == 0 { // LB_GETTOPINDEX
		t.Fatal("ScrollDown did not move native list top index")
	}
	act.Action = "ScrollUp"
	up, err := pumpHelper(t, helperRequest{Action: &act})
	if err != nil {
		t.Fatal(err)
	}
	if r := up.Action; r == nil || r.Verified == nil || !*r.Verified || r.State == nil || r.State.VerticalScrollPercent == nil || *r.State.VerticalScrollPercent != 0 {
		t.Fatalf("ScrollUp result: %+v", r)
	}
	if got, _, _ := send.Call(list, 0x018E, 0, 0); got != 0 {
		t.Fatalf("ScrollUp native top index=%d, want 0", got)
	}
	boundary, err := pumpHelper(t, helperRequest{Action: &act})
	if err != nil {
		t.Fatal(err)
	}
	if r := boundary.Action; r == nil || r.Verified == nil || *r.Verified || r.State == nil || r.State.VerticalScrollPercent == nil || *r.State.VerticalScrollPercent != 0 {
		t.Fatalf("boundary must report unchanged state and false: %+v", r)
	}
	act.Action = "ScrollLeft"
	_, err = pumpHelper(t, helperRequest{Action: &act})
	if err == nil || !strings.Contains(err.Error(), "unsupported pattern: ScrollLeft;") || strings.Contains(err.Error(), ", ScrollRight") {
		t.Fatalf("unsupported horizontal axis: %v", err)
	}
	act.Action = "Invoke"
	_, err = pumpHelper(t, helperRequest{Action: &act})
	if err == nil || !strings.HasPrefix(err.Error(), "unsupported pattern: Invoke;") || strings.Contains(err.Error(), "ScrollLeft") || strings.Contains(err.Error(), "ScrollRight") || !strings.Contains(err.Error(), "ScrollDown") {
		t.Fatalf("non-scroll failure must report only supported axes: %v", err)
	}
}
