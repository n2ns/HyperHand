package host

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/proto"
)

type clickIn struct {
	VM     string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Button string `json:"button,omitempty" jsonschema:"left (default), right or middle"`
	Double bool   `json:"double,omitempty" jsonschema:"double click"`
	Window string `json:"window,omitempty" jsonschema:"click inside this window: case-insensitive title substring that must match exactly one window (see vm_windows); x and y are then relative to its top-left corner, and the click is refused unless it is enabled, it or one of its own windows is in the foreground, and the point is inside it and reaches it or one of its own windows; needs the agent"`
	Handle uint64 `json:"handle,omitempty" jsonschema:"like window, but selects the window by its handle from vm_windows"`
	PID    uint32 `json:"pid,omitempty" jsonschema:"restrict the target window to this process ID; may be used alone if exactly one visible window matches"`
	Exact  bool   `json:"exact,omitempty" jsonschema:"match the full window title instead of a substring, case-insensitively; requires window unless handle is set"`
}
type dragIn struct {
	VM string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	X1 int    `json:"x1"`
	Y1 int    `json:"y1"`
	X2 int    `json:"x2"`
	Y2 int    `json:"y2"`
}
type scrollIn struct {
	VM    string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	Delta int    `json:"delta" jsonschema:"wheel notches; positive scrolls up, negative down"`
}
type typeIn struct {
	VM   string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Text string `json:"text"`
}
type typeTextIn struct {
	VM     string `json:"vm,omitempty"`
	Text   string `json:"text"`
	Mode   string `json:"mode,omitempty" jsonschema:"paste (default) or keys (Unicode SendInput; requires updated agent, does not use clipboard)"`
	Window string `json:"window,omitempty" jsonschema:"target window title; it or one of its own windows must already be foreground"`
	Handle uint64 `json:"handle,omitempty"`
	PID    uint32 `json:"pid,omitempty"`
	Exact  bool   `json:"exact,omitempty"`
}
type keyIn struct {
	VM       string   `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Keys     string   `json:"keys,omitempty" jsonschema:"a key or combination; pass either keys or sequence"`
	Sequence []string `json:"sequence,omitempty" jsonschema:"ordered key combinations, e.g. [ctrl+a,backspace]; at most 256"`
	Window   string   `json:"window,omitempty" jsonschema:"target window title; it or one of its own windows must remain foreground"`
	Handle   uint64   `json:"handle,omitempty"`
	PID      uint32   `json:"pid,omitempty"`
	Exact    bool     `json:"exact,omitempty"`
}

// registerActions registers the mouse, keyboard and control-action tools.
func registerActions(d *deps) {
	s, raw, backend, input, call, u := d.s, d.raw, d.backend, d.input, d.call, d.u
	add(s, "vm_click", "Click at screen pixel (x, y), or with window, handle or pid at (x, y) inside the unique matching window after checking it is enabled, that it or one of its own windows (same process, owned directly or through other owned windows, e.g. AutoCAD's command line) is in the foreground, and that the point reaches it or one of its own windows. exact matches the full title. With a selector the result also gives the handle, pid, class, process and title of the window the click reached.", func(ctx context.Context, in clickIn) (*mcp.CallToolResult, error) {
		b := map[string]int{"": 1, "left": 1, "right": 2, "middle": 3}[in.Button]
		if b == 0 {
			return nil, fmt.Errorf("unknown button %q", in.Button)
		}
		// Keep window checks and the final input on the same VM if the default changes.
		v, err := backend.Find(in.VM)
		if err != nil {
			return nil, err
		}
		in.VM = v.Name
		input.Lock()
		defer input.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		x, y, hit, err := clickPoint(ctx, call, in)
		if err != nil {
			return nil, u.lockedHint(ctx, in.VM, err)
		}
		if err := raw.Click(in.VM, x, y, b, 1+boolInt(in.Double), nil); err != nil {
			return nil, err
		}
		if hit.Handle == 0 {
			return text("ok"), nil
		}
		return text("ok\n%s", windowLines(hit)), nil
	})
	add(s, "vm_drag", "Drag with the left button from (x1, y1) to (x2, y2).", func(ctx context.Context, in dragIn) (*mcp.CallToolResult, error) {
		return done(backend.Drag(in.VM, in.X1, in.Y1, in.X2, in.Y2, nil))
	})
	add(s, "vm_scroll", "Scroll the mouse wheel at (x, y).", func(ctx context.Context, in scrollIn) (*mcp.CallToolResult, error) {
		return done(backend.Scroll(in.VM, in.X, in.Y, in.Delta))
	})
	add(s, "vm_type", "Type text into the foreground window. mode=paste (default) uses the clipboard, with ASCII keyboard fallback without the agent. mode=keys uses guest Unicode SendInput, preserves the clipboard and requires an updated agent. Optional window/handle/pid restricts the target: input goes to that window, or to one of its own windows (same process, owned directly or through other owned windows, e.g. AutoCAD's command line) when that one is in the foreground; never focuses automatically. With a target the result gives the handle, pid, class, process and title of the window that received the input. Input may be partial on failure and must not be blindly retried.",
		func(ctx context.Context, in typeTextIn) (*mcp.CallToolResult, error) {
			if in.Mode != "" && in.Mode != "paste" && in.Mode != "keys" {
				return nil, fmt.Errorf("unknown input mode %q", in.Mode)
			}
			v, err := raw.Find(in.VM)
			if err != nil {
				return nil, err
			}
			in.VM = v.Name
			input.Lock()
			defer input.Unlock()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			sel := windowSelector{Title: in.Window, Handle: in.Handle, PID: in.PID, Exact: in.Exact}
			checkTarget := in.Mode == "keys" || sel != (windowSelector{})
			var root, target proto.WindowInfo
			if checkTarget {
				root, target, err = inputWindow(ctx, call, in.VM, sel)
				if err != nil {
					return nil, err
				}
			}
			if in.Mode == "keys" {
				var r proto.TypeKeysResult
				_, err := call(ctx, in.VM, proto.OpTypeKeys, proto.TypeKeysArgs{Text: in.Text, Handle: target.Handle, PID: target.PID}, nil, &r)
				if err != nil {
					return nil, fmt.Errorf("keys input failed (requires an updated agent): %w", err)
				}
				return text("input events: %d\n%s", r.Events, windowLines(target)), nil
			}
			_, err = call(ctx, in.VM, proto.OpClipboardSet, proto.TextArgs{Text: in.Text}, nil, nil)
			if err == nil {
				if !checkTarget {
					return done(raw.PressKeys(in.VM, "ctrl+v"))
				}
				// Recheck after setting the clipboard; the paste goes to whichever window of the group is foreground now.
				if _, target, err = inputWindow(ctx, call, in.VM, windowSelector{Handle: root.Handle, PID: root.PID}); err != nil {
					return nil, err
				}
				if err := raw.PressKeys(in.VM, "ctrl+v"); err != nil {
					return nil, err
				}
				return text("ok\n%s", windowLines(target)), nil
			}
			if checkTarget {
				return nil, err
			}
			for _, r := range in.Text {
				if r >= 128 {
					return nil, fmt.Errorf("non-ASCII text needs the agent: %w", err)
				}
			}
			return done(raw.TypeText(in.VM, in.Text))
		})
	add(s, "vm_key", "Press keys or an ordered sequence of key combinations. Optional window/handle/pid requires the target, or one of its own windows, to remain foreground; the result then gives the window that received the last combination. Keys: ctrl, shift, alt, win, enter, esc, tab, space, backspace, delete, insert, home, end, pageup, pagedown, arrows, f1-f12, a-z, 0-9, punctuation and plus. Partial sequences are not retried.", func(ctx context.Context, in keyIn) (*mcp.CallToolResult, error) {
		sequence, err := keySequence(in)
		if err != nil {
			return nil, err
		}
		v, err := raw.Find(in.VM)
		if err != nil {
			return nil, err
		}
		input.Lock()
		defer input.Unlock()
		sel := windowSelector{Title: in.Window, Handle: in.Handle, PID: in.PID, Exact: in.Exact}
		var target proto.WindowInfo
		for i, keys := range sequence {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("after %d combinations: %w", i, err)
			}
			if sel != (windowSelector{}) {
				// Pin the selected window; each combination goes to it or to one of its own windows in the foreground.
				var root proto.WindowInfo
				root, target, err = inputWindow(ctx, call, v.Name, sel)
				if err != nil {
					return nil, fmt.Errorf("after %d combinations: %w", i, err)
				}
				sel = windowSelector{Handle: root.Handle, PID: root.PID}
			}
			if err := raw.PressKeys(v.Name, keys); err != nil {
				return nil, fmt.Errorf("combination %d failed; input may be partial: %w", i+1, err)
			}
		}
		if target.Handle == 0 {
			return text("ok"), nil
		}
		return text("ok\n%s", windowLines(target)), nil
	})
	for _, name := range []string{"vm_set_value", "vm_invoke"} {
		addToolIn(d, toolSpec{name: name, desc: "Pending: UI Automation control action by observation index."}, func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
			return nil, notImplemented(name)
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
