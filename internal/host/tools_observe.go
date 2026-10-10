package host

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerObserve registers vm_windows and vm_observe and sets d.observe.
func registerObserve(d *deps) {
	s, call := d.s, d.call
	add(s, "vm_windows", "List the guest's visible top-level windows, from the top of the Z order down, as JSON: handle, title, class, pid, process, rect (visible frame in screenshot pixels), enabled, foreground, minimized, owner (owner window handle) and modal (its owner is disabled, as while a modal dialog runs).",
		func(ctx context.Context, in vmIn) (*mcp.CallToolResult, error) {
			ws, err := listWindows(ctx, call, in.VM)
			if err != nil {
				return nil, err
			}
			b, err := json.MarshalIndent(ws, "", "  ")
			if err != nil {
				return nil, err
			}
			return text("%s", b), nil
		})
	d.observe = func(ctx context.Context, in observeIn) (*observeOut, []byte, error) {
		return nil, nil, notImplemented("vm_observe")
	}
	addToolIn(d, toolSpec{name: "vm_observe", desc: "Observe a window or the whole screen: screenshot, indexed control tree, focused control and an observation_id that actions take.", readOnly: true}, func(ctx context.Context, in observeIn) (*mcp.CallToolResult, error) {
		out, png, err := d.observe(ctx, in)
		if err != nil {
			return nil, err
		}
		return jsonImageResult(out, png)
	})
}
