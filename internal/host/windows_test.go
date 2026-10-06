package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/proto"
)

func testWindows() []proto.WindowInfo {
	return []proto.WindowInfo{
		{Handle: 10, PID: 100, Title: "Options", Process: "acad.exe", Rect: proto.Rect{Left: 100, Top: 50, Right: 600, Bottom: 450}, Enabled: true, Foreground: true, Owner: 20, Modal: true},
		{Handle: 20, PID: 100, Title: "Autodesk AutoCAD 2015 - [a.dwg]", Process: "acad.exe", Rect: proto.Rect{Right: 1920, Bottom: 1040}},
		{Handle: 30, PID: 200, Title: "Autodesk AutoCAD 2015 - [b.dwg]", Process: "acad.exe", Rect: proto.Rect{Right: 1920, Bottom: 1040}, Enabled: true},
	}
}

func TestResolveWindow(t *testing.T) {
	ws := testWindows()
	for _, tt := range []struct {
		name    string
		sel     windowSelector
		want    uint64
		missing bool
	}{
		{"substring", windowSelector{Title: "options"}, 10, false},
		{"handle ignores title and exact", windowSelector{Title: "ignored", Handle: 30, Exact: true}, 30, false},
		{"PID only", windowSelector{PID: 200}, 30, false},
		{"PID intersects title", windowSelector{PID: 100, Title: "AUTOCAD"}, 20, false},
		{"exact case insensitive", windowSelector{Title: "OPTIONS", Exact: true}, 10, false},
		{"missing handle", windowSelector{Handle: 99}, 0, true},
		{"missing title", windowSelector{Title: "notepad"}, 0, true},
		{"exact rejects substring", windowSelector{Title: "option", Exact: true}, 0, true},
		{"PID excludes handle", windowSelector{Handle: 30, PID: 100}, 0, true},
		{"PID excludes title", windowSelector{Title: "Options", PID: 200}, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, err := resolveWindow(ws, tt.sel)
			if tt.missing {
				if !errors.Is(err, errWindowNotFound) {
					t.Fatalf("want not found, got %+v %v", w, err)
				}
			} else if err != nil || w.Handle != tt.want {
				t.Fatalf("want handle %d, got %+v %v", tt.want, w, err)
			}
		})
	}
	for _, sel := range []windowSelector{{}, {Exact: true}, {PID: 200, Exact: true}} {
		if _, err := resolveWindow(ws, sel); err == nil || errors.Is(err, errWindowNotFound) {
			t.Errorf("invalid selector %+v: %v", sel, err)
		}
	}
	for _, sel := range []windowSelector{{Title: "AUTOCAD"}, {PID: 100}} {
		_, err := resolveWindow(ws, sel)
		if err == nil || errors.Is(err, errWindowNotFound) {
			t.Fatalf("ambiguous %+v: %v", sel, err)
		}
		for _, w := range ws {
			if (sel.PID != 0 && w.PID == sel.PID) || (sel.Title != "" && strings.Contains(strings.ToUpper(w.Title), sel.Title)) {
				if !strings.Contains(err.Error(), fmt.Sprintf("handle %d", w.Handle)) {
					t.Errorf("ambiguous error omits handle %d: %v", w.Handle, err)
				}
			}
		}
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
	if _, err := focusWindow(ctx, f.call, titleIn{Title: "note"}); err == nil || !strings.Contains(err.Error(), "vm_update_agent") || slices.Contains(f.ops, proto.OpFocusWindow) {
		t.Errorf("focus by title: %v, ops %v", err, f.ops)
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

func TestFocusWindowSelection(t *testing.T) {
	for _, in := range []titleIn{{Title: "OPTIONS", Exact: true, PID: 100}, {Handle: 10, PID: 100, Title: "ignored", Exact: true}} {
		f := newAgent(10)
		if _, err := focusWindow(context.Background(), f.call, in); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(f.ops, []string{proto.OpListWindows, proto.OpFocusWindow}) {
			t.Fatalf("ops: %v", f.ops)
		}
		if got := f.args[1].(proto.TitleArgs); got != (proto.TitleArgs{Handle: 10}) {
			t.Errorf("focus must use resolved handle only: %+v", got)
		}
	}
	for _, in := range []titleIn{{Title: "autocad"}, {PID: 100}, {Handle: 10, PID: 200}, {Title: "Option", Exact: true}} {
		f := newAgent(10)
		if _, err := focusWindow(context.Background(), f.call, in); err == nil {
			t.Errorf("want refusal: %+v", in)
		}
		if slices.Contains(f.ops, proto.OpFocusWindow) {
			t.Errorf("focus called after selection refusal: %+v", in)
		}
	}
}

func TestClickPointSelection(t *testing.T) {
	for _, in := range []clickIn{{Window: "OPTIONS", PID: 100, Exact: true, X: 1, Y: 2}, {Handle: 10, PID: 100, Window: "ignored", Exact: true, X: 1, Y: 2}} {
		f := newAgent(10)
		if x, y, err := clickPoint(context.Background(), f.call, in); err != nil || x != 101 || y != 52 {
			t.Errorf("%+v: %d %d %v", in, x, y, err)
		}
	}
	for _, in := range []clickIn{{PID: 100}, {Handle: 10, PID: 200}, {Window: "Option", Exact: true}, {Exact: true}} {
		f := newAgent(10)
		if _, _, err := clickPoint(context.Background(), f.call, in); err == nil {
			t.Errorf("want refusal: %+v", in)
		}
		if slices.Contains(f.ops, proto.OpWindowAt) {
			t.Errorf("hit testing called after selection refusal: %+v", in)
		}
	}
	// A PID-only selector must select a window, never fall back to absolute coordinates.
	f := newAgent(30)
	if _, _, err := clickPoint(context.Background(), f.call, clickIn{PID: 200}); err == nil || !strings.Contains(err.Error(), "foreground") {
		t.Errorf("PID-only background: %v", err)
	}
}
