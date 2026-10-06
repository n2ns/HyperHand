package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

func TestStatusDoesNotInterruptBusyAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	stop := sync.OnceFunc(func() { close(release) })
	defer stop()
	b := &windowMCPBackend{
		find: func(string) (hyperv.VM, error) { return hyperv.VM{ID: "A", Name: "A", State: "Running"}, nil },
		respond: func(_ string, req proto.Request) (any, error) {
			switch req.Op {
			case proto.OpExec:
				close(started)
				<-release
				return proto.ExecResult{Stdout: "completed"}, nil
			case proto.OpPing:
				return proto.PingResult{Version: "test"}, nil
			case proto.OpSessionState:
				return proto.SessionStateResult{Console: true}, nil
			default:
				return nil, fmt.Errorf("unexpected op %s", req.Op)
			}
		},
	}
	cs := connectWindowMCP(t, ctx, b)
	done := make(chan error, 1)
	go func() {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_exec", Arguments: map[string]any{"vm": "A", "command": "test"}})
		if err == nil && (r.IsError || !strings.Contains(resultText(r), "completed")) {
			err = fmt.Errorf("exec result: %s", resultText(r))
		}
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
	defer probeCancel()
	r, err := cs.CallTool(probeCtx, &mcp.CallToolParams{Name: "vm_status", Arguments: map[string]any{"vm": "A"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError || !strings.Contains(resultText(r), "agent: busy") {
		t.Fatalf("status: %s", resultText(r))
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_status", Arguments: json.RawMessage(`{"vm":"A"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError || !strings.Contains(resultText(r), "session: unlocked") {
		t.Fatalf("idle status: %s", resultText(r))
	}
}
