package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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
	click   func(string, int, int, int, int, []string) error
}

func (b *windowMCPBackend) Find(name string) (hyperv.VM, error) { return b.find(name) }
func (b *windowMCPBackend) Click(vm string, x, y, button, count int, modifiers []string) error {
	return b.click(vm, x, y, button, count, modifiers)
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
		click: func(_ string, x, y, button, count int, _ []string) error {
			if x != 101 || y != 52 || button != 1 || count != 1 {
				return fmt.Errorf("unexpected click: %d %d %d %d", x, y, button, count)
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
		{"vm_click", map[string]any{"window": "OPTIONS", "pid": 100, "exact": true, "x": 1, "y": 2}},
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

func TestWindowMCPActionsPinVM(t *testing.T) {
	for _, tool := range []string{"vm_click"} {
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
				click: func(vm string, x, y, button, count int, _ []string) error {
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
