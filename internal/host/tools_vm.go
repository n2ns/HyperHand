package host

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/credential"
	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

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
type waitIn struct {
	VM        string `json:"vm,omitempty" jsonschema:"VM name; default: the only running VM"`
	Kind      string `json:"kind" jsonschema:"process_exit or process_running (use name), or file_exists (use path)"`
	Name      string `json:"name,omitempty" jsonschema:"process name, e.g. notepad"`
	Path      string `json:"path,omitempty" jsonschema:"file path in the guest"`
	TimeoutMs int    `json:"timeout_ms,omitempty" jsonschema:"default 60000"`
}

// registerVM registers the VM, checkpoint, command, file, clipboard, wait and agent tools.
func registerVM(d *deps) {
	s, m, raw, backend, input, call, u := d.s, d.m, d.raw, d.backend, d.input, d.call, d.u
	_, _, _ = raw, input, u
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
	add(s, "vm_wait", "Wait until a process runs or exits, or a file exists. Timeout returns satisfied: false.", func(ctx context.Context, in waitIn) (*mcp.CallToolResult, error) {
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
