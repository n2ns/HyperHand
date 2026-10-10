package host

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type endTurnIn struct {
	VM      string `json:"vm,omitempty" jsonschema:"only this VM's temporary checkpoints; default: every VM's"`
	AllTemp bool   `json:"all_temp,omitempty" jsonschema:"also delete temp checkpoints of other runs (every checkpoint named <run_id>-temp-<label>) on the VM, or on every VM when vm is omitted; default false"`
}

// endTurnOut is vm_end_turn's result. DeletedCheckpoints are the names of the deleted checkpoints; Skipped lists the
// temp checkpoints all_temp found but could not delete (with the reason); Errors are this run's registered
// checkpoints that could not be deleted (kept for the next call) and VMs whose checkpoints could not be listed.
type endTurnOut struct {
	CancelledWaits     int                 `json:"cancelled_waits"`
	DeletedCheckpoints []string            `json:"deleted_checkpoints"`
	Skipped            []skippedCheckpoint `json:"skipped"`
	Errors             []string            `json:"errors"`
}

// skippedCheckpoint is a temp checkpoint vm_end_turn {all_temp: true} could not delete.
type skippedCheckpoint struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// takeWaits removes and returns the pending waits so that vm_end_turn can cancel them outside the lock.
func (t *turnState) takeWaits() (waits []context.CancelFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, cancel := range t.waits {
		waits = append(waits, cancel)
		delete(t.waits, id)
	}
	return waits
}

// takeCheckpoints removes and returns the temporary checkpoints of vm, or of every VM when vm is "".
func (t *turnState) takeCheckpoints(vm string) map[string][]tempCheckpoint {
	t.mu.Lock()
	defer t.mu.Unlock()
	checkpoints := map[string][]tempCheckpoint{}
	for name, cps := range t.checkpoints {
		if vm == "" || name == vm {
			checkpoints[name] = cps
			delete(t.checkpoints, name)
		}
	}
	return checkpoints
}

// registerTurn registers vm_end_turn.
func registerTurn(d *deps) {
	addToolIn(d, toolSpec{name: "vm_end_turn", desc: "End the turn: cancel this server's pending vm_wait calls and delete the temp checkpoints created in this run (run_id). Keep and manual checkpoints, running programs and the VM's power state are not touched. Meant for a Stop hook; safe to call any time. all_temp: true additionally deletes every temp checkpoint of any run (names <run_id>-temp-<label>) on the VM, or on every VM when vm is omitted, to clean up after a crashed or restarted server; this crosses runs, so use it only when no other HyperHand client is working on the VM. Deleting merges disk differences and can take minutes per checkpoint; checkpoints all_temp could not delete are listed in skipped.", destructive: true, idempotent: true}, func(ctx context.Context, in endTurnIn) (*mcp.CallToolResult, error) {
		waits := d.turn.takeWaits()
		for _, cancel := range waits {
			cancel()
		}
		out := endTurnOut{CancelledWaits: len(waits), DeletedCheckpoints: []string{}, Skipped: []skippedCheckpoint{}, Errors: []string{}}
		if in.AllTemp {
			deleteAllTemp(d, in.VM, &out)
			return jsonResult(out)
		}
		for vm, cps := range d.turn.takeCheckpoints(in.VM) {
			for _, c := range cps {
				if err := d.raw.DeleteCheckpoint(vm, c.ID, false); err != nil {
					out.Errors = append(out.Errors, fmt.Sprintf("%s/%s: %v", vm, c.Name, err))
					d.turn.addTempCheckpoint(vm, c) // keep it for the next vm_end_turn
					continue
				}
				out.DeletedCheckpoints = append(out.DeletedCheckpoints, c.Name)
			}
		}
		return jsonResult(out)
	})
}

// deleteAllTemp deletes, by id, every checkpoint whose name parses as a temp one of any run on vm (or on every VM
// when vm is ""), unregistering the deleted ones from turnState. Checkpoints it could not delete go to out.Skipped,
// VMs it could not list or resolve to out.Errors.
func deleteAllTemp(d *deps, vm string, out *endTurnOut) {
	var vms []string
	if vm != "" {
		v, err := d.raw.Find(vm)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", vm, err))
			return
		}
		vms = []string{v.Name}
	} else {
		all, err := d.raw.ListVMs()
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("list VMs: %v", err))
			return
		}
		for _, v := range all {
			vms = append(vms, v.Name)
		}
	}
	for _, name := range vms {
		l, err := d.raw.ListCheckpoints(name)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		for _, c := range l.Checkpoints {
			if _, typ, _ := parseCheckpointName(c.Name); typ != checkpointTemp {
				continue
			}
			if err := d.raw.DeleteCheckpoint(name, c.ID, false); err != nil {
				out.Skipped = append(out.Skipped, skippedCheckpoint{ID: c.ID, Name: c.Name, Reason: err.Error()})
				continue
			}
			d.turn.removeTempCheckpoint(name, c.ID)
			out.DeletedCheckpoints = append(out.DeletedCheckpoints, c.Name)
		}
	}
}
