package host

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerTurn registers vm_end_turn.
func registerTurn(d *deps) {
	addToolIn(d, toolSpec{name: "vm_end_turn", desc: "Pending: cancel pending waits and delete this run's temporary checkpoints.", destructive: true}, func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		return nil, notImplemented("vm_end_turn")
	})
}
