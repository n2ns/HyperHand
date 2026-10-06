package host

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

type inputMCPBackend struct {
	*windowMCPBackend
	press    func(string, string) error
	typeText func(string, string) error
}

func (b *inputMCPBackend) PressKeys(vm, keys string) error { return b.press(vm, keys) }
func (b *inputMCPBackend) TypeText(vm, text string) error  { return b.typeText(vm, text) }

func connectInputMCP(t *testing.T, ctx context.Context, b Backend) *mcp.ClientSession {
	t.Helper()
	manager := &Manager{Backend: b}
	t.Cleanup(func() { manager.Drop("A"); manager.Drop("B") })
	st, ct := mcp.NewInMemoryTransports()
	s, err := NewServer(manager).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c, err := mcp.NewClient(&mcp.Implementation{Name: "input-test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestInputMCPKeysPinsTargetAndPreservesClipboard(t *testing.T) {
	for _, mode := range []string{"success", "old-agent", "background"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			inputText := "汉字😀"
			if mode == "old-agent" {
				inputText = "plain ASCII must not fall back"
			}
			var defaults, sent, forbidden atomic.Int32
			b := &inputMCPBackend{windowMCPBackend: &windowMCPBackend{
				find: func(name string) (hyperv.VM, error) {
					id := "A"
					if name == "" && defaults.Add(1) > 1 {
						id = "B"
					}
					return hyperv.VM{ID: id, Name: id}, nil
				},
				respond: func(id string, req proto.Request) (any, error) {
					if id != "A" {
						return nil, fmt.Errorf("changed VM to %s", id)
					}
					switch req.Op {
					case proto.OpListWindows:
						return proto.WindowsResult{Windows: []proto.WindowInfo{{Handle: 17, PID: 42, Title: "Editor", Enabled: true, Foreground: mode != "background"}}}, nil
					case proto.OpTypeKeys:
						sent.Add(1)
						var a proto.TypeKeysArgs
						if err := json.Unmarshal(req.Args, &a); err != nil {
							return nil, err
						}
						if a != (proto.TypeKeysArgs{Text: inputText, Handle: 17, PID: 42}) {
							return nil, fmt.Errorf("wrong input target: %+v", a)
						}
						if mode == "old-agent" {
							return nil, fmt.Errorf("unknown op type_keys")
						}
						return proto.TypeKeysResult{Events: 8}, nil
					default:
						forbidden.Add(1)
						return nil, fmt.Errorf("unexpected operation %s", req.Op)
					}
				},
			}, press: func(string, string) error { forbidden.Add(1); return nil }, typeText: func(string, string) error { forbidden.Add(1); return nil }}
			cs := connectInputMCP(t, ctx, b)
			r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_type", Arguments: map[string]any{"text": inputText, "mode": "keys", "handle": 17, "pid": 42}})
			if err != nil {
				t.Fatal(err)
			}
			if r.IsError != (mode != "success") {
				t.Fatalf("result: %s", resultText(r))
			}
			if mode == "old-agent" && !strings.Contains(resultText(r), "unknown op") {
				t.Fatalf("lost agent error: %s", resultText(r))
			}
			wantSent := int32(1)
			if mode == "background" {
				wantSent = 0
			}
			if defaults.Load() != 1 || sent.Load() != wantSent || forbidden.Load() != 0 {
				t.Fatalf("default resolutions=%d sends=%d forbidden=%d", defaults.Load(), sent.Load(), forbidden.Load())
			}
		})
	}
}

func TestInputMCPSequenceValidatesBeforeSending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var operations atomic.Int32
	b := &inputMCPBackend{windowMCPBackend: &windowMCPBackend{
		find: func(string) (hyperv.VM, error) { operations.Add(1); return hyperv.VM{ID: "A", Name: "A"}, nil },
	}, press: func(string, string) error { operations.Add(1); return nil }}
	cs := connectInputMCP(t, ctx, b)
	for _, args := range []map[string]any{
		{"sequence": []string{"ctrl+a", "not-a-key"}},
		{"keys": "enter", "sequence": []string{"ctrl+a"}},
		{"sequence": []string{}},
	} {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_key", Arguments: args})
		if err == nil && !r.IsError {
			t.Fatalf("accepted invalid input: %+v", args)
		}
	}
	if operations.Load() != 0 {
		t.Fatalf("invalid sequence performed %d operations", operations.Load())
	}
}

func TestInputMCPSequenceOrderAndChangedTarget(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var mu sync.Mutex
			var pressed []string
			var polls, defaults atomic.Int32
			b := &inputMCPBackend{windowMCPBackend: &windowMCPBackend{
				find: func(name string) (hyperv.VM, error) {
					if name == "" {
						defaults.Add(1)
					}
					return hyperv.VM{ID: "A", Name: "A"}, nil
				},
				respond: func(_ string, req proto.Request) (any, error) {
					if req.Op != proto.OpListWindows {
						return nil, fmt.Errorf("unexpected operation %s", req.Op)
					}
					pid := uint32(42)
					if polls.Add(1) > 1 && changed {
						pid = 99
					}
					return proto.WindowsResult{Windows: []proto.WindowInfo{{Handle: 17, PID: pid, Title: "Editor", Enabled: true, Foreground: true}}}, nil
				},
			}, press: func(vm, key string) error {
				if vm != "A" {
					return fmt.Errorf("target VM was not pinned")
				}
				mu.Lock()
				defer mu.Unlock()
				pressed = append(pressed, key)
				return nil
			}}
			cs := connectInputMCP(t, ctx, b)
			r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_key", Arguments: map[string]any{"window": "Editor", "sequence": []string{"ctrl+a", "tab", "enter"}}})
			if err != nil {
				t.Fatal(err)
			}
			if r.IsError != changed {
				t.Fatalf("result: %s", resultText(r))
			}
			want := []string{"ctrl+a", "tab", "enter"}
			if changed {
				want = want[:1]
				if !strings.Contains(resultText(r), "after 1 combinations") {
					t.Fatalf("missing partial progress: %s", resultText(r))
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(pressed, want) || defaults.Load() != 1 {
				t.Fatalf("pressed=%v default resolutions=%d", pressed, defaults.Load())
			}
		})
	}
}
