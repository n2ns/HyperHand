package tray

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"unsafe"

	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows"
)

type shellCall struct {
	msg  uint32
	data notifyIconData
}

// fakeShell stands in for Shell_NotifyIconW: NIM_ADD returns the queued results (then true), everything else true.
type fakeShell struct {
	calls    []shellCall
	adds     []bool
	versions []bool // queued NIM_SETVERSION results, then true
}

func (f *fakeShell) call(msg uint32, d *notifyIconData) bool {
	f.calls = append(f.calls, shellCall{msg, *d})
	if msg == nimAdd && len(f.adds) > 0 {
		ok := f.adds[0]
		f.adds = f.adds[1:]
		return ok
	}
	if msg == nimSetVersion && len(f.versions) > 0 {
		ok := f.versions[0]
		f.versions = f.versions[1:]
		return ok
	}
	return true
}

func (f *fakeShell) take() []shellCall {
	c := f.calls
	f.calls = nil
	return c
}

func msgs(calls []shellCall) []uint32 {
	var m []uint32
	for _, c := range calls {
		m = append(m, c.msg)
	}
	return m
}

func equal(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func tipOf(d notifyIconData) string { return windows.UTF16ToString(d.SzTip[:]) }

func newTestIcon(t *testing.T, adds ...bool) (*notifyIcon, *fakeShell, *bytes.Buffer) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	f := &fakeShell{adds: adds}
	return newNotifyIcon(win.HWND(0x1234), win.HICON(0x5678), "HyperHand", f.call), f, &logs
}

// checkFullAdd checks that an NIM_ADD carries the callback message, icon and tooltip, and is followed by NIM_SETVERSION 4.
func checkFullAdd(t *testing.T, calls []shellCall, tip string) {
	t.Helper()
	var add, ver *shellCall
	for i := range calls {
		switch calls[i].msg {
		case nimAdd:
			add = &calls[i]
		case nimSetVersion:
			ver = &calls[i]
		}
	}
	if add == nil || ver == nil {
		t.Fatalf("calls %v, want NIM_ADD then NIM_SETVERSION", msgs(calls))
	}
	d := add.data
	if d.UFlags != nifMessage|nifIcon|nifTip|nifShowTip || d.UCallbackMessage != uint32(trayCallback) ||
		d.HIcon != 0x5678 || d.HWnd != 0x1234 || d.UID != 1 || tipOf(d) != tip {
		t.Errorf("NIM_ADD data: flags %#x callback %#x icon %#x hwnd %#x uid %d tip %q", d.UFlags, d.UCallbackMessage, d.HIcon, d.HWnd, d.UID, tipOf(d))
	}
	if ver.data.UVersion != notifyIconVersion4 || ver.data.HWnd != 0x1234 || ver.data.UID != 1 {
		t.Errorf("NIM_SETVERSION data: version %d hwnd %#x uid %d", ver.data.UVersion, ver.data.HWnd, ver.data.UID)
	}
}

func TestNotifyIconDataSize(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) == 8 && unsafe.Sizeof(notifyIconData{}) != 976 {
		t.Errorf("sizeof(NOTIFYICONDATAW) = %d, want 976", unsafe.Sizeof(notifyIconData{}))
	}
}

func TestNotifyIconAddsAtOnce(t *testing.T) {
	n, f, _ := newTestIcon(t)
	if n.add() {
		t.Error("retry after a successful add")
	}
	calls := f.take()
	if !equal(msgs(calls), []uint32{nimAdd, nimSetVersion}) {
		t.Fatalf("calls %v", msgs(calls))
	}
	checkFullAdd(t, calls, "HyperHand")
	for range 3 {
		if n.retry() {
			t.Error("timer retries after success")
		}
	}
	if c := f.take(); len(c) != 0 {
		t.Errorf("timer sent %v after success", msgs(c))
	}
}

func TestNotifyIconRetriesOnTimer(t *testing.T) {
	n, f, logs := newTestIcon(t, false, false, true)
	if !n.add() {
		t.Fatal("no retry after a failed NIM_ADD")
	}
	if !equal(msgs(f.take()), []uint32{nimAdd}) {
		t.Fatal("a failed NIM_ADD must not be followed by NIM_SETVERSION")
	}
	if !n.retry() {
		t.Fatal("no retry after the second failure")
	}
	f.take()
	if n.retry() {
		t.Fatal("retry after the timer's NIM_ADD succeeded")
	}
	calls := f.take()
	// A timed-out NIM_ADD may have taken effect, so the retry removes any icon first.
	if !equal(msgs(calls), []uint32{nimDelete, nimAdd, nimSetVersion}) {
		t.Fatalf("calls %v", msgs(calls))
	}
	checkFullAdd(t, calls, "HyperHand")
	if n.retry() || len(f.take()) != 0 {
		t.Error("timer re-adds after success")
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "failed; retrying") || !strings.Contains(lines[1], "added after 2 failed attempts") {
		t.Errorf("log:\n%s", logs)
	}
}

func TestNotifyIconTaskbarCreatedReadds(t *testing.T) {
	n, f, _ := newTestIcon(t)
	n.add()
	f.take()
	if n.taskbarCreated() {
		t.Fatal("retry after a successful re-add")
	}
	calls := f.take()
	if !equal(msgs(calls), []uint32{nimDelete, nimAdd, nimSetVersion}) {
		t.Fatalf("calls %v", msgs(calls))
	}
	checkFullAdd(t, calls, "HyperHand")
	if n.retry() || len(f.take()) != 0 {
		t.Error("timer re-adds after TaskbarCreated")
	}
}

func TestNotifyIconTaskbarCreatedWhileFailing(t *testing.T) {
	n, f, _ := newTestIcon(t, false, true)
	n.add()
	f.take()
	if n.taskbarCreated() {
		t.Fatal("retry after TaskbarCreated added the icon")
	}
	checkFullAdd(t, f.take(), "HyperHand")
	if n.retry() {
		t.Error("timer still retries")
	}
}

func TestNotifyIconTaskbarCreatedFails(t *testing.T) {
	n, f, logs := newTestIcon(t, true, false, true)
	n.add()
	if !n.taskbarCreated() {
		t.Fatal("no retry after the re-add failed")
	}
	f.take()
	if n.retry() {
		t.Fatal("retry after the timer added the icon")
	}
	checkFullAdd(t, f.take(), "HyperHand")
	if !strings.Contains(logs.String(), "added after 1 failed attempts") {
		t.Errorf("log:\n%s", logs)
	}
}

func TestNotifyIconTipBeforeAdd(t *testing.T) {
	n, f, _ := newTestIcon(t, false, true)
	n.add()
	f.take()
	n.setTip("HyperHand: background service unavailable, see Settings")
	if c := f.take(); len(c) != 0 {
		t.Errorf("setTip before the icon exists sent %v", msgs(c))
	}
	n.retry()
	checkFullAdd(t, f.take(), "HyperHand: background service unavailable, see Settings")
}

func TestNotifyIconTipAfterAdd(t *testing.T) {
	n, f, _ := newTestIcon(t)
	n.add()
	f.take()
	n.setTip("changed")
	calls := f.take()
	if !equal(msgs(calls), []uint32{nimModify}) || tipOf(calls[0].data) != "changed" {
		t.Fatalf("calls %v", msgs(calls))
	}
	n.taskbarCreated()
	checkFullAdd(t, f.take(), "changed")
}

func TestNotifyIconTipTruncated(t *testing.T) {
	n, _, _ := newTestIcon(t)
	n.setTip(strings.Repeat("x", 200))
	if got := tipOf(n.data); len(got) != 127 {
		t.Errorf("tip length %d, want 127 plus the terminating null", len(got))
	}
}

func TestNotifyIconRemove(t *testing.T) {
	n, f, _ := newTestIcon(t)
	n.remove()
	if c := f.take(); len(c) != 0 {
		t.Errorf("remove before any add sent %v", msgs(c))
	}
	n.add()
	f.take()
	n.remove()
	if !equal(msgs(f.take()), []uint32{nimDelete}) {
		t.Error("remove did not send NIM_DELETE")
	}
}

// Without NIM_SETVERSION 4 the icon reports clicks in the old format, which the tray does not handle; it counts as
// not added, so the timer deletes and adds it again.
func TestNotifyIconSetVersionFailureRetries(t *testing.T) {
	n, f, _ := newTestIcon(t)
	f.versions = []bool{false}
	if !n.add() {
		t.Fatal("no retry after NIM_SETVERSION failed")
	}
	f.take()
	if n.retry() {
		t.Fatal("still retrying after a full add")
	}
	calls := f.take()
	if got := msgs(calls); !equal(got, []uint32{nimDelete, nimAdd, nimSetVersion}) {
		t.Fatalf("retry calls %v", got)
	}
	checkFullAdd(t, calls, "HyperHand")
}
