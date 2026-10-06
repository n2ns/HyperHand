package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
	"hyperhand/internal/proto"
)

func TestControlsBounds(t *testing.T) {
	a, err := controlsArgs(proto.ControlsArgs{Handle: 1, PID: 2})
	if err != nil || a.MaxDepth != 4 || a.MaxNodes != 200 {
		t.Fatalf("defaults: %+v %v", a, err)
	}
	for _, a := range []proto.ControlsArgs{{PID: 1}, {Handle: 1}, {Handle: 1, PID: 1, MaxDepth: -1}, {Handle: 1, PID: 1, MaxDepth: 11}, {Handle: 1, PID: 1, MaxNodes: -1}, {Handle: 1, PID: 1, MaxNodes: 1001}} {
		if _, err := controlsArgs(a); err == nil {
			t.Errorf("accepted %+v", a)
		}
	}
	if handled, err := RunControlsHelper([]string{"install"}); handled || err != nil {
		t.Fatalf("claimed unrelated role: %v %v", handled, err)
	}
}

type fakeControl struct {
	name                        string
	child, sibling              *fakeControl
	password, cut               bool
	err                         error
	reads, releases, childCalls int
}

func (e *fakeControl) info() (proto.ControlInfo, bool, bool, error) {
	e.reads++
	return proto.ControlInfo{Name: e.name}, e.password, e.cut, e.err
}
func (e *fakeControl) first() (controlElement, error) {
	e.childCalls++
	if e.child == nil {
		return nil, nil
	}
	return e.child, nil
}
func (e *fakeControl) next() (controlElement, error) {
	if e.sibling == nil {
		return nil, nil
	}
	return e.sibling, nil
}
func (e *fakeControl) release() { e.releases++ }

func TestControlsTraversal(t *testing.T) {
	for _, mode := range []string{"full", "max_depth", "max_nodes", "password_subtree", "text_length", "error"} {
		t.Run(mode, func(t *testing.T) {
			leaf := &fakeControl{name: "leaf"}
			second := &fakeControl{name: "second"}
			first := &fakeControl{name: "first", child: leaf, sibling: second}
			root := &fakeControl{name: "root", child: first}
			a := proto.ControlsArgs{MaxDepth: 4, MaxNodes: 200}
			switch mode {
			case "max_depth":
				a.MaxDepth = 1
			case "max_nodes":
				a.MaxNodes = 2
			case "password_subtree":
				first.password = true
			case "text_length":
				first.cut = true
			case "error":
				first.err = errors.New("provider disappeared")
			}
			r, err := walkControls(root, a)
			if mode == "error" {
				if !errors.Is(err, first.err) || first.releases != 1 {
					t.Fatalf("%+v %v", r, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "full" {
				if len(r.Nodes) != 4 || r.Truncated || r.Nodes[2].Parent != 1 || r.Nodes[2].Depth != 2 || r.Nodes[3].Parent != 0 {
					t.Fatalf("tree: %+v", r)
				}
			} else if !r.Truncated || !slices.Contains(r.Truncation, mode) {
				t.Fatalf("missing truncation %s: %+v", mode, r)
			}
			if r.Nodes[0].Parent != -1 || r.Nodes[0].Index != 0 {
				t.Errorf("root: %+v", r.Nodes[0])
			}
			if mode == "max_nodes" && (len(r.Nodes) != 2 || leaf.reads != 0 || second.reads != 0) {
				t.Errorf("node limit ignored: %+v", r)
			}
			if mode == "password_subtree" && first.childCalls != 0 {
				t.Error("password subtree queried")
			}
			if first.releases != 1 || second.releases != 1 {
				t.Errorf("release counts: %d %d", first.releases, second.releases)
			}
		})
	}
}

func TestControlsHelperProcess(t *testing.T) {
	switch os.Getenv("HYPERHAND_CONTROLS_TEST_HELPER") {
	case "hang":
		time.Sleep(time.Minute)
		os.Exit(1)
	case "serve":
		_, err := RunControlsHelper([]string{controlsHelperRole})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func controlsTestCommand(t *testing.T, ctx context.Context, mode string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestControlsHelperProcess$")
	cmd.Env = append(os.Environ(), "HYPERHAND_CONTROLS_TEST_HELPER="+mode)
	return cmd
}

func TestControlsSubprocessCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runControlsCommand(ctx, controlsTestCommand(t, ctx, "hang"), proto.ControlsArgs{Handle: 1, PID: 1})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation: %v (%s)", err, time.Since(start))
	}
	// A subsequent request still starts and returns an ordinary target error.
	next, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_, err = runControlsCommand(next, controlsTestCommand(t, next, "serve"), proto.ControlsArgs{Handle: 1, PID: 1})
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("subsequent request: %v", err)
	}
}

func TestControlsNativeSnapshot(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h := offscreenWindow(t, "hyperhand-controls-test", 0, -30000)
	cls, _ := windows.UTF16PtrFromString("EDIT")
	secret, _ := windows.UTF16PtrFromString("controls-test-password-secret")
	child, _, childErr := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(secret)), 0x40000000|0x10000000|0x20, 10, 10, 100, 20, uintptr(h), 0, 0, 0)
	if child == 0 {
		t.Fatal(childErr)
	}
	a := proto.ControlsArgs{Handle: uint64(h), PID: uint32(os.Getpid()), MaxDepth: 2, MaxNodes: 20}
	wrong := a
	wrong.PID++
	if err := validateControlsWindow(wrong); err == nil {
		t.Fatal("wrong PID accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := controlsTestCommand(t, ctx, "serve")
	type outcome struct {
		r   proto.ControlsResult
		err error
	}
	done := make(chan outcome, 1)
	go func() { r, err := runControlsCommand(ctx, cmd, a); done <- outcome{r, err} }()
	// The target test window must service provider messages while the helper queries it.
	for {
		select {
		case out := <-done:
			if out.err != nil {
				t.Fatal(out.err)
			}
			if len(out.r.Nodes) == 0 {
				t.Fatal("no root")
			}
			data, _ := json.Marshal(out.r)
			if strings.Contains(string(data), "controls-test-password-secret") {
				t.Fatal("password text leaked")
			}
			foundEdit := false
			for _, node := range out.r.Nodes {
				if node.ControlType == 50004 {
					foundEdit = true
					if node.Name != "" {
						t.Errorf("password name not redacted: %q", node.Name)
					}
				}
			}
			if !foundEdit {
				t.Fatal("password edit control missing")
			}
			n := out.r.Nodes[0]
			if n.PID != a.PID || n.Name != "hyperhand-controls-test" || n.Rect.Left != -30000 {
				t.Fatalf("root: %+v", n)
			}
			return
		default:
			var msg win.MSG
			for win.PeekMessage(&msg, 0, 0, 0, co.PM_REMOVE) {
				win.TranslateMessage(&msg)
				win.DispatchMessage(&msg)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestControlsOutputBound(t *testing.T) {
	var out controlsBuffer
	// Exercise io.Copy too: embedding bytes.Buffer would expose ReadFrom and bypass Write's limit.
	_, err := io.Copy(&out, io.LimitReader(bytes.NewReader(make([]byte, controlsOutputLimit+1)), controlsOutputLimit+1))
	if err == nil || len(out.Bytes()) > controlsOutputLimit {
		t.Fatalf("output limit: bytes=%d err=%v", len(out.Bytes()), err)
	}
}
