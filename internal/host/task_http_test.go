package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/hyperv"
)

// taskHTTPBackend exercises the real MCP HTTP transport without contacting Hyper-V.
type taskHTTPBackend struct {
	*observeBackend
	clicks atomic.Int32
	keys   atomic.Int32
	press  func()
}

func (b *taskHTTPBackend) ListVMs() ([]hyperv.VM, error) {
	return []hyperv.VM{{ID: "A", Name: "CAD", State: "Running"}}, nil
}

func (b *taskHTTPBackend) Click(string, int, int, int, int, []string) error {
	b.clicks.Add(1)
	return nil
}

func (b *taskHTTPBackend) PressKeys(string, string) error {
	b.keys.Add(1)
	if b.press != nil {
		b.press()
	}
	return nil
}

func (b *taskHTTPBackend) Stop(string) error { return nil }

func taskHTTPServer(t *testing.T, stateless bool) (string, *taskHTTPBackend) {
	t.Helper()
	b := &taskHTTPBackend{observeBackend: newObserveBackend(t)}
	m := &Manager{Backend: b}
	t.Cleanup(func() { m.Drop("A") })
	s := NewServer(m)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: stateless})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL, b
}

func connectTaskHTTP(t *testing.T, ctx context.Context, endpoint string) *mcp.ClientSession {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "task-http-test", Version: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func requireHTTPTask(t *testing.T, out map[string]any, taskID string) {
	t.Helper()
	if out["task_id"] != taskID || out["run_id"] == nil || out["run_id"] == "" {
		t.Fatalf("missing task identity: %v, want task %q", out, taskID)
	}
}

func TestTaskHTTPExplicitReconnectAndIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	endpoint, b := taskHTTPServer(t, false)
	a := connectTaskHTTP(t, ctx, endpoint)
	observation, _, _ := observe(t, ctx, a, map[string]any{"task_id": "editing", "vm": "CAD"})
	requireHTTPTask(t, observation, "editing")
	id := observation["observation_id"]
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	other := connectTaskHTTP(t, ctx, endpoint)
	refused := callRefused(t, ctx, other, "vm_click", map[string]any{"task_id": "other", "vm": "CAD", "observation_id": id, "x": 4, "y": 4, "observe_after": "none"})
	if refused["error"] != "stale_observation" || b.clicks.Load() != 0 {
		t.Fatalf("another task used the observation: %v, clicks %d", refused, b.clicks.Load())
	}
	var out map[string]any
	callJSON(t, ctx, other, "vm_end_turn", map[string]any{"task_id": "other"}, &out)

	resumed := connectTaskHTTP(t, ctx, endpoint)
	callJSON(t, ctx, resumed, "vm_click", map[string]any{"task_id": "editing", "vm": "CAD", "observation_id": id, "x": 4, "y": 4, "observe_after": "none"}, &out)
	requireHTTPTask(t, out, "editing")
	if b.clicks.Load() != 1 {
		t.Fatalf("reconnected task clicks %d, want 1", b.clicks.Load())
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}

	refused = callRefused(t, ctx, other, "vm_key", map[string]any{"task_id": "waiting", "vm": "CAD", "keys": "enter"})
	if refused["error"] != "vm_busy" || b.keys.Load() != 0 {
		t.Fatalf("disconnect released explicit task ownership: %v", refused)
	}
	callJSON(t, ctx, other, "vm_end_turn", map[string]any{"task_id": "waiting"}, &out)
	refused = callRefused(t, ctx, other, "vm_key", map[string]any{"task_id": "next", "vm": "CAD", "keys": "enter"})
	if refused["error"] != "vm_busy" {
		t.Fatalf("another task's end_turn released editing: %v", refused)
	}

	cleanup := connectTaskHTTP(t, ctx, endpoint)
	for range 2 {
		callJSON(t, ctx, cleanup, "vm_end_turn", map[string]any{"task_id": "editing"}, &out)
		requireHTTPTask(t, out, "editing")
	}
	refused = callRefused(t, ctx, cleanup, "vm_key", map[string]any{"task_id": "editing", "vm": "CAD", "keys": "enter"})
	if refused["error"] != "task_ended" {
		t.Fatalf("late call reopened ended task: %v", refused)
	}
	callJSON(t, ctx, other, "vm_key", map[string]any{"task_id": "next", "vm": "CAD", "keys": "enter"}, &out)
	if b.keys.Load() != 1 {
		t.Fatalf("unexpected key dispatches: %d", b.keys.Load())
	}
}

func TestTaskHTTPDefaultSessionsAreSeparate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	endpoint, b := taskHTTPServer(t, false)
	a, other := connectTaskHTTP(t, ctx, endpoint), connectTaskHTTP(t, ctx, endpoint)
	var first, second, again map[string]any
	callJSON(t, ctx, a, "vm_list", nil, &first)
	callJSON(t, ctx, other, "vm_list", nil, &second)
	callJSON(t, ctx, a, "vm_list", nil, &again)
	id, ok := first["task_id"].(string)
	if !ok || id == "" || second["task_id"] == id || again["task_id"] != id {
		t.Fatalf("session task identities: %v, %v, %v", first, second, again)
	}
	callJSON(t, ctx, a, "vm_key", map[string]any{"vm": "CAD", "keys": "enter"}, &again)
	refused := callRefused(t, ctx, other, "vm_key", map[string]any{"vm": "CAD", "keys": "enter"})
	if refused["error"] != "vm_busy" {
		t.Fatalf("second session bypassed writer ownership: %v", refused)
	}
	callJSON(t, ctx, other, "vm_end_turn", nil, &again)
	callJSON(t, ctx, a, "vm_key", map[string]any{"vm": "CAD", "keys": "enter"}, &again)
	if b.keys.Load() != 2 {
		t.Fatalf("other session cleanup interfered with owner: keys %d", b.keys.Load())
	}
}

func TestTaskHTTPStatelessRequiresExplicitTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	endpoint, _ := taskHTTPServer(t, true)
	a := connectTaskHTTP(t, ctx, endpoint)
	refused := callRefused(t, ctx, a, "vm_list", nil)
	if refused["error"] != "task_required" {
		t.Fatalf("stateless request acquired an implicit task: %v", refused)
	}
	var first, resumed map[string]any
	callJSON(t, ctx, a, "vm_list", map[string]any{"task_id": "script-task"}, &first)
	requireHTTPTask(t, first, "script-task")
	other := connectTaskHTTP(t, ctx, endpoint)
	callJSON(t, ctx, other, "vm_list", map[string]any{"task_id": "script-task"}, &resumed)
	requireHTTPTask(t, resumed, "script-task")
	if first["run_id"] != resumed["run_id"] {
		t.Fatalf("stateless reconnect changed task run: %v -> %v", first, resumed)
	}
}

func TestTaskHTTPEndWaitsForActiveWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	endpoint, b := taskHTTPServer(t, false)
	a, stop, other := connectTaskHTTP(t, ctx, endpoint), connectTaskHTTP(t, ctx, endpoint), connectTaskHTTP(t, ctx, endpoint)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	b.press = func() {
		if b.keys.Load() == 1 {
			close(entered)
			<-release
		}
	}
	type result struct {
		r   *mcp.CallToolResult
		err error
	}
	written := make(chan result, 1)
	go func() {
		r, err := a.CallTool(ctx, &mcp.CallToolParams{Name: "vm_key", Arguments: map[string]any{"task_id": "active", "vm": "CAD", "keys": "enter"}})
		written <- result{r, err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("write did not reach fake backend")
	}
	ended := make(chan result, 1)
	go func() {
		r, err := stop.CallTool(ctx, &mcp.CallToolParams{Name: "vm_end_turn", Arguments: map[string]any{"task_id": "active"}})
		ended <- result{r, err}
	}()
	// Wait until cleanup has barred new calls, rather than assuming goroutine order.
	for {
		r, err := other.CallTool(ctx, &mcp.CallToolParams{Name: "vm_list", Arguments: map[string]any{"task_id": "active"}})
		if err != nil {
			t.Fatal(err)
		}
		if r.IsError {
			var out map[string]any
			if err := json.Unmarshal([]byte(resultText(r)), &out); err != nil || out["error"] != "task_busy" {
				t.Fatalf("unexpected cleanup refusal: %s (%v)", resultText(r), err)
			}
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			t.Fatal("end_turn did not enter closing state")
		}
	}
	select {
	case got := <-ended:
		t.Fatalf("cleanup returned before the write completed: %+v", got)
	default:
	}
	refused := callRefused(t, ctx, other, "vm_key", map[string]any{"task_id": "contender", "vm": "CAD", "keys": "enter"})
	if refused["error"] != "vm_busy" || b.keys.Load() != 1 {
		t.Fatalf("cleanup released ownership during active write: %v, keys %d", refused, b.keys.Load())
	}
	unblock()
	for _, pending := range []<-chan result{written, ended} {
		select {
		case got := <-pending:
			if got.err != nil || got.r == nil || got.r.IsError {
				t.Fatalf("write/cleanup failed: %+v", got)
			}
		case <-ctx.Done():
			t.Fatal("write/cleanup did not finish")
		}
	}
	refused = callRefused(t, ctx, other, "vm_key", map[string]any{"task_id": "active", "vm": "CAD", "keys": "enter"})
	if refused["error"] != "task_ended" {
		t.Fatalf("late write reopened ended task: %v", refused)
	}
	var out map[string]any
	callJSON(t, ctx, other, "vm_key", map[string]any{"task_id": "contender", "vm": "CAD", "keys": "enter"}, &out)
	if b.keys.Load() != 2 {
		t.Fatalf("next owner did not write exactly once: keys %d", b.keys.Load())
	}
}

func TestTaskHTTPOtherTaskInvalidatesOldObservation(t *testing.T) {
	for _, lifecycle := range []bool{false, true} {
		name := "input invalidates coordinates"
		if lifecycle {
			name = "power invalidates runtime IDs"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			endpoint, b := taskHTTPServer(t, false)
			reader, writer := connectTaskHTTP(t, ctx, endpoint), connectTaskHTTP(t, ctx, endpoint)
			old, _, _ := observe(t, ctx, reader, map[string]any{"task_id": "reader", "vm": "CAD", "controls": true})
			var out map[string]any
			if lifecycle {
				callJSON(t, ctx, writer, "vm_turn_off", map[string]any{"task_id": "writer", "vm": "CAD"}, &out)
			} else {
				callJSON(t, ctx, writer, "vm_key", map[string]any{"task_id": "writer", "vm": "CAD", "keys": "enter"}, &out)
			}
			callJSON(t, ctx, writer, "vm_end_turn", map[string]any{"task_id": "writer"}, &out)
			tool, reason := "vm_click", "coordinates"
			args := map[string]any{"task_id": "reader", "vm": "CAD", "observation_id": old["observation_id"], "x": 4, "y": 4, "observe_after": "none"}
			if lifecycle {
				tool, reason = "vm_invoke", "restarted"
				delete(args, "x")
				delete(args, "y")
				args["index"], args["action"] = 2, "Invoke"
			}
			refused := callRefused(t, ctx, reader, tool, args)
			if refused["error"] != "stale_observation" || !strings.Contains(refused["reason"].(string), reason) || b.clicks.Load() != 0 {
				t.Fatalf("other task's mutation did not invalidate the retained observation: %v", refused)
			}
		})
	}
}
