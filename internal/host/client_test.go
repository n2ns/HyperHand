package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hyperhand/internal/proto"
)

// fakePushed holds what write_file stored (other than C:\x.bin); hash_files reports its hashes unless fakeNoHash.
var (
	fakePushed = map[string][]byte{}
	fakeNoHash bool
)

// fakeAgent answers exec, write_file, list_dir, read_file and hash_files requests on conn until it is closed.
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
			if a.Path == `C:\x.bin` {
				*written = payload
			} else {
				fakePushed[a.Path] = payload
			}
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
			if a.Path == `C:\x.bin` {
				out = *written
			} else if d, ok := fakeFiles[a.Path]; ok {
				out = []byte(d)
			} else {
				resp.Error = "no file " + a.Path
			}
		case proto.OpHashFiles:
			if fakeNoHash {
				resp.Error = `unknown op "hash_files"`
				break
			}
			var a proto.PathsArgs
			json.Unmarshal(req.Args, &a)
			var r proto.HashesResult
			for _, p := range a.Paths {
				h := ""
				if d, ok := fakePushed[p]; ok {
					sum := sha256.Sum256(d)
					h = hex.EncodeToString(sum[:])
				}
				r.Hashes = append(r.Hashes, h)
			}
			resp.Result, _ = json.Marshal(r)
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

// A cached connection whose far end closed (agent restarted) is detected before sending and redialed.
func TestClientRedialsDeadConn(t *testing.T) {
	dials := 0
	var far net.Conn
	c := NewClient(func(context.Context) (net.Conn, error) {
		dials++
		a, b := net.Pipe()
		far = b
		go fakeAgent(t, b, new([]byte))
		return a, nil
	})
	defer c.Close()
	ctx := context.Background()
	var r proto.ExecResult
	if _, err := c.Call(ctx, proto.OpExec, proto.ExecArgs{Command: "a"}, nil, &r); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(ctx, proto.OpExec, proto.ExecArgs{Command: "b"}, nil, &r); err != nil || dials != 1 {
		t.Fatalf("live conn reused: dials %d, %v", dials, err)
	}
	far.Close()
	if _, err := c.Call(ctx, proto.OpExec, proto.ExecArgs{Command: "c"}, nil, &r); err != nil || r.Stdout != "ran c" || dials != 2 {
		t.Fatalf("after close: dials %d, %+v, %v", dials, r, err)
	}
}

func TestPushPullTarget(t *testing.T) {
	for _, c := range [][3]string{
		{`C:\dst\`, "a.txt", `C:\dst\a.txt`},
		{`C:\dst/`, "a.txt", `C:\dst/a.txt`},
		{`C:\dst\b.txt`, "a.txt", `C:\dst\b.txt`},
	} {
		if got := pushTarget(c[0], c[1]); got != c[2] {
			t.Errorf("pushTarget(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	dir := t.TempDir()
	for _, c := range [][3]string{
		{dir, `C:\g\f.txt`, filepath.Join(dir, "f.txt")},
		{dir + `\new\`, `C:\g\f.txt`, filepath.Join(dir, "new", "f.txt")},
		{filepath.Join(dir, "x.txt"), `C:\g\f.txt`, filepath.Join(dir, "x.txt")},
	} {
		if got := pullTarget(c[0], c[1]); got != c[2] {
			t.Errorf("pullTarget(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
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

// A few-MB file pushed and pulled through the streaming path arrives intact.
func TestPushPullStream(t *testing.T) {
	var written []byte
	c := NewClient(func(context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go fakeAgent(t, b, &written)
		return a, nil
	})
	defer c.Close()
	ctx, dir := context.Background(), t.TempDir()
	data := make([]byte, 5<<20+123)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	src := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pushFile(ctx, c, src, `C:\x.bin`); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, data) {
		t.Fatalf("pushed %d bytes, want %d", len(written), len(data))
	}
	dst := filepath.Join(dir, "dst.bin")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := pullFile(ctx, c, `C:\x.bin`, dst); err != nil || n != int64(len(data)) {
		t.Fatalf("pull: %d, %v", n, err)
	}
	if got, err := os.ReadFile(dst); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("pulled %d bytes, %v", len(got), err)
	}
	if _, err := pullFile(ctx, c, `C:\missing`, filepath.Join(dir, "m.bin")); err == nil {
		t.Fatal("missing should fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "m.bin.hhpart")); !os.IsNotExist(err) {
		t.Fatalf(".hhpart left behind: %v", err)
	}
}

// Canceling ctx while a response payload is being streamed returns promptly.
func TestCallIOCancel(t *testing.T) {
	c := NewClient(func(context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			var req proto.Request
			if _, err := proto.ReadFrame(b, &req); err != nil {
				return
			}
			// Announce 100 MB, send 1 KB, then stall until the client drops the connection.
			proto.WriteFrameFrom(b, proto.Response{}, 100<<20, io.MultiReader(bytes.NewReader(make([]byte, 1024)), b))
		}()
		return a, nil
	})
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	n, err := c.CallIO(ctx, proto.OpReadFile, proto.PathArgs{Path: `C:\big`}, nil, 0, nil, nil)
	if err != context.Canceled || n != 1024 {
		t.Fatalf("err %v, n %d", err, n)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

// A second push sends only the changed file; force sends everything; an agent without hash_files gets everything.
func TestPushSkipsUnchanged(t *testing.T) {
	c := NewClient(func(context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go fakeAgent(t, b, new([]byte))
		return a, nil
	})
	defer c.Close()
	ctx, dir := context.Background(), t.TempDir()
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check := func(force bool, wantCopied, wantSkipped int, wantSent int64) {
		t.Helper()
		n, skipped, sent, err := push(ctx, c, dir, `C:\p`, force)
		if err != nil || n != wantCopied || skipped != wantSkipped || sent != wantSent {
			t.Fatalf("force=%v: %d copied, %d skipped, %d bytes, %v; want %d, %d, %d", force, n, skipped, sent, err, wantCopied, wantSkipped, wantSent)
		}
	}
	check(false, 3, 0, 15)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	check(false, 1, 2, 7)
	if string(fakePushed[`C:\p\b.txt`]) != "changed" {
		t.Fatalf("guest b.txt = %q", fakePushed[`C:\p\b.txt`])
	}
	check(true, 3, 0, 17)
	fakeNoHash = true
	defer func() { fakeNoHash = false }()
	check(false, 3, 0, 17)
}
