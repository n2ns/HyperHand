package host

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerDoctor registers vm_doctor.
func registerDoctor(d *deps) {
	addToolIn(d, toolSpec{name: "vm_doctor", desc: "Pending: read-only diagnosis of host and guest.", readOnly: true}, func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		return nil, notImplemented("vm_doctor")
	})
}
