package host

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type endTurnIn struct {
	VM string `json:"vm,omitempty" jsonschema:"only this VM's temporary checkpoints; default: every VM's"`
}

// endTurnOut is vm_end_turn's result.
type endTurnOut struct {
	CancelledWaits     int      `json:"cancelled_waits"`
	DeletedCheckpoints []string `json:"deleted_checkpoints"`
	Errors             []string `json:"errors"`
}

// take removes and returns the pending waits and the temporary checkpoints (of vm, or of every VM when vm is "") so
// that vm_end_turn can cancel and delete them outside the lock.
func (t *turnState) take(vm string) (waits []context.CancelFunc, checkpoints map[string][]tempCheckpoint) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, cancel := range t.waits {
		waits = append(waits, cancel)
		delete(t.waits, id)
	}
	checkpoints = map[string][]tempCheckpoint{}
	for name, cps := range t.checkpoints {
		if vm == "" || name == vm {
			checkpoints[name] = cps
			delete(t.checkpoints, name)
		}
	}
	return waits, checkpoints
}

// registerTurn registers vm_end_turn.
func registerTurn(d *deps) {
	addToolIn(d, toolSpec{name: "vm_end_turn", desc: "End the turn: cancel this server's pending vm_wait calls and delete the temporary (temp) checkpoints created in this run (run_id). Keep and manual checkpoints, running programs and the VM's power state are not touched. Meant for a Stop hook; safe to call any time.", destructive: true, idempotent: true}, func(ctx context.Context, in endTurnIn) (*mcp.CallToolResult, error) {
		waits, checkpoints := d.turn.take(in.VM)
		for _, cancel := range waits {
			cancel()
		}
		out := endTurnOut{CancelledWaits: len(waits), DeletedCheckpoints: []string{}, Errors: []string{}}
		for vm, cps := range checkpoints {
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
