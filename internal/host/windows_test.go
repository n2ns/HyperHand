package host

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/proto"
)

func testWindows() []proto.WindowInfo {
	return []proto.WindowInfo{
		{Handle: 10, Title: "Options", Process: "acad.exe", Rect: proto.Rect{Left: 100, Top: 50, Right: 600, Bottom: 450}, Enabled: true, Foreground: true, Owner: 20, Modal: true},
		{Handle: 20, Title: "Autodesk AutoCAD 2015 - [a.dwg]", Process: "acad.exe", Rect: proto.Rect{Right: 1920, Bottom: 1040}},
		{Handle: 30, Title: "Autodesk AutoCAD 2015 - [b.dwg]", Process: "acad.exe", Rect: proto.Rect{Right: 1920, Bottom: 1040}, Enabled: true},
	}
}

func TestResolveWindow(t *testing.T) {
	ws := testWindows()
	if w, err := resolveWindow(ws, "options", 0); err != nil || w.Handle != 10 {
		t.Errorf("title: %+v %v", w, err)
	}
	if w, err := resolveWindow(ws, "ignored", 30); err != nil || w.Handle != 30 {
		t.Errorf("handle: %+v %v", w, err)
	}
	if _, err := resolveWindow(ws, "", 99); err == nil || !strings.Contains(err.Error(), "handle 99") {
		t.Errorf("missing handle: %v", err)
	}
	if _, err := resolveWindow(ws, "notepad", 0); err == nil {
		t.Error("no match: want error")
	}
	_, err := resolveWindow(ws, "AUTOCAD", 0)
	if err == nil || !strings.Contains(err.Error(), "handle 20") || !strings.Contains(err.Error(), "handle 30") {
		t.Errorf("ambiguous title must list both handles: %v", err)
	}
}

func TestClickTarget(t *testing.T) {
	ws := testWindows()
	if x, y, err := clickTarget(ws, ws[0], 10, 20); err != nil || x != 110 || y != 70 {
		t.Errorf("inside: %d %d %v", x, y, err)
	}
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {500, 0}, {0, 400}} {
		if _, _, err := clickTarget(ws, ws[0], p[0], p[1]); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Errorf("%v: %v", p, err)
		}
	}
	// Not foreground: the error names the actual foreground window.
	if _, _, err := clickTarget(ws, ws[2], 10, 10); err == nil || !strings.Contains(err.Error(), `"Options" (handle 10`) {
		t.Errorf("background: %v", err)
	}
	// Foreground but disabled by a modal dialog.
	w := ws[1]
	w.Foreground = true
	if _, _, err := clickTarget(ws, w, 10, 10); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("disabled: %v", err)
	}
}

func TestCheckHit(t *testing.T) {
	ws := testWindows()
	if err := checkHit(ws, ws[0], 110, 70, 10); err != nil {
		t.Errorf("hit: %v", err)
	}
	if err := checkHit(ws, ws[0], 110, 70, 30); err == nil || !strings.Contains(err.Error(), "covered by window") || !strings.Contains(err.Error(), "handle 30") {
		t.Errorf("covered: %v", err)
	}
	if err := checkHit(ws, ws[0], 110, 70, 0); err == nil || !strings.Contains(err.Error(), "off screen") {
		t.Errorf("off screen: %v", err)
	}
}

// fakeCall answers ops from canned results (JSON-encoded) or errors and records the ops it was asked.
type fakeCall struct {
	results map[string]any
	errs    map[string]error
	ops     []string
	args    []any
}

func (f *fakeCall) call(_ context.Context, _, op string, args any, _ []byte, result any) ([]byte, error) {
	f.ops, f.args = append(f.ops, op), append(f.args, args)
	if err := f.errs[op]; err != nil {
		return nil, err
	}
	if r, ok := f.results[op]; ok && result != nil {
		b, _ := json.Marshal(r)
		return nil, json.Unmarshal(b, result)
	}
	return nil, nil
}

func oldAgent() *fakeCall {
	return &fakeCall{
		errs:    map[string]error{proto.OpListWindows: errors.New(`unknown op "list_windows"`), proto.OpWindowAt: errors.New(`unknown op "window_at"`)},
		results: map[string]any{proto.OpFocusWindow: proto.TextResult{Text: "Notepad"}},
	}
}

func newAgent(at uint64) *fakeCall {
	return &fakeCall{results: map[string]any{
		proto.OpListWindows: proto.WindowsResult{Windows: testWindows()},
		proto.OpWindowAt:    proto.HandleResult{Handle: at},
		proto.OpFocusWindow: proto.FocusResult{Text: "Options", Handle: 10},
	}}
}

func resultText(r *mcp.CallToolResult) string { return r.Content[0].(*mcp.TextContent).Text }

func TestToolsOldAgent(t *testing.T) {
	ctx := context.Background()
	f := oldAgent()
	if _, err := listWindows(ctx, f.call, ""); err == nil || !strings.Contains(err.Error(), "vm_update_agent") {
		t.Errorf("list: %v", err)
	}
	f = oldAgent()
	if _, _, err := clickPoint(ctx, f.call, clickIn{Window: "Options", X: 1, Y: 1}); err == nil || !strings.Contains(err.Error(), "vm_update_agent") {
		t.Errorf("click: %v", err)
	}
	// A focus by handle must not reach an agent that ignores handle (it would focus by an empty title).
	f = oldAgent()
	if _, err := focusWindow(ctx, f.call, titleIn{Handle: 10}); err == nil || slices.Contains(f.ops, proto.OpFocusWindow) {
		t.Errorf("focus by handle: %v, ops %v", err, f.ops)
	}
	f = oldAgent()
	if r, err := focusWindow(ctx, f.call, titleIn{Title: "note"}); err != nil || resultText(r) != "focused: Notepad" {
		t.Errorf("focus by title: %v %v", r, err)
	}
}

func TestClickPoint(t *testing.T) {
	ctx := context.Background()
	f := newAgent(10)
	if x, y, err := clickPoint(ctx, f.call, clickIn{Window: "options", X: 10, Y: 20}); err != nil || x != 110 || y != 70 {
		t.Fatalf("ok: %d %d %v", x, y, err)
	}
	if p := f.args[1].(proto.PointArgs); p != (proto.PointArgs{X: 110, Y: 70}) {
		t.Errorf("window_at asked for %+v", p)
	}
	// Plain coordinates never ask the agent.
	f = newAgent(10)
	if x, y, err := clickPoint(ctx, f.call, clickIn{X: 5, Y: 6}); err != nil || x != 5 || y != 6 || len(f.ops) != 0 {
		t.Errorf("plain: %d %d %v %v", x, y, err, f.ops)
	}
	// Every refusal is an error; window_at is not asked once an earlier check fails.
	for _, c := range []struct {
		in   clickIn
		at   uint64
		want string
	}{
		{clickIn{Window: "autocad"}, 10, "pass handle instead"},
		{clickIn{Handle: 30}, 10, "not in the foreground"},
		{clickIn{Handle: 10, X: 900}, 10, "outside"},
		{clickIn{Handle: 10, X: 1, Y: 1}, 30, "covered by window"},
		{clickIn{Handle: 10, X: 1, Y: 1}, 0, "off screen"},
	} {
		f := newAgent(c.at)
		_, _, err := clickPoint(ctx, f.call, c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: want %q, got %v", c.in, c.want, err)
		}
		if c.want != "covered by window" && c.want != "off screen" && slices.Contains(f.ops, proto.OpWindowAt) {
			t.Errorf("%+v: window_at asked after a failed check", c.in)
		}
	}
}

func TestFocusWindowTool(t *testing.T) {
	ctx := context.Background()
	f := newAgent(10)
	if _, err := focusWindow(ctx, f.call, titleIn{}); err == nil || len(f.ops) != 0 {
		t.Errorf("empty: %v %v", err, f.ops)
	}
	f = newAgent(10)
	r, err := focusWindow(ctx, f.call, titleIn{Handle: 10})
	if err != nil || resultText(r) != "focused: Options\nhandle: 10" {
		t.Fatalf("handle: %v %v", r, err)
	}
	if !slices.Equal(f.ops, []string{proto.OpListWindows, proto.OpFocusWindow}) || f.args[1].(proto.TitleArgs).Handle != 10 {
		t.Errorf("ops %v args %+v", f.ops, f.args)
	}
}
