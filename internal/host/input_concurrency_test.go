package host

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

// A tool that sends input or touches the clipboard waits for a running key operation to release the input lock, and
// its own checks run only afterwards, against the state the key operation left behind.
func TestInputMCPConcurrentMutationsWaitForKeys(t *testing.T) {
	for _, tool := range []string{"vm_clipboard_set", "vm_type"} {
		t.Run(tool, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var holding, changed atomic.Bool
			var interleaved, checks, typed atomic.Int32
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
						return proto.WindowsResult{Windows: []proto.WindowInfo{{Handle: 17, PID: 42, Title: "Editor", Enabled: true, Foreground: !changed.Load(), Rect: proto.Rect{Right: 100, Bottom: 100}}}, Session: &proto.SessionStateResult{Console: true}}, nil
					case proto.OpClipboardSet:
						return nil, nil
					case proto.OpTypeKeys:
						typed.Add(1)
						return proto.TypeKeysResult{Events: 2}, nil
					default:
						return nil, fmt.Errorf("unexpected op %s", req.Op)
					}
				},
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
			checksBefore := checks.Load() // the key operation's own session check
			args := map[string]any{"vm": "A"}
			switch tool {
			case "vm_clipboard_set":
				args["text"] = "new clipboard"
			case "vm_type":
				args["handle"], args["text"], args["activate"] = 17, "x", false
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
			// A blocked request must not execute guest mutations or even its precondition query.
			select {
			case out := <-pending:
				t.Fatalf("%s finished while keys held the lock: %+v %v", tool, out.result, out.err)
			case <-time.After(100 * time.Millisecond):
			}
			if interleaved.Load() != 0 || checks.Load() != checksBefore || typed.Load() != 0 {
				t.Fatalf("operation entered key critical section: guest=%d checks=%d typed=%d", interleaved.Load(), checks.Load()-checksBefore, typed.Load())
			}
			unblock()
			if err := <-keysDone; err != nil {
				t.Fatal(err)
			}
			out := <-pending
			if out.err != nil {
				t.Fatal(out.err)
			}
			if tool == "vm_type" {
				if m := resultJSON(t, out.result); !out.result.IsError || m["error"] != codeActivateFailed || typed.Load() != 0 {
					t.Fatalf("stale input was not refused: %v typed=%d", m, typed.Load())
				}
			} else if out.result.IsError {
				t.Fatalf("%s after unlock: %s", tool, resultText(out.result))
			}
		})
	}
}
