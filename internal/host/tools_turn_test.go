package host

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

func TestEndTurnDeletesTempCheckpointsOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b := &vmToolsBackend{vms: []hyperv.VM{{Name: "Win10", ID: "id-a", State: "Running"}, {Name: "Win11", ID: "id-b", State: "Running"}}}
	cs, d := connectTools(t, ctx, b)
	d.turn.addTempCheckpoint("Win10", d.runID+"-temp-step1")
	d.turn.addTempCheckpoint("Win10", d.runID+"-temp-fails")
	d.turn.addTempCheckpoint("Win11", d.runID+"-temp-other")
	var out endTurnOut
	callJSON(t, ctx, cs, "vm_end_turn", map[string]any{"vm": "Win10"}, &out)
	slices.Sort(out.DeletedCheckpoints)
	if out.CancelledWaits != 0 || !slices.Equal(out.DeletedCheckpoints, []string{d.runID + "-temp-step1"}) || len(out.Errors) != 1 {
		t.Fatalf("end_turn(Win10) %+v", out)
	}
	if !slices.Equal(b.deleted, []string{"Win10/" + d.runID + "-temp-step1"}) {
		t.Errorf("deleted %v", b.deleted)
	}
	// The failed one stays registered for the next call; the other VM's checkpoint was not touched.
	d.turn.mu.Lock()
	left := map[string][]string{"Win10": slices.Clone(d.turn.checkpoints["Win10"]), "Win11": slices.Clone(d.turn.checkpoints["Win11"])}
	d.turn.mu.Unlock()
	if !slices.Equal(left["Win10"], []string{d.runID + "-temp-fails"}) || !slices.Equal(left["Win11"], []string{d.runID + "-temp-other"}) {
		t.Errorf("left %v", left)
	}
	// Without vm every VM's temp checkpoints go; the failing one is reported again.
	callJSON(t, ctx, cs, "vm_end_turn", nil, &out)
	if !slices.Equal(out.DeletedCheckpoints, []string{d.runID + "-temp-other"}) || len(out.Errors) != 1 {
		t.Fatalf("end_turn() %+v", out)
	}
	// Nothing left: an empty, well-formed result (arrays, not null).
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "vm_end_turn", Arguments: map[string]any{}})
	if err != nil || r.IsError {
		t.Fatal(err, r)
	}
	if got := resultText(r); got != `{"cancelled_waits":0,"deleted_checkpoints":[],"errors":[`+"\""+"Win10/"+d.runID+"-temp-fails: Hyper-V job failed"+"\""+`]}` {
		t.Errorf("empty result %s", got)
	}
	// Only temp names were ever deleted: no keep or manual checkpoint reached the backend.
	for _, n := range b.deleted {
		if _, typ, _ := parseCheckpointName(n[len("Win10/"):]); typ != checkpointTemp {
			t.Errorf("deleted a %s checkpoint: %s", typ, n)
		}
	}
}

func TestEndTurnCancelsWaits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	b := newFakeAgentBackend(func(req proto.Request) (any, error) {
		if req.Op != proto.OpWait {
			return nil, errors.New("unexpected op " + req.Op)
		}
		entered <- struct{}{}
		<-release // the guest condition never comes true
		return proto.WaitResult{Satisfied: false}, nil
	})
	defer close(release)
	cs, _ := connectTools(t, ctx, b)
	done := make(chan map[string]any, 1)
	go func() {
		done <- callRefusedQuiet(ctx, cs, "vm_wait", map[string]any{"kind": "process_exit", "name": "acad", "timeout_ms": 60000})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("vm_wait never reached the agent")
	}
	var out endTurnOut
	callJSON(t, ctx, cs, "vm_end_turn", nil, &out)
	if out.CancelledWaits != 1 || len(out.DeletedCheckpoints) != 0 || len(out.Errors) != 0 {
		t.Fatalf("end_turn %+v", out)
	}
	select {
	case e := <-done:
		if e == nil || e["error"] != codeFailed || e["reason"] != "the wait was cancelled by vm_end_turn" {
			t.Fatalf("vm_wait after cancel: %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("vm_wait did not return after vm_end_turn")
	}
	// The cancelled wait is unregistered: a second end_turn cancels nothing.
	callJSON(t, ctx, cs, "vm_end_turn", nil, &out)
	if out.CancelledWaits != 0 {
		t.Errorf("second end_turn %+v", out)
	}
}

// callRefusedQuiet is callRefused without a *testing.T, for calls made from another goroutine; nil means the call did
// not produce a refusal.
func callRefusedQuiet(ctx context.Context, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || !r.IsError {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(resultText(r)), &obj); err != nil {
		return nil
	}
	return obj
}
