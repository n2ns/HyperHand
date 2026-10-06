package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	if in.Window == "" && in.Handle == 0 && in.PID == 0 && !in.Exact {
		return in.X, in.Y, nil
	}
	sel := windowSelector{Title: in.Window, Handle: in.Handle, PID: in.PID, Exact: in.Exact}
	if err := sel.validate(); err != nil {
		return 0, 0, err
	}
	ws, err := listWindows(ctx, call, in.VM)
	if err != nil {
		return 0, 0, err
	}
	w, err := resolveWindow(ws, sel)
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
	sel := windowSelector{Title: in.Title, Handle: in.Handle, PID: in.PID, Exact: in.Exact}
	if err := sel.validate(); err != nil {
		return nil, err
	}
	ws, err := listWindows(ctx, call, in.VM)
	if err != nil {
		return nil, err
	}
	w, err := resolveWindow(ws, sel)
	if err != nil {
		return nil, err
	}
	var r proto.FocusResult
	if _, err := call(ctx, in.VM, proto.OpFocusWindow, proto.TitleArgs{Handle: w.Handle}, nil, &r); err != nil {
		return nil, err
	}
	return text("focused: %s\nhandle: %d", r.Text, r.Handle), nil
}

// describe is a short identification of a window for error messages.
func describe(w proto.WindowInfo) string {
	return fmt.Sprintf("%q (handle %d, %s)", w.Title, w.Handle, w.Process)
}

type windowSelector struct {
	Title  string
	Handle uint64
	PID    uint32
	Exact  bool
}

func (s windowSelector) validate() error {
	if s.Handle == 0 && s.Exact && s.Title == "" {
		return errors.New("exact requires a title")
	}
	if s.Handle == 0 && s.PID == 0 && s.Title == "" {
		return errors.New("pass title, handle or pid")
	}
	return nil
}

var errWindowNotFound = errors.New("no matching visible window")

// A handle takes precedence over the title, as before; PID always restricts the match.
// Several matches are an error even when only one of them is in the foreground.
func resolveWindow(ws []proto.WindowInfo, s windowSelector) (proto.WindowInfo, error) {
	if err := s.validate(); err != nil {
		return proto.WindowInfo{}, err
	}
	var found []proto.WindowInfo
	for _, w := range ws {
		if s.PID != 0 && w.PID != s.PID {
			continue
		}
		if s.Handle != 0 {
			if w.Handle != s.Handle {
				continue
			}
		} else if s.Title != "" {
			if s.Exact {
				if !strings.EqualFold(w.Title, s.Title) {
					continue
				}
			} else if !strings.Contains(strings.ToLower(w.Title), strings.ToLower(s.Title)) {
				continue
			}
		}
		found = append(found, w)
	}
	switch len(found) {
	case 0:
		return proto.WindowInfo{}, fmt.Errorf("%w (handle %d, pid %d, title %q, exact %v)", errWindowNotFound, s.Handle, s.PID, s.Title, s.Exact)
	case 1:
		return found[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d windows match; pass handle instead:", len(found))
	for _, w := range found {
		b.WriteString("\n" + describe(w))
	}
	return proto.WindowInfo{}, fmt.Errorf("%s", b.String())
}

// Window waits run on the host, releasing the agent between polls so other tools can make progress.
func waitWindow(ctx context.Context, call agentCall, in waitIn) (*mcp.CallToolResult, error) {
	switch in.Kind {
	case "window_exists", "window_gone", "window_foreground":
	default:
		return nil, fmt.Errorf("unknown window wait kind %q", in.Kind)
	}
	sel := windowSelector{Title: in.Title, Handle: in.Handle, PID: in.PID, Exact: in.Exact}
	if err := sel.validate(); err != nil {
		return nil, err
	}
	d := 60 * time.Second
	if in.TimeoutMs > 0 {
		// Avoid overflowing time.Duration for a large JSON integer.
		if int64(in.TimeoutMs) > int64((1<<63-1)/time.Millisecond) {
			return nil, errors.New("timeout_ms is too large")
		}
		d = time.Duration(in.TimeoutMs) * time.Millisecond
	}
	wctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if wctx.Err() != nil {
			return text("satisfied: false"), nil
		}
		ws, err := listWindows(wctx, call, in.VM)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if wctx.Err() != nil {
			return text("satisfied: false"), nil
		}
		if err != nil {
			return nil, err
		}
		w, err := resolveWindow(ws, sel)
		missing := errors.Is(err, errWindowNotFound)
		if err != nil && !missing {
			return nil, err
		}
		if in.Kind == "window_gone" && missing {
			return text("satisfied: true"), nil
		}
		if !missing && (in.Kind == "window_exists" || in.Kind == "window_foreground" && w.Foreground) {
			return text("satisfied: true\nhandle: %d\ntitle: %s", w.Handle, w.Title), nil
		}
		select {
		case <-wctx.Done():
		case <-ticker.C:
		}
	}
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
