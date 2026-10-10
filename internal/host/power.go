package host

import (
	"context"
	"fmt"
	"time"

	"hyperhand/internal/hyperv"
)

// shutdownTimeout is how long vm_shutdown waits for the VM to be off.
const shutdownTimeout = 3 * time.Minute

// waitOff polls the VM's state every 2 seconds until it is Off. It never turns the VM off itself.
func waitOff(ctx context.Context, find func() (hyperv.VM, error), sleep func(time.Duration), timeout time.Duration) error {
	end := time.Now().Add(timeout)
	for {
		v, err := find()
		if err != nil {
			return err
		}
		if v.State == "Off" {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("stopped waiting for VM %s to shut down; the shutdown may still be in progress: %w", v.Name, ctx.Err())
		}
		if !time.Now().Before(end) {
			return fmt.Errorf("VM %s is still %s %v after the shutdown request: a program in the guest may be blocking shutdown (check with vm_observe), or the guest ignored the request. It was not turned off; use vm_turn_off only if the guest is stuck", v.Name, v.State, timeout)
		}
		sleep(2 * time.Second)
	}
}
