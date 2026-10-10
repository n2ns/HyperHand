package host

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerDoctor registers vm_doctor.
func registerDoctor(d *deps) {
	dr := newDoctor(d.raw, d.call)
	addToolIn(d, toolSpec{name: "vm_doctor", desc: "Diagnose the host (HyperHand service, MCP listener, service pipe, Hyper-V socket registration, enhanced session mode) and the selected VM (power, agent version and protocol, session, agent integrity). Read-only; each warn or fail carries a concrete suggestion.", readOnly: true, idempotent: true}, func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
		return jsonResult(map[string]any{"checks": dr.run(ctx, in.VM)})
	})
}
