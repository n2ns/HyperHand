package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

// GuestAgentPath is where vm_install_agent puts the agent in the guest.
const GuestAgentPath = `C:\Users\Public\HyperHand\hyperhand-agent.exe`

type vmIn struct {
	VM string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
}
type screenshotIn struct {
	VM     string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Source string `json:"source,omitempty" jsonschema:"host (default: Hyper-V console, works without the agent) or agent (taken inside the guest)"`
}
type clickIn struct {
	VM     string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Button string `json:"button,omitempty" jsonschema:"left (default), right or middle"`
	Double bool   `json:"double,omitempty" jsonschema:"double click"`
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
type keyIn struct {
	VM   string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Keys string `json:"keys" jsonschema:"a key or combination, e.g. enter, esc, tab, f2, ctrl+v, win+r, alt+f4"`
}
type execIn struct {
	VM        string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Command   string `json:"command"`
	Shell     string `json:"shell,omitempty" jsonschema:"powershell (default) or cmd"`
	Cwd       string `json:"cwd,omitempty" jsonschema:"working directory in the guest"`
	TimeoutMs int    `json:"timeout_ms,omitempty" jsonschema:"default 60000"`
}
type pushIn struct {
	VM        string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	HostPath  string `json:"host_path" jsonschema:"file or directory on the host"`
	GuestPath string `json:"guest_path" jsonschema:"destination file or directory in the guest"`
}
type pullIn struct {
	VM        string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	GuestPath string `json:"guest_path" jsonschema:"file in the guest"`
	HostPath  string `json:"host_path" jsonschema:"destination file on the host"`
}
type titleIn struct {
	VM    string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Title string `json:"title" jsonschema:"case-insensitive substring of the window title"`
}
type waitIn struct {
	VM        string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Kind      string `json:"kind" jsonschema:"process_exit, process_running (use name) or file_exists (use path)"`
	Name      string `json:"name,omitempty" jsonschema:"process name, e.g. acad"`
	Path      string `json:"path,omitempty" jsonschema:"file path in the guest"`
	TimeoutMs int    `json:"timeout_ms,omitempty" jsonschema:"default 60000"`
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
	s := mcp.NewServer(&mcp.Implementation{Name: "hyperhand", Version: proto.Version}, nil)
	call := func(ctx context.Context, vm, op string, args any, payload []byte, result any) ([]byte, error) {
		c, err := m.Client(vm)
		if err != nil {
			return nil, err
		}
		return c.Call(ctx, op, args, payload, result)
	}

	add(s, "vm_list", "List Hyper-V VMs (name, state, id).", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		vms, err := hyperv.ListVMs()
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
	add(s, "vm_start", "Start a VM.", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		return done(hyperv.Start(in.VM))
	})
	add(s, "vm_stop", "Turn off a VM.", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		return done(hyperv.Stop(in.VM))
	})
	add(s, "vm_screenshot", "Take a PNG screenshot of the VM screen. Its pixel coordinates are the coordinates for vm_click, vm_drag and vm_scroll.",
		func(ctx context.Context, in screenshotIn) (*mcp.CallToolResult, error) {
			var png []byte
			var w, h int
			var err error
			switch in.Source {
			case "", "host":
				png, w, h, err = hyperv.Screenshot(in.VM)
			case "agent":
				if png, err = call(ctx, in.VM, proto.OpScreenshot, nil, nil, nil); err == nil {
					var cfg image.Config
					cfg, _, err = image.DecodeConfig(bytes.NewReader(png))
					w, h = cfg.Width, cfg.Height
				}
			default:
				err = fmt.Errorf("unknown source %q", in.Source)
			}
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.ImageContent{Data: png, MIMEType: "image/png"},
				&mcp.TextContent{Text: fmt.Sprintf("%dx%d", w, h)},
			}}, nil
		})
	add(s, "vm_click", "Click at screen pixel (x, y).", func(ctx context.Context, in clickIn) (*mcp.CallToolResult, error) {
		b := map[string]int{"": 1, "left": 1, "right": 2, "middle": 3}[in.Button]
		if b == 0 {
			return nil, fmt.Errorf("unknown button %q", in.Button)
		}
		return done(hyperv.Click(in.VM, in.X, in.Y, b, in.Double))
	})
	add(s, "vm_drag", "Drag with the left button from (x1, y1) to (x2, y2).", func(ctx context.Context, in dragIn) (*mcp.CallToolResult, error) {
		return done(hyperv.Drag(in.VM, in.X1, in.Y1, in.X2, in.Y2))
	})
	add(s, "vm_scroll", "Scroll the mouse wheel at (x, y).", func(ctx context.Context, in scrollIn) (*mcp.CallToolResult, error) {
		return done(hyperv.Scroll(in.VM, in.X, in.Y, in.Delta))
	})
	add(s, "vm_type", "Type text into the focused window. Non-ASCII text is pasted through the clipboard (needs the agent).",
		func(ctx context.Context, in typeIn) (*mcp.CallToolResult, error) {
			ascii := true
			for _, r := range in.Text {
				ascii = ascii && r < 128
			}
			if ascii {
				return done(hyperv.TypeText(in.VM, in.Text))
			}
			if _, err := call(ctx, in.VM, proto.OpClipboardSet, proto.TextArgs{Text: in.Text}, nil, nil); err != nil {
				return nil, err
			}
			return done(hyperv.PressKeys(in.VM, "ctrl+v"))
		})
	add(s, "vm_key", "Press a key or key combination.", func(ctx context.Context, in keyIn) (*mcp.CallToolResult, error) {
		return done(hyperv.PressKeys(in.VM, in.Keys))
	})
	add(s, "vm_exec", "Run a command in the guest (as the logged-on user); returns exit code, stdout and stderr.",
		func(ctx context.Context, in execIn) (*mcp.CallToolResult, error) {
			var r proto.ExecResult
			if _, err := call(ctx, in.VM, proto.OpExec, proto.ExecArgs{Command: in.Command, Shell: in.Shell, Cwd: in.Cwd, TimeoutMs: in.TimeoutMs}, nil, &r); err != nil {
				return nil, err
			}
			return text("exit_code: %d\ntimed_out: %v\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.TimedOut, r.Stdout, r.Stderr), nil
		})
	add(s, "vm_push", "Copy a file, or a directory recursively, from the host into the guest.", func(ctx context.Context, in pushIn) (*mcp.CallToolResult, error) {
		n := 0
		err := filepath.WalkDir(in.HostPath, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(in.HostPath, p)
			if err != nil {
				return err
			}
			dst := in.GuestPath
			if rel != "." {
				dst = strings.TrimRight(in.GuestPath, `\/`) + `\` + rel
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if _, err := call(ctx, in.VM, proto.OpWriteFile, proto.PathArgs{Path: dst}, data, nil); err != nil {
				return fmt.Errorf("%s: %w", dst, err)
			}
			n++
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("%w (%d files copied)", err, n)
		}
		return text("%d files copied", n), nil
	})
	add(s, "vm_pull", "Copy a file from the guest to the host.", func(ctx context.Context, in pullIn) (*mcp.CallToolResult, error) {
		data, err := call(ctx, in.VM, proto.OpReadFile, proto.PathArgs{Path: in.GuestPath}, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(in.HostPath), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(in.HostPath, data, 0o644); err != nil {
			return nil, err
		}
		return text("%d bytes written to %s", len(data), in.HostPath), nil
	})
	add(s, "vm_clipboard_get", "Get the guest clipboard text.", func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		var r proto.TextResult
		if _, err := call(ctx, in.VM, proto.OpClipboardGet, nil, nil, &r); err != nil {
			return nil, err
		}
		return text("%s", r.Text), nil
	})
	add(s, "vm_clipboard_set", "Set the guest clipboard text.", func(ctx context.Context, in typeIn) (*mcp.CallToolResult, error) {
		_, err := call(ctx, in.VM, proto.OpClipboardSet, proto.TextArgs{Text: in.Text}, nil, nil)
		return done(err)
	})
	add(s, "vm_focus_window", "Bring the first window whose title contains the text to the foreground.", func(ctx context.Context, in titleIn) (*mcp.CallToolResult, error) {
		var r proto.TextResult
		if _, err := call(ctx, in.VM, proto.OpFocusWindow, proto.TitleArgs{Title: in.Title}, nil, &r); err != nil {
			return nil, err
		}
		return text("focused: %s", r.Text), nil
	})
	add(s, "vm_wait", "Wait in the guest until a process exits, a process is running, or a file exists.", func(ctx context.Context, in waitIn) (*mcp.CallToolResult, error) {
		var r proto.WaitResult
		if _, err := call(ctx, in.VM, proto.OpWait, proto.WaitArgs{Kind: in.Kind, Name: in.Name, Path: in.Path, TimeoutMs: in.TimeoutMs}, nil, &r); err != nil {
			return nil, err
		}
		return text("satisfied: %v", r.Satisfied), nil
	})
	add(s, "vm_install_agent", "Install the HyperHand agent in the guest (copies it in and runs its installer via the keyboard; a user must be logged on), then wait until it answers.",
		func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
			exe, err := agentExe()
			if err != nil {
				return nil, err
			}
			if err := hyperv.CopyToGuest(in.VM, exe, GuestAgentPath); err != nil {
				return nil, fmt.Errorf("copy agent: %w", err)
			}
			if err := hyperv.PressKeys(in.VM, "win+r"); err != nil {
				return nil, err
			}
			time.Sleep(1500 * time.Millisecond)
			if err := hyperv.TypeText(in.VM, GuestAgentPath+" install"); err != nil {
				return nil, err
			}
			if err := hyperv.PressKeys(in.VM, "enter"); err != nil {
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
