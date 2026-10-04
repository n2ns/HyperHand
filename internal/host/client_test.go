package host

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"hyperhand/internal/proto"
)

// fakeAgent answers exec, write_file, list_dir and read_file requests on conn until it is closed.
func fakeAgent(t *testing.T, conn net.Conn, written *[]byte) {
	defer conn.Close()
	for {
		var req proto.Request
		payload, err := proto.ReadFrame(conn, &req)
		if err != nil {
			return
		}
		var resp proto.Response
		var out []byte
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
		case proto.OpListDir:
			var a proto.PathArgs
			json.Unmarshal(req.Args, &a)
			if e, ok := fakeDirs[a.Path]; ok {
				resp.Result, _ = json.Marshal(proto.ListDirResult{Entries: e})
			} else {
				resp.Error = "not a directory"
			}
		case proto.OpReadFile:
			var a proto.PathArgs
			json.Unmarshal(req.Args, &a)
			if d, ok := fakeFiles[a.Path]; ok {
				out = []byte(d)
			} else {
				resp.Error = "no file " + a.Path
			}
		default:
			resp.Error = "unknown op"
		}
		if err := proto.WriteFrame(conn, resp, out); err != nil {
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

var fakeDirs = map[string][]proto.DirEntry{
	`C:\d`:           {{Name: "a.txt"}, {Name: "sub", IsDir: true}},
	`C:\d\sub`:       {{Name: "b.txt"}, {Name: "empty", IsDir: true}},
	`C:\d\sub\empty`: nil,
}
var fakeFiles = map[string]string{`C:\d\a.txt`: "aa", `C:\d\sub\b.txt`: "bbb", `C:\f.txt`: "f"}

func TestPull(t *testing.T) {
	c := NewClient(func(context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go fakeAgent(t, b, new([]byte))
		return a, nil
	})
	defer c.Close()
	ctx, dir := context.Background(), t.TempDir()

	n, size, err := pull(ctx, c, `C:\d`, filepath.Join(dir, "d"))
	if err != nil || n != 2 || size != 5 {
		t.Fatalf("dir pull: %d files, %d bytes, %v", n, size, err)
	}
	for p, want := range map[string]string{"d/a.txt": "aa", "d/sub/b.txt": "bbb"} {
		if got, err := os.ReadFile(filepath.Join(dir, p)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", p, got, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, "d/sub/empty")); err != nil || !fi.IsDir() {
		t.Errorf("empty dir: %v", err)
	}

	if n, _, err := pull(ctx, c, `C:\f.txt`, filepath.Join(dir, "x", "f.txt")); err != nil || n != 1 {
		t.Fatalf("file pull: %d, %v", n, err)
	}
	if _, _, err := pull(ctx, c, `C:\missing`, filepath.Join(dir, "m")); err == nil {
		t.Fatal("missing should fail")
	}
}
