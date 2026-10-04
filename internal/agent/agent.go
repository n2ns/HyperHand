// Package agent implements the guest-side op handlers and the request/response loop.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"

	"hyperhand/internal/proto"
)

// Handler handles one op: args are the request's JSON args, payload the request's payload.
// ctx is cancelled when the host goes away while the request runs.
type Handler func(ctx context.Context, args json.RawMessage, payload []byte) (result any, out []byte, err error)

var handlers = map[string]Handler{
	proto.OpPing:         ping,
	proto.OpExec:         execOp,
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
	// One goroutine reads frames for the whole connection. The host sends nothing while it waits for a response, so a
	// read error while a request runs means the host went away: the request's context is cancelled.
	// write_file payloads are streamed to the file right here (done is set and opErr carries the result), so the
	// stream stays in sync whatever happens to the file.
	type frame struct {
		req     proto.Request
		payload []byte
		done    bool
		opErr   error
		err     error
	}
	frames := make(chan frame, 1)
	gone := make(chan struct{})
	go func() {
		for {
			var f frame
			var size int64
			size, f.err = proto.ReadHeader(c, &f.req)
			if f.err == nil && f.req.Op == proto.OpWriteFile {
				f.done = true
				f.opErr, f.err = writeFileStream(c, f.req.Args, size)
			} else if f.err == nil {
				f.payload = make([]byte, size)
				_, f.err = io.ReadFull(c, f.payload)
			}
			if f.err != nil {
				close(gone)
			}
			frames <- f
			if f.err != nil {
				return
			}
		}
	}()
	for {
		f := <-frames
		if f.err != nil {
			return f.err
		}
		req, payload := f.req, f.payload
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-gone:
				cancel()
			case <-ctx.Done():
			}
		}()
		var resp proto.Response
		var result any
		var out []byte
		var err error
		if f.done {
			err = f.opErr
		} else {
			result, out, err = Dispatch(ctx, req.Op, req.Args, payload)
		}
		stream, isStream := result.(fileStream) // read_file: the payload is streamed from the open file
		if isStream {
			result = nil
		}
		var werr error
		select {
		case <-gone:
			werr = errors.New("host disconnected")
		default:
		}
		cancel()
		if err == nil && result != nil {
			resp.Result, err = json.Marshal(result)
		}
		if err != nil {
			resp.Error, out = err.Error(), nil
		}
		if werr == nil {
			if isStream {
				werr = proto.WriteFrameFrom(c, resp, stream.size, stream.f)
			} else {
				werr = proto.WriteFrame(c, resp, out)
			}
		}
		if isStream {
			stream.f.Close()
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
