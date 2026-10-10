package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A task survives individual HTTP connections. Session defaults are only for
// persistent MCP sessions; callers sharing a session use explicit task_id values.
type taskRegistry struct {
	mu       sync.Mutex
	tasks    map[string]*taskState
	sessions map[*mcp.ServerSession]string
	owners   map[string]string // canonical VM name, folded -> task ID
}

type taskState struct {
	id, runID      string
	obs            *observationStore
	turn           *turnState
	mu             sync.Mutex
	active         map[int]taskCall
	next           int
	closing, ended bool
	closingVM      string
	changed        chan struct{}
}

type taskCall struct {
	vm     string
	wait   bool
	cancel context.CancelFunc
}
type taskContextKey struct{}

var errTaskEnd = errors.New("the wait was cancelled by vm_end_turn")

func newTaskRegistry() *taskRegistry {
	return &taskRegistry{tasks: map[string]*taskState{}, sessions: map[*mcp.ServerSession]string{}, owners: map[string]string{}}
}

func (d *deps) taskObs(ctx context.Context) *observationStore {
	if t, ok := ctx.Value(taskContextKey{}).(*taskState); ok {
		return t.obs
	}
	return d.obs
}
func (d *deps) taskTurn(ctx context.Context) *turnState {
	if t, ok := ctx.Value(taskContextKey{}).(*taskState); ok {
		return t.turn
	}
	return d.turn
}
func (d *deps) taskRunID(ctx context.Context) string {
	if t, ok := ctx.Value(taskContextKey{}).(*taskState); ok {
		return t.runID
	}
	return d.runID
}

// Serialize only the revision boundaries with input dispatch. Holding input for
// the external call itself would deadlock lifecycle operations that inject keys.
func (d *deps) beginExternalMutation(ctx context.Context, vm string, lifecycle bool) func() {
	d.input.Lock()
	end := d.taskObs(ctx).beginMutation(vm, lifecycle)
	d.input.Unlock()
	return func() { d.input.Lock(); defer d.input.Unlock(); end() }
}

func (d *deps) resolveTask(req *mcp.CallToolRequest, id string, ending bool) (*taskState, error) {
	r := d.tasks
	if r == nil {
		return nil, nil
	}
	if len(id) > 128 || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n\t") {
		return nil, refuse("invalid_argument", "use a nonblank task_id of at most 128 characters", nil, "invalid task_id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	explicit := id != ""
	if !explicit {
		if req.Session == nil || (req.Extra != nil && req.Session.ID() == "") {
			return nil, refuse("task_required", "pass the same unique task_id on every tool call in this AI task", nil, "this transport has no persistent MCP session; task_id is required")
		}
		id = r.sessions[req.Session]
		if t := r.tasks[id]; t != nil {
			t.mu.Lock()
			ended := t.ended
			t.mu.Unlock()
			if ended && !ending {
				id = ""
			}
		}
		if id == "" {
			id = "task-" + newRunID()
			r.sessions[req.Session] = id
		}
	}
	t := r.tasks[id]
	if t == nil {
		t = &taskState{id: id, runID: newRunID(), obs: newObservationStoreWithRevisions(d.obs.revisions), turn: newTurnState(), active: map[int]taskCall{}, changed: make(chan struct{})}
		r.tasks[id] = t
	}
	return t, nil
}

func (t *taskState) signalLocked() { close(t.changed); t.changed = make(chan struct{}) }

func (t *taskState) enter(ctx context.Context, vm string, wait bool) (context.Context, func(), error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return ctx, nil, refuse("task_ended", "use a new task_id for a new task", nil, "task %q has ended", t.id)
	}
	if t.closing && (t.closingVM == "" || strings.EqualFold(t.closingVM, vm)) {
		return ctx, nil, refuse("task_busy", "wait for vm_end_turn to finish", nil, "task %q is being cleaned up", t.id)
	}
	id := t.next
	t.next++
	cctx, cancel := context.WithCancelCause(ctx)
	t.active[id] = taskCall{vm: vm, wait: wait, cancel: func() { cancel(errTaskEnd) }}
	return cctx, func() { cancel(nil); t.mu.Lock(); delete(t.active, id); t.signalLocked(); t.mu.Unlock() }, nil
}

func (r *taskRegistry) claim(t *taskState, vm string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := strings.ToUpper(vm)
	if owner := r.owners[key]; owner != "" && owner != t.id {
		return refuse("vm_busy", "wait for the owning task to call vm_end_turn; read-only tools remain available", map[string]any{"vm": vm, "owner_task_id": owner}, "VM %q is owned by another task", vm)
	}
	r.owners[key] = t.id
	return nil
}

func (r *taskRegistry) release(t *taskState, vm string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, owner := range r.owners {
		if owner == t.id && (vm == "" || strings.EqualFold(key, vm)) {
			delete(r.owners, key)
		}
	}
}

// beginEnd blocks new calls before inspecting active operations, and waits for
// already accepted writes to finish before any checkpoint cleanup or release.
func (t *taskState) beginEnd(ctx context.Context, vm string) (int, error) {
	t.mu.Lock()
	if t.closing {
		t.mu.Unlock()
		return 0, refuse("task_busy", "retry vm_end_turn after the pending cleanup completes", nil, "task cleanup is already running")
	}
	t.closing = true
	t.closingVM = vm
	cancelled := 0
	for _, call := range t.active {
		if call.wait && (vm == "" || strings.EqualFold(vm, call.vm)) {
			call.cancel()
			cancelled++
		}
	}
	for {
		pending := false
		for _, call := range t.active {
			if vm == "" || strings.EqualFold(vm, call.vm) {
				pending = true
				break
			}
		}
		if !pending {
			t.mu.Unlock()
			return cancelled, nil
		}
		changed := t.changed
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			t.mu.Lock()
			t.closing = false
			t.signalLocked()
			t.mu.Unlock()
			return cancelled, ctx.Err()
		case <-changed:
		}
		t.mu.Lock()
	}
}

func (t *taskState) finishEnd(vm string, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if success && vm == "" {
		t.ended = true
	}
	t.closing = false
	t.signalLocked()
}

func taskResult(r *mcp.CallToolResult, t *taskState) *mcp.CallToolResult {
	if r == nil || t == nil {
		return r
	}
	for _, c := range r.Content {
		text, ok := c.(*mcp.TextContent)
		if !ok {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(text.Text), &obj) != nil || obj == nil {
			continue
		}
		obj["task_id"], _ = json.Marshal(t.id)
		obj["run_id"], _ = json.Marshal(t.runID)
		b, _ := json.Marshal(obj)
		text.Text = string(b)
	}
	return r
}
