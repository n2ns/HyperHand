package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

// groupWindows mirrors AutoCAD 2015 as observed: an untitled command line owned by the main window, a transient
// command history popup owned by the command line, and windows that must stay outside the main window's group.
func groupWindows(fg uint64) []proto.WindowInfo {
	ws := []proto.WindowInfo{
		{Handle: 22, PID: 100, Class: "HwndWrapper[hist]", Process: "acad.exe", Owner: 21, Enabled: true, Rect: proto.Rect{Left: 438, Top: 923, Right: 510, Bottom: 985}},
		{Handle: 21, PID: 100, Class: "HwndWrapper[cmd]", Process: "acad.exe", Owner: 20, Enabled: true, Rect: proto.Rect{Left: 384, Top: 984, Right: 1536, Bottom: 1011}},
		{Handle: 20, PID: 100, Title: "Autodesk AutoCAD 2015 - [Drawing1.dwg]", Class: "AfxMDIFrame110u", Process: "acad.exe", Enabled: true, Rect: proto.Rect{Right: 1920, Bottom: 1040}},
		{Handle: 24, PID: 300, Title: "Helper", Process: "other.exe", Owner: 20, Enabled: true}, // owned, other process
		{Handle: 25, PID: 100, Title: "Unowned", Process: "acad.exe", Enabled: true},            // same process, not owned
		{Handle: 26, PID: 100, Title: "Orphan", Process: "acad.exe", Owner: 99, Enabled: true},  // owner not listed
		{Handle: 40, PID: 100, Title: "Loop A", Process: "acad.exe", Owner: 41, Enabled: true},  // owner cycle
		{Handle: 41, PID: 100, Title: "Loop B", Process: "acad.exe", Owner: 40, Enabled: true},
		{Handle: 50, PID: 400, Title: "Autodesk Licensing", Class: "QWidget", Process: "LMU.exe", Enabled: true},
	}
	for i := range ws {
		ws[i].Foreground = ws[i].Handle == fg
	}
	return ws
}

func TestInGroup(t *testing.T) {
	ws := groupWindows(20)
	main := ws[2]
	for h, want := range map[uint64]bool{20: true, 21: true, 22: true, 24: false, 25: false, 26: false, 40: false, 50: false, 0: false, 999: false} {
		if got := inGroup(ws, main, h); got != want {
			t.Errorf("handle %d: in group %v, want %v", h, got, want)
		}
	}
	// Groups only go down the owner chain: the command line's group holds its popup but not the main window.
	if !inGroup(ws, ws[1], 22) || inGroup(ws, ws[1], 20) {
		t.Error("command line group")
	}
	// A cycle ends the walk instead of looping.
	if inGroup(ws, proto.WindowInfo{Handle: 7, PID: 100}, 40) {
		t.Error("cycle reached an unrelated root")
	}
}

func TestClickTargetGroupForeground(t *testing.T) {
	// The command line is foreground: clicks in the main window are allowed.
	ws := groupWindows(21)
	if x, y, err := clickTarget(ws, ws[2], 500, 500); err != nil || x != 500 || y != 500 {
		t.Errorf("group foreground: %d %d %v", x, y, err)
	}
	// Another process's window is foreground: refused, naming it and the next step.
	ws = groupWindows(50)
	_, _, err := clickTarget(ws, ws[2], 500, 500)
	if err == nil || !strings.Contains(err.Error(), "LMU.exe") || !strings.Contains(err.Error(), "vm_focus_window with handle 20") {
		t.Errorf("other foreground: %v", err)
	}
	// A same-process window that the main window does not own does not count either.
	ws = groupWindows(25)
	if _, _, err := clickTarget(ws, ws[2], 500, 500); err == nil {
		t.Error("unowned same-process foreground accepted")
	}
}

func TestDisabledTarget(t *testing.T) {
	ctx := context.Background()
	main := windowSelector{Handle: 20, PID: 100}
	// AutoCAD's licensing dialog (another process, unowned) disables the main window: click and input name it as the
	// window to act on, not the disabled main window.
	ws := groupWindows(50)
	ws[2].Enabled = false
	_, _, err := clickTarget(ws, ws[2], 1, 1)
	if err == nil || !strings.Contains(err.Error(), "probably blocks it; act on handle 50 first") {
		t.Errorf("click, other process: %v", err)
	}
	f := groupAgent(50, 0)
	f.results[proto.OpListWindows] = proto.WindowsResult{Windows: ws}
	if _, _, err := inputWindow(ctx, f.call, "", main); err == nil || !strings.Contains(err.Error(), "act on handle 50 first") {
		t.Errorf("input, other process: %v", err)
	}
	// Its own modal dialog in the foreground: named, and input is refused rather than sent to the dialog.
	ws = append(ws, proto.WindowInfo{Handle: 30, PID: 100, Title: "Customer Involvement Program", Process: "acad.exe", Owner: 20, Modal: true, Enabled: true})
	for i := range ws {
		ws[i].Foreground = ws[i].Handle == 30
	}
	if _, _, err := clickTarget(ws, ws[2], 1, 1); err == nil || !strings.Contains(err.Error(), "disabled by its modal dialog") || !strings.Contains(err.Error(), "handle 30") {
		t.Errorf("click, own dialog: %v", err)
	}
	f.results[proto.OpListWindows] = proto.WindowsResult{Windows: ws}
	if _, _, err := inputWindow(ctx, f.call, "", main); err == nil || !strings.Contains(err.Error(), "handle 30") {
		t.Errorf("input, own dialog: %v", err)
	}
	// The dialog itself, selected by its handle, takes input.
	if _, target, err := inputWindow(ctx, f.call, "", windowSelector{Handle: 30, PID: 100}); err != nil || target.Handle != 30 {
		t.Errorf("input to dialog: %+v %v", target, err)
	}
}

// An owner chain of exactly maxOwnerDepth links is in the group; one more link is not.
func TestInGroupDepth(t *testing.T) {
	var ws []proto.WindowInfo
	for h := uint64(1); h <= maxOwnerDepth+1; h++ {
		ws = append(ws, proto.WindowInfo{Handle: h, PID: 1, Owner: h + 1})
	}
	root := proto.WindowInfo{Handle: maxOwnerDepth + 2, PID: 1}
	ws = append(ws, root)
	if !inGroup(ws, root, 2) || inGroup(ws, root, 1) {
		t.Errorf("depth: %v %v", inGroup(ws, root, 2), inGroup(ws, root, 1))
	}
}

func TestCheckHitGroup(t *testing.T) {
	ws := groupWindows(20)
	main := ws[2]
	for _, h := range []uint64{21, 22} {
		hit, err := checkHit(ws, main, 900, 997, proto.HandleResult{Handle: h})
		if err != nil || hit.Handle != h || hit.Class == "" {
			t.Errorf("own window %d: %+v %v", h, hit, err)
		}
	}
	for _, h := range []uint64{24, 25, 50} {
		if _, err := checkHit(ws, main, 900, 997, proto.HandleResult{Handle: h}); err == nil || !strings.Contains(err.Error(), "covered by window") {
			t.Errorf("foreign window %d: %v", h, err)
		}
	}
	// A window list_windows does not show is described from window_at.
	_, err := checkHit(ws, main, 900, 997, proto.HandleResult{Handle: 131160, Class: "Shell_LightDismissOverlay", PID: 5488, Process: "explorer.exe"})
	if err == nil || !strings.Contains(err.Error(), "class Shell_LightDismissOverlay, explorer.exe") || !strings.Contains(err.Error(), "dismiss or close it") {
		t.Errorf("unlisted overlay: %v", err)
	}
	// Older agents only give the handle.
	if _, err := checkHit(ws, main, 900, 997, proto.HandleResult{Handle: 131160}); err == nil || !strings.Contains(err.Error(), "covered by window handle 131160") {
		t.Errorf("old agent: %v", err)
	}
}

func groupAgent(fg, at uint64) *fakeCall {
	return &fakeCall{results: map[string]any{
		proto.OpListWindows: proto.WindowsResult{Windows: groupWindows(fg)},
		proto.OpWindowAt:    proto.HandleResult{Handle: at},
	}}
}

func TestClickPointGroup(t *testing.T) {
	// Main window selector, point on the command line: allowed, and the command line is returned.
	x, y, hit, err := clickPoint(context.Background(), groupAgent(20, 21).call, clickIn{Handle: 20, PID: 100, X: 900, Y: 997})
	if err != nil || x != 900 || y != 997 || hit.Handle != 21 {
		t.Fatalf("command line: %d %d %+v %v", x, y, hit, err)
	}
	if !strings.Contains(windowLines(hit), "handle: 21\npid: 100\nclass: HwndWrapper[cmd]\nprocess: acad.exe") {
		t.Errorf("lines: %q", windowLines(hit))
	}
	// A new agent describes the hit window; the listed description wins for own windows, and window_at's is used for
	// a window list_windows does not show.
	f := groupAgent(20, 21)
	f.results[proto.OpWindowAt] = proto.HandleResult{Handle: 21, Class: "ignored", PID: 100, Process: "acad.exe"}
	if _, _, hit, err := clickPoint(context.Background(), f.call, clickIn{Handle: 20, X: 900, Y: 997}); err != nil || hit.Class != "HwndWrapper[cmd]" {
		t.Errorf("listed hit: %+v %v", hit, err)
	}
	f.results[proto.OpWindowAt] = proto.HandleResult{Handle: 131160, Class: "Shell_LightDismissOverlay", PID: 5488, Process: "explorer.exe"}
	if _, _, _, err := clickPoint(context.Background(), f.call, clickIn{Handle: 20, X: 900, Y: 997}); err == nil || !strings.Contains(err.Error(), "class Shell_LightDismissOverlay, explorer.exe") {
		t.Errorf("unlisted hit: %v", err)
	}
}

// inputMCP serves list_windows from windows(n), n counting list_windows calls, records keys and type_keys and pastes.
func inputMCP(t *testing.T, windows func(n int32) []proto.WindowInfo) (*mcp.ClientSession, *[]string, context.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	var lists atomic.Int32
	var mu sync.Mutex
	var log []string
	record := func(s string) { mu.Lock(); log = append(log, s); mu.Unlock() }
	b := &inputMCPBackend{windowMCPBackend: &windowMCPBackend{
		find: func(string) (hyperv.VM, error) { return hyperv.VM{ID: "A", Name: "A"}, nil },
		respond: func(_ string, req proto.Request) (any, error) {
			switch req.Op {
			case proto.OpListWindows:
				return proto.WindowsResult{Windows: windows(lists.Add(1))}, nil
			case proto.OpTypeKeys:
				var a proto.TypeKeysArgs
				if err := json.Unmarshal(req.Args, &a); err != nil {
					return nil, err
				}
				record(fmt.Sprintf("type_keys %d", a.Handle))
				return proto.TypeKeysResult{Events: 14}, nil
			case proto.OpClipboardSet:
				record("clipboard")
				return nil, nil
			}
			return nil, fmt.Errorf("unexpected operation %s", req.Op)
		},
	}, press: func(_, keys string) error { record("press " + keys); return nil }}
	return connectInputMCP(t, ctx, b), &log, ctx
}

func TestKeyAndPasteGroupTarget(t *testing.T) {
	// vm_key with the main window selector while the command line is foreground: sent, and the command line reported.
	cs, log, ctx := inputMCP(t, func(int32) []proto.WindowInfo { return groupWindows(21) })
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_key", Arguments: map[string]any{"handle": 20, "pid": 100, "sequence": []string{"a", "enter"}}})
	if err != nil || r.IsError || !strings.HasPrefix(resultText(r), "ok\nhandle: 21\n") || strings.Join(*log, ",") != "press a,press enter" {
		t.Errorf("vm_key: %v %v %v", r, err, *log)
	}
	// The foreground leaves the group mid-sequence: stopped before the second combination.
	cs, log, ctx = inputMCP(t, func(n int32) []proto.WindowInfo {
		if n > 1 {
			return groupWindows(50)
		}
		return groupWindows(21)
	})
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_key", Arguments: map[string]any{"handle": 20, "pid": 100, "sequence": []string{"a", "enter"}}})
	if err != nil || !r.IsError || !strings.Contains(resultText(r), "after 1 combinations") || strings.Join(*log, ",") != "press a" {
		t.Errorf("vm_key left group: %v %v %v", r, err, *log)
	}
	// Paste: rechecked after setting the clipboard, and the window foreground at the recheck is reported.
	cs, log, ctx = inputMCP(t, func(n int32) []proto.WindowInfo {
		if n > 1 {
			return groupWindows(22) // the history popup came up in between
		}
		return groupWindows(21)
	})
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_type", Arguments: map[string]any{"handle": 20, "pid": 100, "text": "x"}})
	if err != nil || r.IsError || !strings.HasPrefix(resultText(r), "ok\nhandle: 22\n") || strings.Join(*log, ",") != "clipboard,press ctrl+v" {
		t.Errorf("paste: %v %v %v", r, err, *log)
	}
	// Paste when the group lost the foreground after the clipboard was set: no paste.
	cs, log, ctx = inputMCP(t, func(n int32) []proto.WindowInfo {
		if n > 1 {
			return groupWindows(50)
		}
		return groupWindows(21)
	})
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_type", Arguments: map[string]any{"handle": 20, "pid": 100, "text": "x"}})
	if err != nil || !r.IsError || strings.Join(*log, ",") != "clipboard" {
		t.Errorf("paste after losing foreground: %v %v %v", r, err, *log)
	}
}

func TestInputWindowGroup(t *testing.T) {
	ctx := context.Background()
	main := windowSelector{Handle: 20, PID: 100}
	// The command line is foreground: input for the main window goes to it.
	root, target, err := inputWindow(ctx, groupAgent(21, 0).call, "", main)
	if err != nil || root.Handle != 20 || target.Handle != 21 {
		t.Errorf("group: %+v %+v %v", root, target, err)
	}
	// The selected window itself is foreground.
	if _, target, err := inputWindow(ctx, groupAgent(20, 0).call, "", main); err != nil || target.Handle != 20 {
		t.Errorf("self: %+v %v", target, err)
	}
	// Without a selector, the foreground window.
	if root, target, err := inputWindow(ctx, groupAgent(50, 0).call, "", windowSelector{}); err != nil || root.Handle != 50 || target.Handle != 50 {
		t.Errorf("no selector: %+v %+v %v", root, target, err)
	}
	// Another process in the foreground: refused with the next step.
	if _, _, err := inputWindow(ctx, groupAgent(50, 0).call, "", main); err == nil || !strings.Contains(err.Error(), "vm_focus_window") {
		t.Errorf("foreign foreground: %v", err)
	}
	// A minimized or disabled receiving window is refused.
	f := groupAgent(21, 0)
	ws := groupWindows(21)
	ws[1].Enabled = false
	f.results[proto.OpListWindows] = proto.WindowsResult{Windows: ws}
	if _, _, err := inputWindow(ctx, f.call, "", main); err == nil || !strings.Contains(err.Error(), "handle 21") {
		t.Errorf("disabled command line: %v", err)
	}
}

func TestTypeKeysReportsReceivingWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sent proto.TypeKeysArgs
	b := &inputMCPBackend{windowMCPBackend: &windowMCPBackend{
		find: func(string) (hyperv.VM, error) { return hyperv.VM{ID: "A", Name: "A"}, nil },
		respond: func(_ string, req proto.Request) (any, error) {
			switch req.Op {
			case proto.OpListWindows:
				return proto.WindowsResult{Windows: groupWindows(21)}, nil
			case proto.OpTypeKeys:
				if err := json.Unmarshal(req.Args, &sent); err != nil {
					return nil, err
				}
				return proto.TypeKeysResult{Events: 14}, nil
			}
			return nil, fmt.Errorf("unexpected operation %s", req.Op)
		},
	}}
	cs := connectInputMCP(t, ctx, b)
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_type", Arguments: map[string]any{"text": "(+ 1 2)", "mode": "keys", "handle": 20, "pid": 100}})
	if err != nil || r.IsError {
		t.Fatalf("%v %v", r, err)
	}
	if got := resultText(r); !strings.HasPrefix(got, "input events: 14\nhandle: 21\npid: 100\n") {
		t.Errorf("result %q", got)
	}
	if sent.Handle != 21 || sent.PID != 100 {
		t.Errorf("keys sent to %+v", sent)
	}
}
