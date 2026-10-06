package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

type windowMCPBackend struct {
	Backend
	find    func(string) (hyperv.VM, error)
	respond func(string, proto.Request) (any, error)
	click   func(string, int, int, int, bool) error
}

func (b *windowMCPBackend) Find(name string) (hyperv.VM, error) { return b.find(name) }
func (b *windowMCPBackend) Click(vm string, x, y, button int, double bool) error {
	return b.click(vm, x, y, button, double)
}
func (b *windowMCPBackend) Dial(_ context.Context, id string) (net.Conn, error) {
	client, guest := net.Pipe()
	go func() {
		defer guest.Close()
		for {
			var req proto.Request
			if _, err := proto.ReadFrame(guest, &req); err != nil {
				return
			}
			result, err := b.respond(id, req)
			response := proto.Response{}
			if err != nil {
				response.Error = err.Error()
			} else {
				response.Result, _ = json.Marshal(result)
			}
			if err := proto.WriteFrame(guest, response, nil); err != nil {
				return
			}
		}
	}()
	return client, nil
}

func connectWindowMCP(t *testing.T, ctx context.Context, b *windowMCPBackend) *mcp.ClientSession {
	t.Helper()
	manager := &Manager{Backend: b}
	t.Cleanup(func() { manager.Drop("A"); manager.Drop("B") })
	st, ct := mcp.NewInMemoryTransports()
	server, err := NewServer(manager).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	client, err := mcp.NewClient(&mcp.Implementation{Name: "window-test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestWindowMCPSelectorSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var clicks atomic.Int32
	b := &windowMCPBackend{
		find: func(string) (hyperv.VM, error) { return hyperv.VM{ID: "A", Name: "CAD"}, nil },
		respond: func(_ string, req proto.Request) (any, error) {
			switch req.Op {
			case proto.OpListWindows:
				return proto.WindowsResult{Windows: testWindows()}, nil
			case proto.OpFocusWindow:
				var args proto.TitleArgs
				if err := json.Unmarshal(req.Args, &args); err != nil {
					return nil, err
				}
				if args != (proto.TitleArgs{Handle: 10}) {
					return nil, fmt.Errorf("unexpected focus args: %+v", args)
				}
				return proto.FocusResult{Handle: 10, Text: "Options"}, nil
			case proto.OpWindowAt:
				return proto.HandleResult{Handle: 10}, nil
			default:
				return nil, fmt.Errorf("unexpected guest op %q", req.Op)
			}
		},
		click: func(_ string, x, y, button int, double bool) error {
			if x != 101 || y != 52 || button != 1 || double {
				return fmt.Errorf("unexpected click: %d %d %d %v", x, y, button, double)
			}
			clicks.Add(1)
			return nil
		},
	}
	cs := connectWindowMCP(t, ctx, b)
	for _, tt := range []struct {
		name string
		args map[string]any
	}{
		{"vm_focus_window", map[string]any{"title": "OPTIONS", "pid": 100, "exact": true}},
		{"vm_click", map[string]any{"window": "OPTIONS", "pid": 100, "exact": true, "x": 1, "y": 2}},
		{"vm_wait", map[string]any{"kind": "window_foreground", "title": "OPTIONS", "handle": 10, "pid": 100, "exact": true, "timeout_ms": 100}},
	} {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tt.name, Arguments: tt.args})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if r.IsError {
			t.Fatalf("%s: %s", tt.name, resultText(r))
		}
	}
	if clicks.Load() != 1 {
		t.Errorf("click count: %d", clicks.Load())
	}
}

func TestWindowMCPWaitReleasesAgentAndPinsVM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstPoll := make(chan struct{})
	var polls, defaultFinds, otherVMCalls atomic.Int32
	var clipboardRead atomic.Bool
	b := &windowMCPBackend{
		find: func(name string) (hyperv.VM, error) {
			id := "A"
			if name == "" && defaultFinds.Add(1) > 1 {
				id = "B"
			}
			return hyperv.VM{ID: id, Name: id}, nil
		},
		respond: func(id string, req proto.Request) (any, error) {
			if id != "A" {
				otherVMCalls.Add(1)
			}
			switch req.Op {
			case proto.OpListWindows:
				if polls.Add(1) == 1 {
					close(firstPoll)
				}
				if clipboardRead.Load() {
					return proto.WindowsResult{Windows: testWindows()}, nil
				}
				return proto.WindowsResult{}, nil
			case proto.OpClipboardGet:
				clipboardRead.Store(true)
				return proto.TextResult{Text: "concurrent request completed"}, nil
			default:
				return nil, fmt.Errorf("window wait must not send guest op %q", req.Op)
			}
		},
	}
	cs := connectWindowMCP(t, ctx, b)
	done := make(chan error, 1)
	go func() {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_wait", Arguments: map[string]any{"kind": "window_exists", "title": "Options", "timeout_ms": 2000}})
		if err == nil && (r.IsError || !strings.Contains(resultText(r), "satisfied: true")) {
			err = fmt.Errorf("wait result: %s", resultText(r))
		}
		done <- err
	}()
	select {
	case <-firstPoll:
	case err := <-done:
		t.Fatalf("wait ended before first poll: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// This targets the same agent; completion is also what makes the pending window appear.
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_clipboard_get", Arguments: map[string]any{"vm": "A"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError || resultText(r) != "concurrent request completed" {
		t.Fatalf("concurrent request: %s", resultText(r))
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if polls.Load() < 2 {
		t.Errorf("wait never polled again: %d", polls.Load())
	}
	if defaultFinds.Load() != 1 || otherVMCalls.Load() != 0 {
		t.Errorf("wait changed VM: default resolutions=%d, other VM calls=%d", defaultFinds.Load(), otherVMCalls.Load())
	}
}

func TestWindowMCPActionsPinVM(t *testing.T) {
	for _, tool := range []string{"vm_focus_window", "vm_click"} {
		t.Run(tool, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var defaultFinds, actions atomic.Int32
			b := &windowMCPBackend{
				find: func(name string) (hyperv.VM, error) {
					id := name
					if name == "" {
						id = "A"
						if defaultFinds.Add(1) > 1 {
							id = "B"
						}
					}
					return hyperv.VM{ID: id, Name: id}, nil
				},
				respond: func(id string, req proto.Request) (any, error) {
					if id != "A" {
						return nil, fmt.Errorf("window operation moved to VM %s", id)
					}
					switch req.Op {
					case proto.OpListWindows:
						return proto.WindowsResult{Windows: testWindows()}, nil
					case proto.OpWindowAt:
						return proto.HandleResult{Handle: 10}, nil
					case proto.OpFocusWindow:
						actions.Add(1)
						return proto.FocusResult{Handle: 10, Text: "Options"}, nil
					default:
						return nil, fmt.Errorf("unexpected op %q", req.Op)
					}
				},
				click: func(vm string, x, y, button int, double bool) error {
					if vm != "A" {
						return fmt.Errorf("click did not pin VM: %q", vm)
					}
					actions.Add(1)
					return nil
				},
			}
			cs := connectWindowMCP(t, ctx, b)
			args := map[string]any{"pid": 100, "exact": true}
			if tool == "vm_click" {
				args["window"], args["x"], args["y"] = "Options", 1, 2
			} else {
				args["title"] = "Options"
			}
			r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if r.IsError {
				t.Fatalf("%s: %s", tool, resultText(r))
			}
			if defaultFinds.Load() != 1 || actions.Load() != 1 {
				t.Errorf("default resolutions=%d actions=%d", defaultFinds.Load(), actions.Load())
			}
		})
	}
}
