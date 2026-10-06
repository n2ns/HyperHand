package host

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/credential"
	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

// GuestAgentPath is where vm_install_agent puts the agent in the guest.
const GuestAgentPath = `C:\Users\Public\HyperHand\hyperhand-agent.exe`

type vmIn struct {
	VM string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
}
type screenshotIn struct {
	VM      string            `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Source  string            `json:"source,omitempty" jsonschema:"host (default: Hyper-V console, works without the agent) or agent (taken inside the guest)"`
	Region  *screenshotRegion `json:"region,omitempty" jsonschema:"crop in original image pixels: x, y, width, height; must fit inside the captured image"`
	MaxSize int               `json:"max_size,omitempty" jsonschema:"maximum output width or height; 0 keeps original size, never upscales"`
}
type clickIn struct {
	VM     string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Button string `json:"button,omitempty" jsonschema:"left (default), right or middle"`
	Double bool   `json:"double,omitempty" jsonschema:"double click"`
	Window string `json:"window,omitempty" jsonschema:"click inside this window: case-insensitive title substring that must match exactly one window (see vm_windows); x and y are then relative to its top-left corner, and the click is refused unless it is the enabled foreground window and the point is inside it; needs the agent"`
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
	Window string `json:"window,omitempty" jsonschema:"target window title; must already be foreground"`
	Handle uint64 `json:"handle,omitempty"`
	PID    uint32 `json:"pid,omitempty"`
	Exact  bool   `json:"exact,omitempty"`
}
type keyIn struct {
	VM       string   `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Keys     string   `json:"keys,omitempty" jsonschema:"a key or combination; pass either keys or sequence"`
	Sequence []string `json:"sequence,omitempty" jsonschema:"ordered key combinations, e.g. [ctrl+a,backspace]; at most 256"`
	Window   string   `json:"window,omitempty" jsonschema:"target window title; must remain foreground"`
	Handle   uint64   `json:"handle,omitempty"`
	PID      uint32   `json:"pid,omitempty"`
	Exact    bool     `json:"exact,omitempty"`
}
type execIn struct {
	VM        string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Command   string `json:"command"`
	Shell     string `json:"shell,omitempty" jsonschema:"powershell (default) or cmd"`
	Cwd       string `json:"cwd,omitempty" jsonschema:"working directory in the guest"`
	TimeoutMs int    `json:"timeout_ms,omitempty" jsonschema:"default 60000"`
	Admin     bool   `json:"admin,omitempty" jsonschema:"run elevated (administrator)"`
}
type pushIn struct {
	VM        string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	HostPath  string `json:"host_path" jsonschema:"file or directory on the host"`
	GuestPath string `json:"guest_path" jsonschema:"destination file or directory in the guest"`
	Force     bool   `json:"force,omitempty" jsonschema:"upload every file even if the guest already has an identical copy; default false"`
}
type pullIn struct {
	VM        string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	GuestPath string `json:"guest_path" jsonschema:"file or directory in the guest"`
	HostPath  string `json:"host_path" jsonschema:"destination file or directory on the host"`
}
type checkpointIn struct {
	VM   string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Name string `json:"name" jsonschema:"checkpoint name"`
}
type restoreIn struct {
	VM    string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Name  string `json:"name" jsonschema:"checkpoint name"`
	Start *bool  `json:"start,omitempty" jsonschema:"start the VM after restoring if it is not running; default true"`
}
type titleIn struct {
	VM     string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Title  string `json:"title,omitempty" jsonschema:"case-insensitive substring of the window title"`
	Handle uint64 `json:"handle,omitempty" jsonschema:"window handle from vm_windows; when set, title is ignored"`
	PID    uint32 `json:"pid,omitempty" jsonschema:"restrict the target window to this process ID; may be used alone if exactly one visible window matches"`
	Exact  bool   `json:"exact,omitempty" jsonschema:"match the full title instead of a substring, case-insensitively; requires title unless handle is set"`
}
type waitIn struct {
	VM        string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Kind      string `json:"kind" jsonschema:"process_exit, process_running (use name), file_exists (use path), window_exists, window_gone or window_foreground (use title, handle or pid)"`
	Name      string `json:"name,omitempty" jsonschema:"process name, e.g. notepad"`
	Path      string `json:"path,omitempty" jsonschema:"file path in the guest"`
	TimeoutMs int    `json:"timeout_ms,omitempty" jsonschema:"default 60000"`
	Title     string `json:"title,omitempty" jsonschema:"window title substring, case-insensitive; only for window conditions"`
	Handle    uint64 `json:"handle,omitempty" jsonschema:"window handle from vm_windows; when set, title is ignored"`
	PID       uint32 `json:"pid,omitempty" jsonschema:"restrict the target window to this process ID; may be used alone if exactly one visible window matches"`
	Exact     bool   `json:"exact,omitempty" jsonschema:"match the full title instead of a substring, case-insensitively; requires title unless handle is set"`
}

type controlsIn struct {
	VM       string `json:"vm,omitempty"`
	Title    string `json:"title,omitempty"`
	Handle   uint64 `json:"handle,omitempty"`
	PID      uint32 `json:"pid,omitempty"`
	Exact    bool   `json:"exact,omitempty"`
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"default 4; maximum 10; root depth is 0"`
	MaxNodes int    `json:"max_nodes,omitempty" jsonschema:"default 200; maximum 1000"`
}

func add[In any](s *mcp.Server, name, desc string, f func(context.Context, In) (*mcp.CallToolResult, error)) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc}, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		r, err := f(ctx, in)
		return r, nil, err
	})
}

func text(format string, a ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, a...)}}}
}

func done(err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return nil, err
	}
	return text("ok"), nil
}

// NewServer builds the MCP server with all HyperHand tools.
func NewServer(m *Manager) *mcp.Server {
	raw := m.backend()
	input := &sync.Mutex{}
	backend := lockedInput{raw, input}
	s := mcp.NewServer(&mcp.Implementation{Name: "hyperhand", Version: proto.Version}, nil)
	call := func(ctx context.Context, vm, op string, args any, payload []byte, result any) ([]byte, error) {
		c, err := m.Client(vm)
		if err != nil {
			return nil, err
		}
		return c.Call(ctx, op, args, payload, result)
	}

	add(s, "vm_list", "List Hyper-V VMs (name, state, id).", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		vms, err := backend.ListVMs()
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		for _, v := range vms {
			fmt.Fprintf(&b, "%s\t%s\t%s\n", v.Name, v.State, v.ID)
		}
		if b.Len() == 0 {
			return text("no VMs"), nil
		}
		return text("%s", b.String()), nil
	})
	u := newUnlocker(call, raw, input)
	add(s, "vm_start", "Start a VM (if it is not running) and wait until its desktop is usable: the guest agent answers and the session is unlocked, typing the unlock password stored in the HyperHand tray if the session is locked. An error says why the desktop is not usable; the VM may still be running.", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		v, err := backend.Find(in.VM)
		if err != nil {
			return nil, err
		}
		if v.State != "Running" {
			err = backend.Start(v.Name)
			m.Drop(v.ID)
			if err != nil {
				return nil, err
			}
			if m.AfterStart != nil {
				m.AfterStart(v.Name)
			}
		}
		r, err := u.ready(ctx, v.Name, agentStartTimeout)
		if err != nil {
			return nil, fmt.Errorf("VM %s is running, but its desktop is not usable: %w", v.Name, err)
		}
		return text("VM %s is running; %s", v.Name, r), nil
	})
	add(s, "vm_status", "Report a VM's power state and, when it runs, whether the guest agent answers, whether the session is locked, and whether an unlock password is stored. Does not wait or change anything.", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		v, err := backend.Find(in.VM)
		if err != nil {
			return nil, err
		}
		_, _, stored, credErr := credential.Read(v.Name)
		pw := map[bool]string{true: "stored", false: "not stored"}[stored]
		if credErr != nil {
			pw = "unknown: " + credErr.Error()
		}
		var b strings.Builder
		fmt.Fprintf(&b, "VM: %s\npower: %s\nunlock password: %s\n", v.Name, v.State, pw)
		if v.State != "Running" {
			return text("%s", b.String()), nil
		}
		var p proto.PingResult
		c, err := m.Client(v.Name)
		if err != nil {
			return nil, err
		}
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = c.TryCall(pctx, proto.OpPing, &p)
		cancel()
		if err != nil {
			if errors.Is(err, ErrAgentBusy) {
				b.WriteString("agent: busy (this host has another request in progress)\nsession: not queried while busy\n")
			} else {
				fmt.Fprintf(&b, "agent: not answering (connection or response failed; guest state is unknown): %v\n", err)
			}
			return text("%s", b.String()), nil
		}
		fmt.Fprintf(&b, "agent: %s on %s as %s\n", p.Version, p.Hostname, p.User)
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var st proto.SessionStateResult
		err = c.TryCall(sctx, proto.OpSessionState, &st)
		cancel()
		switch {
		case err != nil:
			fmt.Fprintf(&b, "session: unknown: %v\n", err)
		case st.Locked:
			b.WriteString("session: locked\n")
		default:
			b.WriteString("session: unlocked\n")
		}
		if err == nil && !st.Console {
			b.WriteString("console: no (an enhanced session, remote desktop or another user's session: host screenshots and input do not reach it)\n")
		}
		if err == nil && st.Consent {
			b.WriteString("UAC prompt: open\n")
		}
		return text("%s", b.String()), nil
	})
	add(s, "vm_unlock", "Unlock a running VM's locked session by typing the unlock password stored in the HyperHand tray on the Hyper-V keyboard. Types it once and only while the agent reports the session locked and no UAC prompt open; the password is never returned.", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		v, err := backend.Find(in.VM)
		if err != nil {
			return nil, err
		}
		if v.State != "Running" {
			return nil, fmt.Errorf("VM %s is %s; start it with vm_start", v.Name, v.State)
		}
		r, err := u.unlock(ctx, v.Name)
		if err != nil {
			return nil, err
		}
		return text("%s", r), nil
	})
	add(s, "vm_shutdown", "Shut a VM down normally: ask Windows in the guest to shut down (through the Hyper-V shutdown integration service) and wait up to 3 minutes until the VM is off. Not forced: a program with unsaved work can keep Windows from shutting down, and the tool then fails with the VM still running. Never turns the power off; use vm_turn_off only when the guest cannot shut down.", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		v, err := backend.Find(in.VM)
		if err != nil {
			return nil, err
		}
		if v.State == "Off" {
			return text("VM %s is already off", v.Name), nil
		}
		if err := backend.Shutdown(v.Name); err != nil {
			return nil, err
		}
		err = waitOff(ctx, func() (hyperv.VM, error) { return backend.Find(v.Name) }, time.Sleep, shutdownTimeout)
		m.Drop(v.ID)
		if err != nil {
			return nil, err
		}
		return text("VM %s is off", v.Name), nil
	})
	add(s, "vm_turn_off", "Turn a VM off immediately, like pulling the power plug: unsaved work in the guest is lost and its file system may be damaged. Use vm_shutdown instead unless the guest is stuck.", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		v, err := backend.Find(in.VM)
		if err != nil {
			return nil, err
		}
		err = backend.Stop(v.Name)
		m.Drop(v.ID)
		return done(err)
	})
	add(s, "vm_checkpoints", "List the VM's checkpoints (name, creation time).", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		cps, err := backend.ListCheckpoints(in.VM)
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		for _, c := range cps {
			fmt.Fprintf(&b, "%s\t%s\n", c.Name, c.CreationTime)
		}
		if b.Len() == 0 {
			return text("no checkpoints"), nil
		}
		return text("%s", b.String()), nil
	})
	add(s, "vm_checkpoint", "Create a checkpoint of the VM.", func(ctx context.Context, in checkpointIn) (*mcp.CallToolResult, error) {
		return done(backend.CreateCheckpoint(in.VM, in.Name))
	})
	add(s, "vm_restore", "Restore a checkpoint (exact name), then start the VM if it is not running (unless start is false).", func(ctx context.Context, in restoreIn) (*mcp.CallToolResult, error) {
		v, err := backend.Find(in.VM) // resolve "" now: after the restore the VM may be off
		if err != nil {
			return nil, err
		}
		if err := backend.RestoreCheckpoint(v.Name, in.Name); err != nil {
			return nil, err
		}
		m.Drop(v.ID)
		if in.Start == nil || *in.Start {
			if v, err = backend.Find(v.Name); err != nil {
				return nil, err
			} else if v.State != "Running" { // Hyper-V may already have resumed the restored VM.
				return done(backend.Start(v.Name))
			}
		}
		return text("ok"), nil
	})
	add(s, "vm_screenshot", "Take a PNG screenshot, optionally cropped and scaled. Returns JSON coordinate metadata alongside the image. Map image pixels back using origin + crop + pixel/scale before vm_click, vm_drag or vm_scroll; agent screenshots from a non-console or unknown session must not be used for console input.",
		func(ctx context.Context, in screenshotIn) (*mcp.CallToolResult, error) {
			v, err := backend.Find(in.VM)
			if err != nil {
				return nil, err
			}
			if in.Source == "" {
				in.Source = "host"
			}
			var data []byte
			var capture proto.ScreenshotResult
			compatible := in.Source == "host"
			switch in.Source {
			case "host":
				data, _, _, err = backend.Screenshot(v.Name)
			case "agent":
				data, err = call(ctx, v.Name, proto.OpScreenshot, nil, nil, &capture)
				compatible = capture.Width > 0 && capture.Height > 0 && capture.Console
			default:
				err = fmt.Errorf("unknown source %q", in.Source)
			}
			if err != nil {
				return nil, err
			}
			data, geometry, err := transformScreenshot(data, screenshotOptions{Region: in.Region, MaxSize: in.MaxSize})
			if err != nil {
				return nil, err
			}
			if in.Source == "agent" && capture.Width > 0 && (capture.Width != geometry.OriginalWidth || capture.Height != geometry.OriginalHeight) {
				return nil, fmt.Errorf("agent screenshot dimensions do not match capture metadata")
			}
			metadata, err := json.Marshal(struct {
				screenshotGeometry
				VM                 string                  `json:"vm"`
				VMID               string                  `json:"vm_id"`
				Source             string                  `json:"source"`
				CapturedAt         string                  `json:"captured_at"`
				OriginX            int                     `json:"origin_x"`
				OriginY            int                     `json:"origin_y"`
				AgentSession       *proto.ScreenshotResult `json:"agent_session,omitempty"`
				ConsoleCoordinates bool                    `json:"console_coordinates"`
			}{geometry, v.Name, v.ID, in.Source, time.Now().UTC().Format(time.RFC3339Nano), capture.OriginX, capture.OriginY,
				func() *proto.ScreenshotResult {
					if in.Source == "agent" && capture.Width > 0 {
						return &capture
					}
					return nil
				}(), compatible})
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.ImageContent{Data: data, MIMEType: "image/png"},
				&mcp.TextContent{Text: string(metadata)},
			}}, nil
		})
	add(s, "vm_windows", "List the guest's visible top-level windows, from the top of the Z order down, as JSON: handle, title, class, pid, process, rect (visible frame in screenshot pixels), enabled, foreground, minimized, owner (owner window handle) and modal (its owner is disabled, as while a modal dialog runs).",
		func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
			ws, err := listWindows(ctx, call, in.VM)
			if err != nil {
				return nil, err
			}
			b, err := json.MarshalIndent(ws, "", "  ")
			if err != nil {
				return nil, err
			}
			return text("%s", b), nil
		})
	add(s, "vm_click", "Click at screen pixel (x, y), or with window, handle or pid at (x, y) inside the unique matching window after checking it is the enabled foreground window. exact matches the full title.", func(ctx context.Context, in clickIn) (*mcp.CallToolResult, error) {
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
		x, y, err := clickPoint(ctx, call, in)
		if err != nil {
			return nil, u.lockedHint(ctx, in.VM, err)
		}
		return done(raw.Click(in.VM, x, y, b, in.Double))
	})
	add(s, "vm_controls", "Read a bounded UI Automation control-view tree for one visible window selected by title, handle or pid. Requires an updated agent. Returns snapshot-local indexes, physical screen rectangles and truncation reasons. Does not click or read values; password names and subtrees are omitted. Provider calls time out after 10 seconds.", func(ctx context.Context, in controlsIn) (*mcp.CallToolResult, error) {
		if in.MaxDepth < 0 || in.MaxDepth > 10 || in.MaxNodes < 0 || in.MaxNodes > 1000 {
			return nil, fmt.Errorf("max_depth must be 0..10 and max_nodes 0..1000 (0 uses defaults)")
		}
		c, err := m.Client(in.VM)
		if err != nil {
			return nil, err
		}
		pinned := func(ctx context.Context, _, op string, args any, payload []byte, result any) ([]byte, error) {
			return c.Call(ctx, op, args, payload, result)
		}
		ws, err := listWindows(ctx, pinned, in.VM)
		if err != nil {
			return nil, err
		}
		w, err := resolveWindow(ws, windowSelector{Title: in.Title, Handle: in.Handle, PID: in.PID, Exact: in.Exact})
		if err != nil {
			return nil, err
		}
		var r proto.ControlsResult
		if _, err := c.Call(ctx, proto.OpListControls, proto.ControlsArgs{Handle: w.Handle, PID: w.PID, MaxDepth: in.MaxDepth, MaxNodes: in.MaxNodes}, nil, &r); err != nil {
			return nil, fmt.Errorf("controls require an updated agent with UI Automation support: %w", err)
		}
		data, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		return text("%s", data), nil
	})
	add(s, "vm_drag", "Drag with the left button from (x1, y1) to (x2, y2).", func(ctx context.Context, in dragIn) (*mcp.CallToolResult, error) {
		return done(backend.Drag(in.VM, in.X1, in.Y1, in.X2, in.Y2))
	})
	add(s, "vm_scroll", "Scroll the mouse wheel at (x, y).", func(ctx context.Context, in scrollIn) (*mcp.CallToolResult, error) {
		return done(backend.Scroll(in.VM, in.X, in.Y, in.Delta))
	})
	add(s, "vm_type", "Type text into the foreground window. mode=paste (default) uses the clipboard, with ASCII keyboard fallback without the agent. mode=keys uses guest Unicode SendInput, preserves the clipboard and requires an updated agent. Optional window/handle/pid restricts the target; never focuses it automatically. Input may be partial on failure and must not be blindly retried.",
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
			var target proto.WindowInfo
			if checkTarget {
				target, err = inputWindow(ctx, call, in.VM, sel)
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
				return text("input events: %d", r.Events), nil
			}
			_, err = call(ctx, in.VM, proto.OpClipboardSet, proto.TextArgs{Text: in.Text}, nil, nil)
			if err == nil {
				if checkTarget {
					if _, err := inputWindow(ctx, call, in.VM, windowSelector{Handle: target.Handle, PID: target.PID}); err != nil {
						return nil, err
					}
				}
				return done(raw.PressKeys(in.VM, "ctrl+v"))
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
	add(s, "vm_key", "Press keys or an ordered sequence of key combinations. Optional window/handle/pid requires the target to remain foreground. Keys: ctrl, shift, alt, win, enter, esc, tab, space, backspace, delete, insert, home, end, pageup, pagedown, arrows, f1-f12, a-z, 0-9, punctuation and plus. Partial sequences are not retried.", func(ctx context.Context, in keyIn) (*mcp.CallToolResult, error) {
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
		for i, keys := range sequence {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("after %d combinations: %w", i, err)
			}
			if sel != (windowSelector{}) {
				w, err := inputWindow(ctx, call, v.Name, sel)
				if err != nil {
					return nil, fmt.Errorf("after %d combinations: %w", i, err)
				}
				sel = windowSelector{Handle: w.Handle, PID: w.PID}
			}
			if err := raw.PressKeys(v.Name, keys); err != nil {
				return nil, fmt.Errorf("combination %d failed; input may be partial: %w", i+1, err)
			}
		}
		return text("ok"), nil
	})
	add(s, "vm_exec", "Run a command in the guest (as the logged-on user); returns exit code, stdout and stderr.",
		func(ctx context.Context, in execIn) (*mcp.CallToolResult, error) {
			var r proto.ExecResult
			if _, err := call(ctx, in.VM, proto.OpExec, proto.ExecArgs{Command: in.Command, Shell: in.Shell, Cwd: in.Cwd, TimeoutMs: in.TimeoutMs, Admin: in.Admin}, nil, &r); err != nil {
				return nil, err
			}
			return text("exit_code: %d\ntimed_out: %v\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.TimedOut, r.Stdout, r.Stderr), nil
		})
	add(s, "vm_push", "Copy a file, or a directory recursively, from the host into the guest. Files whose SHA-256 already matches the guest copy are skipped unless force is true.", func(ctx context.Context, in pushIn) (*mcp.CallToolResult, error) {
		c, err := m.Client(in.VM)
		if err != nil {
			return nil, err
		}
		n, skipped, sent, err := push(ctx, c, in.HostPath, in.GuestPath, in.Force)
		if err != nil {
			return nil, fmt.Errorf("%w (%d files copied)", err, n)
		}
		return text("%d files copied, %d unchanged skipped (%d bytes sent)", n, skipped, sent), nil
	})
	add(s, "vm_pull", "Copy a file, or a directory recursively, from the guest to the host.", func(ctx context.Context, in pullIn) (*mcp.CallToolResult, error) {
		c, err := m.Client(in.VM)
		if err != nil {
			return nil, err
		}
		n, size, err := pull(ctx, c, in.GuestPath, in.HostPath)
		if err != nil {
			return nil, fmt.Errorf("%w (%d files copied)", err, n)
		}
		return text("%d files (%d bytes) written to %s", n, size, in.HostPath), nil
	})
	add(s, "vm_clipboard_get", "Get the guest clipboard text.", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		var r proto.TextResult
		if _, err := call(ctx, in.VM, proto.OpClipboardGet, nil, nil, &r); err != nil {
			return nil, err
		}
		return text("%s", r.Text), nil
	})
	add(s, "vm_clipboard_set", "Set the guest clipboard text.", func(ctx context.Context, in typeIn) (*mcp.CallToolResult, error) {
		input.Lock()
		defer input.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, err := call(ctx, in.VM, proto.OpClipboardSet, proto.TextArgs{Text: in.Text}, nil, nil)
		return done(err)
	})
	add(s, "vm_focus_window", "Bring the unique matching visible window to the foreground by title, handle or pid. exact matches the full title; multiple matches are an error. Returns its title and handle.", func(ctx context.Context, in titleIn) (*mcp.CallToolResult, error) {
		input.Lock()
		defer input.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Resolve once: window handles are only meaningful in this guest.
		c, err := m.Client(in.VM)
		if err != nil {
			return nil, err
		}
		r, err := focusWindow(ctx, func(ctx context.Context, _, op string, args any, payload []byte, result any) ([]byte, error) {
			return c.Call(ctx, op, args, payload, result)
		}, in)
		if err != nil {
			return nil, u.lockedHint(ctx, in.VM, err)
		}
		return r, nil
	})
	add(s, "vm_wait", "Wait for a process or file condition, or for a unique visible window to appear, disappear (hidden or destroyed), or become foreground. Window conditions use title, handle or pid, with optional exact title matching. Timeout returns satisfied: false; multiple window matches are an error.", func(ctx context.Context, in waitIn) (*mcp.CallToolResult, error) {
		switch in.Kind {
		case "window_exists", "window_gone", "window_foreground":
			// Resolve the VM once so changing the default running VM cannot move a wait to another VM.
			c, err := m.Client(in.VM)
			if err != nil {
				return nil, err
			}
			return waitWindow(ctx, func(ctx context.Context, _, op string, args any, payload []byte, result any) ([]byte, error) {
				return c.Call(ctx, op, args, payload, result)
			}, in)
		}
		var r proto.WaitResult
		if _, err := call(ctx, in.VM, proto.OpWait, proto.WaitArgs{Kind: in.Kind, Name: in.Name, Path: in.Path, TimeoutMs: in.TimeoutMs}, nil, &r); err != nil {
			return nil, err
		}
		return text("satisfied: %v", r.Satisfied), nil
	})
	add(s, "vm_install_agent", "Install the HyperHand agent in the guest (copies it in and runs its installer via the keyboard; a user must be logged on), then wait until it answers. The install command is typed on the keyboard, so the guest IME must be in English mode; if it fails, check with vm_screenshot.",
		func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
			exe, err := agentExe()
			if err != nil {
				return nil, err
			}
			if err := backend.CopyToGuest(in.VM, exe, GuestAgentPath); err != nil {
				return nil, fmt.Errorf("copy agent: %w", err)
			}
			if err := backend.PressKeys(in.VM, "win+r"); err != nil {
				return nil, err
			}
			time.Sleep(1500 * time.Millisecond)
			if err := backend.TypeText(in.VM, GuestAgentPath+" install"); err != nil {
				return nil, err
			}
			if err := backend.PressKeys(in.VM, "enter"); err != nil {
				return nil, err
			}
			c, err := m.Client(in.VM)
			if err != nil {
				return nil, err
			}
			return waitPing(ctx, c)
		})
	add(s, "vm_update_agent", "Replace the guest agent with the hyperhand-agent.exe next to hyperhand.exe and wait until it is back.",
		func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
			exe, err := agentExe()
			if err != nil {
				return nil, err
			}
			data, err := os.ReadFile(exe)
			if err != nil {
				return nil, err
			}
			c, err := m.Client(in.VM)
			if err != nil {
				return nil, err
			}
			if _, err := c.Call(ctx, proto.OpUpdateAgent, nil, data, nil); err != nil {
				return nil, err
			}
			c.Close()
			time.Sleep(2 * time.Second)
			return waitPing(ctx, c)
		})
	return s
}

func agentExe() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(self), "hyperhand-agent.exe"), nil
}

// waitPing pings the agent for up to 30 s.
func waitPing(ctx context.Context, c *Client) (*mcp.CallToolResult, error) {
	var err error
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Second) {
		var p proto.PingResult
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = c.Call(pctx, proto.OpPing, nil, nil, &p)
		cancel()
		if err == nil {
			return text("agent %s running on %s as %s", p.Version, p.Hostname, p.User), nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.Join(errors.New("agent did not answer within 30 s"), err)
}

// pushTarget: a single file pushed to a guest path ending in \ or / goes into that directory under its own name.
func pushTarget(guestPath, name string) string {
	if strings.HasSuffix(guestPath, `\`) || strings.HasSuffix(guestPath, "/") {
		return guestPath + name
	}
	return guestPath
}

// pullTarget: a single guest file pulled to an existing host directory, or to a path ending in \ or /, goes into it
// under the guest file's name.
func pullTarget(hostPath, guestPath string) string {
	if fi, err := os.Stat(hostPath); err == nil && fi.IsDir() || strings.HasSuffix(hostPath, `\`) || strings.HasSuffix(hostPath, "/") {
		g := strings.TrimRight(guestPath, `\/`)
		return filepath.Join(hostPath, g[strings.LastIndexAny(g, `\/`)+1:])
	}
	return hostPath
}

// pull copies a guest file, or a guest directory recursively (walked with list_dir), to hostPath; returns files and bytes.
func pull(ctx context.Context, c *Client, guestPath, hostPath string) (files, size int, err error) {
	var ls proto.ListDirResult
	if _, err := c.Call(ctx, proto.OpListDir, proto.PathArgs{Path: guestPath}, nil, &ls); err != nil {
		hostPath = pullTarget(hostPath, guestPath)
		if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
			return 0, 0, err
		}
		n, err := pullFile(ctx, c, guestPath, hostPath)
		if err != nil {
			return 0, 0, fmt.Errorf("%s: %w", guestPath, err)
		}
		return 1, int(n), nil
	}
	if err := os.MkdirAll(hostPath, 0o755); err != nil {
		return 0, 0, err
	}
	for _, e := range ls.Entries {
		g, h := strings.TrimRight(guestPath, `\/`)+`\`+e.Name, filepath.Join(hostPath, e.Name)
		var n, b int
		if e.IsDir {
			n, b, err = pull(ctx, c, g, h)
		} else {
			var size int64
			if size, err = pullFile(ctx, c, g, h); err != nil {
				err = fmt.Errorf("%s: %w", g, err)
			} else {
				n, b = 1, int(size)
			}
		}
		files, size = files+n, size+b
		if err != nil {
			return files, size, err
		}
	}
	return files, size, nil
}

// push copies the host file or directory hostPath to guestPath. Unless force is set, files whose SHA-256 matches the
// guest's (asked once via hash_files; an agent without hash_files gets everything) are skipped.
func push(ctx context.Context, c *Client, hostPath, guestPath string, force bool) (copied, skipped int, sent int64, err error) {
	var srcs, dsts []string
	err = filepath.WalkDir(hostPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(hostPath, p)
		if err != nil {
			return err
		}
		dst := pushTarget(guestPath, filepath.Base(p))
		if rel != "." {
			dst = strings.TrimRight(guestPath, `\/`) + `\` + rel
		}
		srcs, dsts = append(srcs, p), append(dsts, dst)
		return nil
	})
	if err != nil {
		return 0, 0, 0, err
	}
	var guest []string
	if !force {
		for i := 0; i < len(dsts); i += 1000 {
			var r proto.HashesResult
			if _, err := c.Call(ctx, proto.OpHashFiles, proto.PathsArgs{Paths: dsts[i:min(i+1000, len(dsts))]}, nil, &r); err != nil {
				if strings.HasPrefix(err.Error(), "unknown op") { // older agent: upload everything
					guest = nil
					break
				}
				return 0, 0, 0, err
			}
			guest = append(guest, r.Hashes...)
		}
	}
	for i, src := range srcs {
		if i < len(guest) && guest[i] != "" {
			if h, err := hashFile(src); err != nil {
				return copied, skipped, sent, err
			} else if h == guest[i] {
				skipped++
				continue
			}
		}
		n, err := pushFile(ctx, c, src, dsts[i])
		if err != nil {
			return copied, skipped, sent, fmt.Errorf("%s: %w", dsts[i], err)
		}
		copied, sent = copied+1, sent+n
	}
	return copied, skipped, sent, nil
}

// hashFile returns the lowercase hex SHA-256 of the file at p.
func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pushFile streams the host file hostPath to guestPath.
func pushFile(ctx context.Context, c *Client, hostPath, guestPath string) (int64, error) {
	f, err := os.Open(hostPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if _, err = c.CallIO(ctx, proto.OpWriteFile, proto.PathArgs{Path: guestPath}, f, fi.Size(), nil, nil); err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// pullFile streams the guest file into a unique temporary file beside hostPath, then renames it over hostPath.
func pullFile(ctx context.Context, c *Client, guestPath, hostPath string) (int64, error) {
	f, err := os.CreateTemp(filepath.Dir(hostPath), ".hyperhand-*.hhpart")
	if err != nil {
		return 0, err
	}
	part := f.Name()
	w := bufio.NewWriterSize(f, 1<<20)
	n, err := c.CallIO(ctx, proto.OpReadFile, proto.PathArgs{Path: guestPath}, nil, 0, w, nil)
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(part, hostPath)
	}
	if err != nil {
		os.Remove(part)
		return 0, err
	}
	return n, nil
}
