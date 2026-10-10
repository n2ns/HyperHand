package host

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerLaunch registers vm_launch.
func registerLaunch(d *deps) {
	addToolIn(d, toolSpec{name: "vm_launch", desc: "Pending: start a detached process in the guest and return its first window."}, func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		return nil, notImplemented("vm_launch")
	})
}
