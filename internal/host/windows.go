package host

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/proto"
)

// agentCall calls an op on a VM's agent (NewServer's call; a fake in tests).
type agentCall func(ctx context.Context, vm, op string, args any, payload []byte, result any) ([]byte, error)

func listWindows(ctx context.Context, call agentCall, vm string) ([]proto.WindowInfo, error) {
	var r proto.WindowsResult
	if _, err := call(ctx, vm, proto.OpListWindows, nil, nil, &r); err != nil {
		if strings.HasPrefix(err.Error(), "unknown op") {
			return nil, fmt.Errorf("the guest agent is too old to list windows; run vm_update_agent")
		}
		return nil, err
	}
	return r.Windows, nil
}

// clickPoint returns the screen point for vm_click: (x, y) as given, or with window or handle, the point inside that
// window after every check passed. An error means nothing may be clicked.
func clickPoint(ctx context.Context, call agentCall, in clickIn) (int, int, error) {
	if in.Window == "" && in.Handle == 0 {
		return in.X, in.Y, nil
	}
	ws, err := listWindows(ctx, call, in.VM)
	if err != nil {
		return 0, 0, err
	}
	w, err := resolveWindow(ws, in.Window, in.Handle)
	if err != nil {
		return 0, 0, err
	}
	x, y, err := clickTarget(ws, w, in.X, in.Y)
	if err != nil {
		return 0, 0, err
	}
	var at proto.HandleResult
	if _, err := call(ctx, in.VM, proto.OpWindowAt, proto.PointArgs{X: x, Y: y}, nil, &at); err != nil {
		return 0, 0, err
	}
	if err := checkHit(ws, w, x, y, at.Handle); err != nil {
		return 0, 0, err
	}
	return x, y, nil
}

func focusWindow(ctx context.Context, call agentCall, in titleIn) (*mcp.CallToolResult, error) {
	if in.Title == "" && in.Handle == 0 {
		return nil, errors.New("pass title or handle") // an empty title would match any window
	}
	if in.Handle != 0 {
		// An agent without list_windows ignores handle and would focus by an empty title, i.e. any window.
		if _, err := listWindows(ctx, call, in.VM); err != nil {
			return nil, err
		}
	}
	var r proto.FocusResult
	if _, err := call(ctx, in.VM, proto.OpFocusWindow, proto.TitleArgs{Title: in.Title, Handle: in.Handle}, nil, &r); err != nil {
		return nil, err
	}
	if r.Handle == 0 { // older agent
		return text("focused: %s", r.Text), nil
	}
	return text("focused: %s\nhandle: %d", r.Text, r.Handle), nil
}

// describe is a short identification of a window for error messages.
func describe(w proto.WindowInfo) string {
	return fmt.Sprintf("%q (handle %d, %s)", w.Title, w.Handle, w.Process)
}

// resolveWindow picks the window with the handle, or else the only window whose title contains title
// (case-insensitive). Several matches are an error that lists them, so the caller can pass a handle instead.
func resolveWindow(ws []proto.WindowInfo, title string, handle uint64) (proto.WindowInfo, error) {
	if handle != 0 {
		for _, w := range ws {
			if w.Handle == handle {
				return w, nil
			}
		}
		return proto.WindowInfo{}, fmt.Errorf("no visible top-level window has handle %d", handle)
	}
	var found []proto.WindowInfo
	for _, w := range ws {
		if strings.Contains(strings.ToLower(w.Title), strings.ToLower(title)) {
			found = append(found, w)
		}
	}
	switch len(found) {
	case 0:
		return proto.WindowInfo{}, fmt.Errorf("no visible window title contains %q", title)
	case 1:
		return found[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d windows have a title containing %q; pass handle instead:", len(found), title)
	for _, w := range found {
		b.WriteString("\n" + describe(w))
	}
	return proto.WindowInfo{}, fmt.Errorf("%s", b.String())
}

// clickTarget converts (x, y), relative to the top-left of w's visible frame, to screen pixels. It refuses when w is
// not the foreground window, is disabled (a modal dialog owns input), or the point is outside w.
func clickTarget(ws []proto.WindowInfo, w proto.WindowInfo, x, y int) (int, int, error) {
	if !w.Foreground {
		fg := "none"
		for _, o := range ws {
			if o.Foreground {
				fg = describe(o)
			}
		}
		return 0, 0, fmt.Errorf("window %s is not in the foreground; the foreground window is %s", describe(w), fg)
	}
	if !w.Enabled {
		return 0, 0, fmt.Errorf("window %s is disabled (a modal dialog owns input)", describe(w))
	}
	r := w.Rect
	ax, ay := int(r.Left)+x, int(r.Top)+y
	if x < 0 || y < 0 || ax >= int(r.Right) || ay >= int(r.Bottom) {
		return 0, 0, fmt.Errorf("(%d, %d) is outside window %s, which is %dx%d", x, y, describe(w), r.Right-r.Left, r.Bottom-r.Top)
	}
	return ax, ay, nil
}

// checkHit refuses a click at screen point (x, y) unless at, the top-level window there, is w: another window (for
// example an always-on-top one) covers the point, or the point is off screen (at is 0).
func checkHit(ws []proto.WindowInfo, w proto.WindowInfo, x, y int, at uint64) error {
	if at == w.Handle {
		return nil
	}
	if at == 0 {
		return fmt.Errorf("(%d, %d) in window %s is off screen", x, y, describe(w))
	}
	other := fmt.Sprintf("handle %d", at)
	for _, o := range ws {
		if o.Handle == at {
			other = describe(o)
		}
	}
	return fmt.Errorf("screen point (%d, %d) of window %s is covered by window %s", x, y, describe(w), other)
}
