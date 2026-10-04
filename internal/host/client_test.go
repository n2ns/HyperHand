package host

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"

	"hyperhand/internal/proto"
)

// fakeAgent answers exec and write_file requests on conn until it is closed.
func fakeAgent(t *testing.T, conn net.Conn, written *[]byte) {
	defer conn.Close()
	for {
		var req proto.Request
		payload, err := proto.ReadFrame(conn, &req)
		if err != nil {
			return
		}
		var resp proto.Response
		switch req.Op {
		case proto.OpExec:
			var a proto.ExecArgs
			json.Unmarshal(req.Args, &a)
			resp.Result, _ = json.Marshal(proto.ExecResult{ExitCode: 3, Stdout: "ran " + a.Command})
		case proto.OpWriteFile:
			var a proto.PathArgs
			json.Unmarshal(req.Args, &a)
			if a.Path != `C:\x.bin` {
				resp.Error = "bad path " + a.Path
			}
			*written = payload
		default:
			resp.Error = "unknown op"
		}
		if err := proto.WriteFrame(conn, resp, nil); err != nil {
			t.Error(err)
			return
		}
	}
}

func TestClient(t *testing.T) {
	var written []byte
	c := NewClient(func(context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go fakeAgent(t, b, &written)
		return a, nil
	})
	defer c.Close()
	ctx := context.Background()

	var r proto.ExecResult
	if _, err := c.Call(ctx, proto.OpExec, proto.ExecArgs{Command: "dir"}, nil, &r); err != nil {
		t.Fatal(err)
	}
	if r.ExitCode != 3 || r.Stdout != "ran dir" {
		t.Fatalf("exec result %+v", r)
	}

	data := bytes.Repeat([]byte{1, 2, 3}, 100000)
	if _, err := c.Call(ctx, proto.OpWriteFile, proto.PathArgs{Path: `C:\x.bin`}, data, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, data) {
		t.Fatalf("payload: got %d bytes, want %d", len(written), len(data))
	}

	if _, err := c.Call(ctx, "nope", nil, nil, nil); err == nil || err.Error() != "unknown op" {
		t.Fatalf("agent error: %v", err)
	}
}

func TestNewServer(t *testing.T) { NewServer(&Manager{}) } // AddTool panics on a bad input schema
