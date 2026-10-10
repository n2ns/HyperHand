package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Error codes of toolError. Every refusal a tool makes has one of these; "failed" is for errors without a specific
// code (transport, Hyper-V, unexpected agent errors).
const (
	codeFailed              = "failed"
	codeInvalidArgument     = "invalid_argument"
	codeStaleObservation    = "stale_observation"  // the observation's window moved, resized, minimized or closed
	codeStaleElement        = "stale_element"      // the control index/runtime ID no longer resolves
	codeSessionUnusable     = "session_unusable"   // locked, secure desktop or non-console session
	codeActivateFailed      = "activate_failed"    // the target could not be brought to the foreground
	codeTargetDisabled      = "target_disabled"    // the target window is disabled (modal dialog or other blocker)
	codeCovered             = "covered"            // another window covers the point
	codeIntegrityMismatch   = "integrity_mismatch" // the target runs at a higher integrity level than the agent
	codeTargetNotResponding = "target_not_responding"
	codeUnsupportedPattern  = "unsupported_pattern" // the control does not support the requested action
	codePartialInput        = "partial_input"       // some input was injected before the failure
	codeAgentRequired       = "agent_required"      // the operation needs the guest agent, which is not reachable
	codeAgentOutdated       = "agent_outdated"      // the agent's protocol is older than proto.Protocol
	codeAmbiguousTarget     = "ambiguous_target"    // a selector matched several windows
	codeNoWindow            = "no_window"           // no window matched, or none appeared
	codeNoCheckpoint        = "no_checkpoint"       // no checkpoint has the given id or name
)

// toolError is a structured refusal: Code is one of the code constants, Reason says what happened, Next names the
// tool call that makes progress, and Fields carries the facts the caller needs for it (handles, classes, processes).
// It is returned to the MCP client as an isError result whose single text item is the JSON object
// {"error": Code, "reason": Reason, "next": Next, "run_id": ..., <Fields>...}.
type toolError struct {
	Code   string
	Reason string
	Next   string
	Fields map[string]any
}

func (e *toolError) Error() string {
	if e.Next == "" {
		return e.Code + ": " + e.Reason
	}
	return e.Code + ": " + e.Reason + "; " + e.Next
}

// refuse builds a toolError. fields may be nil.
func refuse(code, next string, fields map[string]any, format string, args ...any) *toolError {
	return &toolError{Code: code, Reason: fmt.Sprintf(format, args...), Next: next, Fields: fields}
}

// asToolError returns err as a toolError, wrapping other errors as "failed" (agent "unknown op" errors become
// agent_outdated, since no supported agent lacks an op the host sends).
func asToolError(err error) *toolError {
	var te *toolError
	if errors.As(err, &te) {
		return te
	}
	if strings.HasPrefix(err.Error(), "unknown op") {
		return refuse(codeAgentOutdated, "call vm_update_agent", nil, "the guest agent is too old: %v", err)
	}
	return &toolError{Code: codeFailed, Reason: err.Error(), Next: "call vm_status, then vm_doctor if the VM is running; the action may or may not have happened, so observe before repeating it"}
}

// errorResult renders a toolError as the isError MCP result described on toolError.
func errorResult(runID string, err error) *mcp.CallToolResult {
	te := asToolError(err)
	obj := map[string]any{"error": te.Code, "reason": te.Reason}
	if te.Next != "" {
		obj["next"] = te.Next
	}
	if runID != "" {
		obj["run_id"] = runID
	}
	for k, v := range te.Fields {
		obj[k] = v
	}
	b, _ := json.Marshal(obj)
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// jsonResult renders v as a tool result with one JSON text item.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
}

// jsonImageResult renders v as a JSON text item preceded by a PNG image item (omitted when png is nil).
func jsonImageResult(v any, png []byte) (*mcp.CallToolResult, error) {
	r, err := jsonResult(v)
	if err != nil {
		return nil, err
	}
	if png != nil {
		r.Content = append([]mcp.Content{&mcp.ImageContent{Data: png, MIMEType: "image/png"}}, r.Content...)
	}
	return r, nil
}

// toolSpec describes a tool for registration: its MCP annotations are hints for clients, not a security boundary.
type toolSpec struct {
	name        string
	desc        string
	readOnly    bool // ReadOnlyHint
	destructive bool // DestructiveHint (meaningful when !readOnly)
	idempotent  bool // IdempotentHint
}

// addToolIn registers a tool with typed input In on d's server. Handler errors become structured isError results
// (see toolError), never MCP protocol errors, so the client always gets the JSON object.
func addToolIn[In any](d *deps, spec toolSpec, f func(context.Context, In) (*mcp.CallToolResult, error)) {
	destructive, openWorld := spec.destructive, false
	tool := &mcp.Tool{Name: spec.name, Description: spec.desc, Annotations: &mcp.ToolAnnotations{
		ReadOnlyHint: spec.readOnly, DestructiveHint: &destructive, IdempotentHint: spec.idempotent, OpenWorldHint: &openWorld,
	}}
	mcp.AddTool(d.s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		r, err := f(ctx, in)
		if err != nil {
			return errorResult(d.runID, err), nil, nil
		}
		return r, nil, nil
	})
}
