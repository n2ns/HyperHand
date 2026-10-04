// Package agent implements the guest-side op handlers and the request/response loop.
package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"

	"hyperhand/internal/proto"
)

// Handler handles one op: args are the request's JSON args, payload the request's payload.
type Handler func(args json.RawMessage, payload []byte) (result any, out []byte, err error)

var handlers = map[string]Handler{
	proto.OpPing:         ping,
	proto.OpExec:         execOp,
	proto.OpWriteFile:    writeFile,
	proto.OpReadFile:     readFile,
	proto.OpScreenshot:   screenshotOp,
	proto.OpClipboardGet: clipboardGet,
	proto.OpClipboardSet: clipboardSet,
	proto.OpFocusWindow:  focusWindow,
	proto.OpWait:         waitOp,
	proto.OpUpdateAgent:  updateAgent,
}

// AfterUpdate is called after a successful update_agent response has been sent
// (the new exe is already in place); it should start the new exe and exit.
var AfterUpdate func()

// Dispatch runs the handler for op.
func Dispatch(op string, args json.RawMessage, payload []byte) (any, []byte, error) {
	h, ok := handlers[op]
	if !ok {
		return nil, nil, fmt.Errorf("unknown op %q", op)
	}
	return h(args, payload)
}

// Serve reads requests from rw and writes responses until the connection fails.
func Serve(rw io.ReadWriter) error {
	for {
		var req proto.Request
		payload, err := proto.ReadFrame(rw, &req)
		if err != nil {
			return err
		}
		var resp proto.Response
		result, out, err := Dispatch(req.Op, req.Args, payload)
		if err == nil && result != nil {
			resp.Result, err = json.Marshal(result)
		}
		if err != nil {
			resp.Error, out = err.Error(), nil
		}
		if err := proto.WriteFrame(rw, resp, out); err != nil {
			return err
		}
		if req.Op == proto.OpUpdateAgent && resp.Error == "" && AfterUpdate != nil {
			AfterUpdate()
		}
	}
}

func decode(args json.RawMessage, v any) error {
	if len(args) == 0 {
		return nil
	}
	return json.Unmarshal(args, v)
}

func ping(json.RawMessage, []byte) (any, []byte, error) {
	r := proto.PingResult{Version: proto.Version}
	r.Hostname, _ = os.Hostname()
	if u, err := user.Current(); err == nil {
		r.User = u.Username
	}
	return r, nil, nil
}

func updateAgent(_ json.RawMessage, payload []byte) (any, []byte, error) {
	if len(payload) == 0 {
		return nil, nil, fmt.Errorf("empty payload")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(exe+".new", payload, 0o755); err != nil {
		return nil, nil, err
	}
	os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil {
		return nil, nil, err
	}
	if err := os.Rename(exe+".new", exe); err != nil {
		os.Rename(exe+".old", exe)
		return nil, nil, err
	}
	return nil, nil, nil
}
