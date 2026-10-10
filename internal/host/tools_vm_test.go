package host

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/hyperv"
)

func TestRunIDNaming(t *testing.T) {
	id := newRunID()
	if !regexp.MustCompile(`^run-\d{8}-\d{4}-[0-9a-f]{4}$`).MatchString(id) {
		t.Fatalf("run ID %q", id)
	}
	if id2 := newRunID(); id2 == id {
		t.Errorf("two run IDs are equal: %q", id)
	}
	name := checkpointName(id, "step3", false)
	if name != id+"-temp-step3" {
		t.Errorf("temp name %q", name)
	}
	if runID, typ, label := parseCheckpointName(name); runID != id || typ != checkpointTemp || label != "step3" {
		t.Errorf("parse(%q) = %q %q %q", name, runID, typ, label)
	}
	if keep := checkpointName(id, "golden", true); keep != id+"-keep-golden" {
		t.Errorf("keep name %q", keep)
	}
}

func TestParseCheckpointName(t *testing.T) {
	for _, c := range []struct{ name, runID, typ, label string }{
		{"run-20261010-0812-7f3a-temp-step3", "run-20261010-0812-7f3a", "temp", "step3"},
		{"run-20261010-0812-7f3a-keep-before-install", "run-20261010-0812-7f3a", "keep", "before-install"},
		{"run-20261010-0812-7f3a-keep-a-temp-b", "run-20261010-0812-7f3a", "keep", "a-temp-b"},
		{"run-20261010-0812-7f3a-temp-", "", "manual", ""},  // empty label
		{"run-20261010-0812-7f3a-old-x", "", "manual", ""},  // unknown type
		{"run-2026101-0812-7f3a-temp-x", "", "manual", ""},  // short date
		{"run-20261010-0812-7F3A-temp-x", "", "manual", ""}, // uppercase hex
		{"baseline", "", "manual", ""},
		{"干净", "", "manual", ""},
		{"", "", "manual", ""},
	} {
		runID, typ, label := parseCheckpointName(c.name)
		if runID != c.runID || typ != c.typ || label != c.label {
			t.Errorf("parseCheckpointName(%q) = %q %q %q, want %q %q %q", c.name, runID, typ, label, c.runID, c.typ, c.label)
		}
	}
}

// vmToolsBackend records checkpoint operations for the VM tools; unexpected operations fail (nil Backend).
type vmToolsBackend struct {
	Backend
	mu      sync.Mutex
	created []string
	deleted []string
	vms     []hyperv.VM
	listErr error
}

func (b *vmToolsBackend) ListVMs() ([]hyperv.VM, error) { return b.vms, b.listErr }
func (b *vmToolsBackend) Find(name string) (hyperv.VM, error) {
	for _, v := range b.vms {
		if name == "" && v.State == "Running" || strings.EqualFold(v.Name, name) {
			return v, nil
		}
	}
	if name == "" {
		return hyperv.VM{}, errors.New("no running VM")
	}
	return hyperv.VM{}, errors.New("VM \"" + name + "\" not found")
}
func (b *vmToolsBackend) ListCheckpoints(vm string) (hyperv.CheckpointList, error) {
	return hyperv.CheckpointList{CheckpointType: "Standard", CurrentParentID: "id-2", Checkpoints: []hyperv.Checkpoint{
		{Name: "baseline", CreatedAt: "2026-10-04T10:00:00+08:00", ID: "id-1", Kind: "standard", State: "off"},
		{Name: "run-20261010-0812-7f3a-temp-step3", CreatedAt: "2026-10-10T08:15:00+08:00", ID: "id-2", ParentID: "id-1", Kind: "standard", State: "running"},
	}}, nil
}
func (b *vmToolsBackend) CreateCheckpoint(vm, name string) (hyperv.Checkpoint, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.created = append(b.created, vm+"/"+name)
	return hyperv.Checkpoint{ID: "id-" + name, Name: name, CreatedAt: "2026-10-10T09:00:00+08:00", Kind: "standard", State: "running"}, nil
}
func (b *vmToolsBackend) DeleteCheckpoint(vm, id string, subtree bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if strings.HasSuffix(id, "-fails") {
		return errors.New("Hyper-V job failed")
	}
	b.deleted = append(b.deleted, vm+"/"+id)
	return nil
}
func (b *vmToolsBackend) RenameCheckpoint(vm, id, name string) error { return nil }

// connectTools builds a server on b and returns the client session and the server's deps.
func connectTools(t *testing.T, ctx context.Context, b Backend) (*mcp.ClientSession, *deps) {
	t.Helper()
	m := &Manager{Backend: b}
	s := mcp.NewServer(&mcp.Implementation{Name: "hyperhand", Version: "test"}, nil)
	d := &deps{s: s, m: m, raw: b, backend: lockedInput{b, &sync.Mutex{}}, input: &sync.Mutex{}, obs: newObservationStore(), runID: newRunID(), turn: newTurnState()}
	d.call = func(ctx context.Context, vm, op string, args any, payload []byte, result any) ([]byte, error) {
		c, err := m.Client(vm)
		if err != nil {
			return nil, err
		}
		return c.Call(ctx, op, args, payload, result)
	}
	d.u = newUnlocker(d.call, b, d.input)
	registerVM(d)
	registerTurn(d)
	registerLaunch(d)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, d
}

// callJSON calls a tool and decodes its JSON text item into out; it fails the test on an isError result.
func callJSON(t *testing.T, ctx context.Context, cs *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if r.IsError {
		t.Fatalf("%s refused: %s", name, resultText(r))
	}
	if err := json.Unmarshal([]byte(resultText(r)), out); err != nil {
		t.Fatalf("%s result %s: %v", name, resultText(r), err)
	}
}

// callRefused calls a tool expecting a refusal and returns the decoded error object.
func callRefused(t *testing.T, ctx context.Context, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !r.IsError {
		t.Fatalf("%s succeeded: %s", name, resultText(r))
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(resultText(r)), &obj); err != nil {
		t.Fatalf("%s error %s: %v", name, resultText(r), err)
	}
	return obj
}

func TestVMListAndCheckpointsShapes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b := &vmToolsBackend{vms: []hyperv.VM{{Name: "Win10", ID: "id-a", State: "Running"}, {Name: "Win11", ID: "id-b", State: "Off"}}}
	cs, d := connectTools(t, ctx, b)
	var list struct {
		VMs   []hyperv.VM `json:"vms"`
		RunID string      `json:"run_id"`
	}
	callJSON(t, ctx, cs, "vm_list", nil, &list)
	if len(list.VMs) != 2 || list.VMs[0].Name != "Win10" || list.RunID != d.runID {
		t.Errorf("vm_list %+v", list)
	}
	var cps struct {
		Checkpoints []checkpointOut `json:"checkpoints"`
	}
	callJSON(t, ctx, cs, "vm_checkpoints", nil, &cps)
	want := []checkpointOut{
		{Name: "baseline", CreatedAt: "2026-10-04T10:00:00+08:00", Type: "manual", ID: "id-1"},
		{Name: "run-20261010-0812-7f3a-temp-step3", CreatedAt: "2026-10-10T08:15:00+08:00", RunID: "run-20261010-0812-7f3a", Type: "temp", Label: "step3", ID: "id-2", Parent: "id-1"},
	}
	if len(cps.Checkpoints) != 2 || cps.Checkpoints[0] != want[0] || cps.Checkpoints[1] != want[1] {
		t.Errorf("vm_checkpoints %+v", cps.Checkpoints)
	}
	// An unknown VM is an invalid argument that names the fix.
	e := callRefused(t, ctx, cs, "vm_status", map[string]any{"vm": "nope"})
	if e["error"] != codeInvalidArgument || e["run_id"] != d.runID || !strings.Contains(e["next"].(string), "vm_list") {
		t.Errorf("unknown VM: %v", e)
	}
	// Tool annotations follow the design.
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]bool{"vm_list": true, "vm_status": true, "vm_checkpoints": true, "vm_clipboard_get": true, "vm_pull": true, "vm_wait": true}
	destructive := map[string]bool{"vm_turn_off": true, "vm_restore": true, "vm_shutdown": true, "vm_update_agent": true, "vm_push": true, "vm_end_turn": true}
	for _, tool := range tools.Tools {
		a := tool.Annotations
		if a == nil || a.DestructiveHint == nil || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: annotations %+v", tool.Name, a)
			continue
		}
		if a.ReadOnlyHint != readOnly[tool.Name] || *a.DestructiveHint != destructive[tool.Name] {
			t.Errorf("%s: readOnly %v destructive %v", tool.Name, a.ReadOnlyHint, *a.DestructiveHint)
		}
	}
}

func TestVMWaitValidatesKind(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cs, _ := connectTools(t, ctx, &vmToolsBackend{vms: []hyperv.VM{{Name: "Win10", ID: "id-a", State: "Running"}}})
	for _, args := range []map[string]any{
		{"kind": "window_exists", "name": "x"},
		{"kind": "process_exit"},
		{"kind": "file_exists"},
	} {
		if e := callRefused(t, ctx, cs, "vm_wait", args); e["error"] != codeInvalidArgument {
			t.Errorf("%v: %v", args, e)
		}
	}
}

func TestAgentErr(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{errors.New("connect to agent: dial failed"), codeAgentRequired},
		{errors.New("agent: EOF"), codeAgentRequired},
		{errors.New(`C:\x.txt: agent: EOF`), codeAgentRequired},
		{errors.New(`unknown op "launch"`), codeAgentOutdated},
		{errors.New(`VM "x" not found`), codeInvalidArgument},
		{errors.New("no running VM"), codeInvalidArgument},
		{errors.New("access denied"), codeFailed},
		{refuse(codeNoWindow, "", nil, "x"), codeNoWindow},
	} {
		if got := asToolError(agentErr(c.err)).Code; got != c.code {
			t.Errorf("agentErr(%v) = %s, want %s", c.err, got, c.code)
		}
	}
	if err := agentErr(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
}
