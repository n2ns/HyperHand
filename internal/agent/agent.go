// Package agent implements the guest-side op handlers and the request/response loop.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"time"

	"hyperhand/internal/proto"
)

// Handler handles one op: args are the request's JSON args, payload the request's payload.
// ctx is cancelled when the host goes away while the request runs.
type Handler func(ctx context.Context, args json.RawMessage, payload []byte) (result any, out []byte, err error)

var handlers = map[string]Handler{
	proto.OpPing:         ping,
	proto.OpExec:         execOp,
	proto.OpWriteFile:    writeFile,
	proto.OpReadFile:     readFile,
	proto.OpListDir:      listDir,
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

// Dispatch runs the handler for op; a panicking handler returns an error.
func Dispatch(ctx context.Context, op string, args json.RawMessage, payload []byte) (result any, out []byte, err error) {
	h, ok := handlers[op]
	if !ok {
		return nil, nil, fmt.Errorf("unknown op %q", op)
	}
	defer func() {
		if r := recover(); r != nil {
			result, out, err = nil, nil, fmt.Errorf("%s panicked: %v", op, r)
		}
	}()
	return h(ctx, args, payload)
}

// Serve reads requests from c and writes responses until the connection fails.
func Serve(c net.Conn) error {
	for {
		var req proto.Request
		payload, err := proto.ReadFrame(c, &req)
		if err != nil {
			return err
		}
		// The host sends nothing while it waits for the response, so any return of this read
		// (EOF, error or data) means the host went away: cancel the request.
		ctx, cancel := context.WithCancel(context.Background())
		watchErr := make(chan error, 1)
		go func() {
			var b [1]byte
			_, err := c.Read(b[:])
			if err == nil {
				err = errors.New("unexpected data from host while a request is running")
			}
			cancel()
			watchErr <- err
		}()
		var resp proto.Response
		result, out, err := Dispatch(ctx, req.Op, req.Args, payload)
		// Stop the watcher before the next ReadFrame so it does not take bytes of the next frame.
		c.SetReadDeadline(time.Unix(1, 0))
		werr := <-watchErr
		c.SetReadDeadline(time.Time{})
		cancel()
		if ne, ok := werr.(net.Error); ok && ne.Timeout() {
			werr = nil // stopped by the deadline above: the host is still there
		}
		if err == nil && result != nil {
			resp.Result, err = json.Marshal(result)
		}
		if err != nil {
			resp.Error, out = err.Error(), nil
		}
		if werr == nil {
			werr = proto.WriteFrame(c, resp, out)
		}
		if req.Op == proto.OpUpdateAgent && resp.Error == "" && AfterUpdate != nil {
			AfterUpdate() // the new exe is already in place, even if the response could not be sent
		}
		if werr != nil {
			return werr
		}
	}
}

func decode(args json.RawMessage, v any) error {
	if len(args) == 0 {
		return nil
	}
	return json.Unmarshal(args, v)
}

func ping(context.Context, json.RawMessage, []byte) (any, []byte, error) {
	r := proto.PingResult{Version: proto.Version}
	r.Hostname, _ = os.Hostname()
	if u, err := user.Current(); err == nil {
		r.User = u.Username
	}
	return r, nil, nil
}

func updateAgent(_ context.Context, _ json.RawMessage, payload []byte) (any, []byte, error) {
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
