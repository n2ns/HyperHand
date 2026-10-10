package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"hyperhand/internal/hyperv"
)

func TestSchemaErrorsAreStructuredBeforeVMAccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	vmCalls := 0
	cs := connectWindowMCP(t, ctx, &windowMCPBackend{find: func(string) (hyperv.VM, error) {
		vmCalls++
		return hyperv.VM{}, nil
	}})
	for _, tc := range []struct {
		name     string
		args     any
		wantTask bool
	}{
		{"misspelled required field", map[string]any{"condition": "unknown", "task_id": "schema-task"}, true},
		{"missing required field", map[string]any{"task_id": "schema-task"}, true},
		{"wrong kind type", map[string]any{"kind": 1, "task_id": "schema-task"}, true},
		{"wrong timeout type", map[string]any{"kind": "file_exists", "timeout_ms": "10", "task_id": "schema-task"}, true},
		{"wrong vm type", map[string]any{"kind": "file_exists", "vm": 1, "task_id": "schema-task"}, true},
		{"wrong task type", map[string]any{"kind": "file_exists", "task_id": 1}, false},
		{"unknown property", map[string]any{"kind": "file_exists", "condition": "unknown", "task_id": "schema-task"}, true},
		{"null", json.RawMessage(`null`), false},
		{"array", []any{}, false},
		{"string", "invalid", false},
		{"omitted", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_wait", Arguments: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			m := resultJSON(t, r)
			if !r.IsError || m["error"] != codeInvalidArgument || !strings.Contains(m["next"].(string), "vm_wait input schema") {
				t.Fatalf("schema error did not use the JSON contract: %v", m)
			}
			if tc.wantTask && (m["task_id"] != "schema-task" || m["run_id"] == "" || m["run_id"] == nil) {
				t.Fatalf("schema error lost task identity: %v", m)
			}
		})
	}
	if vmCalls != 0 {
		t.Fatalf("invalid arguments reached VM backend %d times", vmCalls)
	}
}

func TestSchemaValidRequestAndTaskIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := mcp.NewServer(&mcp.Implementation{Name: "schema-test", Version: "test"}, nil)
	d := &deps{s: s, tasks: newTaskRegistry(), obs: newObservationStore(), turn: newTurnState(), runID: "server-run"}
	type input struct {
		Kind  string `json:"kind"`
		Count int8   `json:"count,omitempty"`
	}
	var calls int
	addToolIn(d, toolSpec{name: "schema_test", readOnly: true}, func(_ context.Context, in input) (*mcp.CallToolResult, error) {
		calls++
		return jsonResult(map[string]any{"kind": in.Kind, "count": in.Count})
	})
	addToolIn(d, toolSpec{name: "schema_optional", readOnly: true}, func(_ context.Context, in vmIn) (*mcp.CallToolResult, error) {
		return jsonResult(map[string]any{"ok": true, "vm": in.VM})
	})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "schema-client", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	var defaultTask any
	for _, args := range []any{nil, json.RawMessage(`null`), map[string]any{}} {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "schema_optional", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		m := resultJSON(t, r)
		if r.IsError || m["ok"] != true || m["vm"] != "" {
			t.Fatalf("optional arguments changed behavior: %v", m)
		}
		if defaultTask == nil {
			defaultTask = m["task_id"]
		} else if m["task_id"] != defaultTask {
			t.Fatalf("optional arguments changed default task: %v", m)
		}
	}
	call := func(args any) (*mcp.CallToolResult, map[string]any) {
		t.Helper()
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "schema_test", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return r, resultJSON(t, r)
	}
	r, valid := call(map[string]any{"kind": "ok", "count": 7, "task_id": "schema-task"})
	if r.IsError || valid["kind"] != "ok" || valid["count"] != float64(7) {
		t.Fatalf("valid input: %v", valid)
	}
	for _, args := range []any{map[string]any{"task_id": "schema-task"}, map[string]any{"kind": "ok", "count": 1000, "task_id": "schema-task"}} {
		r, invalid := call(args)
		if !r.IsError || invalid["error"] != codeInvalidArgument || invalid["task_id"] != valid["task_id"] || invalid["run_id"] != valid["run_id"] {
			t.Fatalf("invalid input identity: %v, want %v", invalid, valid)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid input reached handler: calls=%d", calls)
	}
	d.tasks.tasks["schema-task"].mu.Lock()
	d.tasks.tasks["schema-task"].ended = true
	d.tasks.tasks["schema-task"].mu.Unlock()
	r, ended := call(map[string]any{"kind": "ok", "task_id": "schema-task"})
	if !r.IsError || ended["error"] != "task_ended" || ended["run_id"] != valid["run_id"] {
		t.Fatalf("task end behavior changed: %v", ended)
	}
}
