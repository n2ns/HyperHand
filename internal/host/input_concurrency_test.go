package host

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

func TestInputMCPConcurrentMutationsWaitForKeys(t *testing.T) {
	for _, tool := range []string{"vm_clipboard_set", "vm_click"} {
		t.Run(tool, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var holding, changed atomic.Bool
			var interleaved, checks, clicks atomic.Int32
			observe := func() {
				if holding.Load() {
					interleaved.Add(1)
				}
			}
			b := &inputMCPBackend{windowMCPBackend: &windowMCPBackend{
				find: func(string) (hyperv.VM, error) { return hyperv.VM{ID: "A", Name: "A"}, nil },
				respond: func(_ string, req proto.Request) (any, error) {
					observe()
					switch req.Op {
					case proto.OpListWindows:
						checks.Add(1)
						return proto.WindowsResult{Windows: []proto.WindowInfo{{Handle: 17, PID: 42, Title: "Editor", Enabled: true, Foreground: !changed.Load(), Rect: proto.Rect{Right: 100, Bottom: 100}}}}, nil
					case proto.OpFocusWindow:
						return proto.FocusResult{Handle: 17, Text: "Editor"}, nil
					case proto.OpClipboardSet:
						return nil, nil
					case proto.OpWindowAt:
						return proto.HandleResult{Handle: 17}, nil
					case proto.OpSessionState:
						return proto.SessionStateResult{Console: true}, nil
					default:
						return nil, fmt.Errorf("unexpected op %s", req.Op)
					}
				},
				click: func(string, int, int, int, int, []string) error { observe(); clicks.Add(1); return nil },
			}, press: func(string, string) error {
				holding.Store(true)
				close(entered)
				<-release
				// The running key operation changed the foreground window before releasing its lock.
				changed.Store(true)
				holding.Store(false)
				return nil
			}}
			cs := connectInputMCP(t, ctx, b)
			keysDone := make(chan error, 1)
			go func() {
				r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_key", Arguments: map[string]any{"keys": "tab", "vm": "A"}})
				if err == nil && r.IsError {
					err = fmt.Errorf("keys: %s", resultText(r))
				}
				keysDone <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			args := map[string]any{"vm": "A"}
			switch tool {
			case "vm_clipboard_set":
				args["text"] = "new clipboard"
			case "vm_click":
				args["handle"], args["x"], args["y"] = 17, 1, 2
			}
			type outcome struct {
				result *mcp.CallToolResult
				err    error
			}
			pending := make(chan outcome, 1)
			go func() {
				r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
				pending <- outcome{r, err}
			}()
			// A blocked request must not execute guest mutations or even the click's precondition query.
			select {
			case out := <-pending:
				t.Fatalf("%s finished while keys held the lock: %+v %v", tool, out.result, out.err)
			case <-time.After(100 * time.Millisecond):
			}
			if interleaved.Load() != 0 || checks.Load() != 0 || clicks.Load() != 0 {
				t.Fatalf("operation entered key critical section: guest=%d checks=%d clicks=%d", interleaved.Load(), checks.Load(), clicks.Load())
			}
			unblock()
			if err := <-keysDone; err != nil {
				t.Fatal(err)
			}
			out := <-pending
			if out.err != nil {
				t.Fatal(out.err)
			}
			if tool == "vm_click" {
				if !out.result.IsError || !strings.Contains(resultText(out.result), "foreground") || clicks.Load() != 0 {
					t.Fatalf("stale click was not refused: %s clicks=%d", resultText(out.result), clicks.Load())
				}
			} else if out.result.IsError {
				t.Fatalf("%s after unlock: %s", tool, resultText(out.result))
			}
		})
	}
}
