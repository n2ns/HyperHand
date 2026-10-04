package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hyperhand/internal/proto"
)

func call(t *testing.T, c net.Conn, op string, args any, payload []byte, result any) []byte {
	t.Helper()
	req := proto.Request{Op: op}
	if args != nil {
		req.Args, _ = json.Marshal(args)
	}
	if err := proto.WriteFrame(c, req, payload); err != nil {
		t.Fatal(err)
	}
	var resp proto.Response
	out, err := proto.ReadFrame(c, &resp)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" {
		t.Fatalf("%s: %s", op, resp.Error)
	}
	if result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestServe(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go Serve(server)

	var p proto.PingResult
	call(t, client, proto.OpPing, nil, nil, &p)
	if p.Version != proto.Version || p.Hostname == "" || p.User == "" {
		t.Fatalf("ping: %+v", p)
	}

	var e proto.ExecResult
	call(t, client, proto.OpExec, proto.ExecArgs{Command: "Write-Output hi"}, nil, &e)
	if strings.TrimSpace(e.Stdout) != "hi" || e.ExitCode != 0 || e.TimedOut {
		t.Fatalf("exec: %+v", e)
	}

	path := filepath.Join(t.TempDir(), "a", "b", "f.txt")
	data := []byte("hello 世界")
	call(t, client, proto.OpWriteFile, proto.PathArgs{Path: path}, data, nil)
	if got := call(t, client, proto.OpReadFile, proto.PathArgs{Path: path}, nil, nil); string(got) != string(data) {
		t.Fatalf("read_file: %q", got)
	}

	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "f.txt"), nil, 0o644)
	var ld proto.ListDirResult
	call(t, client, proto.OpListDir, proto.PathArgs{Path: dir}, nil, &ld)
	if len(ld.Entries) != 2 || ld.Entries[0] != (proto.DirEntry{Name: "f.txt"}) || ld.Entries[1] != (proto.DirEntry{Name: "sub", IsDir: true}) {
		t.Fatalf("list_dir: %+v", ld)
	}

	late := filepath.Join(t.TempDir(), "late.txt")
	time.AfterFunc(500*time.Millisecond, func() { os.WriteFile(late, nil, 0o644) })
	var w proto.WaitResult
	call(t, client, proto.OpWait, proto.WaitArgs{Kind: "file_exists", Path: late, TimeoutMs: 5000}, nil, &w)
	if !w.Satisfied {
		t.Fatal("wait file_exists not satisfied")
	}
	call(t, client, proto.OpWait, proto.WaitArgs{Kind: "file_exists", Path: late + ".no", TimeoutMs: 400}, nil, &w)
	if w.Satisfied {
		t.Fatal("wait should time out")
	}

	// errors come back in Response.Error
	proto.WriteFrame(client, proto.Request{Op: "nope"}, nil)
	var resp proto.Response
	if _, err := proto.ReadFrame(client, &resp); err != nil || resp.Error == "" {
		t.Fatalf("unknown op: %v %+v", err, resp)
	}
}

func TestAdminWrapper(t *testing.T) {
	ps := adminWrapper(proto.ExecArgs{Command: "Get-Date"}, `C:\work dir`, `C:\t\cmd.ps1`, `C:\t\out`, `C:\t\err`, `C:\t\code`)
	for _, want := range []string{`cd /d "C:\work dir"`, `-File "C:\t\cmd.ps1"`, `>"C:\t\out" 2>"C:\t\err"`, `>"C:\t\code" echo %errorlevel%`} {
		if !strings.Contains(ps, want) {
			t.Errorf("powershell wrapper lacks %q:\n%s", want, ps)
		}
	}
	c := adminWrapper(proto.ExecArgs{Command: "echo 100% & dir /b", Shell: "cmd"}, `C:\w`, `C:\t\cmd.ps1`, `C:\t\out`, `C:\t\err`, `C:\t\code`)
	if want := `cmd.exe /d /s /c "echo 100%% & dir /b" >"C:\t\out" 2>"C:\t\err"`; !strings.Contains(c, want) {
		t.Errorf("cmd wrapper lacks %q:\n%s", want, c)
	}
}

// The admin ps1 (run with -File) must give the same exit code as normal exec (-Command).
func TestAdminScriptExitCode(t *testing.T) {
	dir := t.TempDir()
	for i, command := range []string{"exit 3", `Get-Item C:\nope-hh`, "cmd /c exit 5", "Write-Output ok", "cmd /c exit 0"} {
		r, _, err := execOp(context.Background(), mustJSON(proto.ExecArgs{Command: command}), nil)
		if err != nil {
			t.Fatal(err)
		}
		want := r.(proto.ExecResult).ExitCode
		ps1 := filepath.Join(dir, fmt.Sprintf("s%d.ps1", i))
		os.WriteFile(ps1, []byte(adminScript(command)), 0o644)
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", ps1)
		cmd.Run()
		if got := cmd.ProcessState.ExitCode(); got != want {
			t.Errorf("%q: admin script exit %d, exec exit %d", command, got, want)
		}
		t.Logf("%q -> %d", command, want)
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestToUTF8StripsBOM(t *testing.T) {
	if s := toUTF8([]byte("\xEF\xBB\xBFhi")); s != "hi" {
		t.Fatalf("%q", s)
	}
}

func TestListDirSkipsLinks(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "real"), 0o755)
	if err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(dir, "junction"), filepath.Join(dir, "real")).Run(); err != nil {
		t.Skip("mklink /J:", err)
	}
	os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "symlink")) // needs admin or developer mode; fine if it fails
	r, _, err := listDir(context.Background(), mustJSON(proto.PathArgs{Path: dir}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if e := r.(proto.ListDirResult).Entries; len(e) != 1 || e[0].Name != "real" {
		t.Fatalf("entries: %+v", e)
	}
}

func TestWaitArgs(t *testing.T) {
	ctx := context.Background()
	for _, a := range []proto.WaitArgs{{Kind: "process_running"}, {Kind: "process_exit"}, {Kind: "file_exists"}} {
		if _, _, err := waitOp(ctx, mustJSON(a), nil); err == nil {
			t.Errorf("%+v: want error", a)
		}
	}
	for _, name := range []string{"svchost", "svchost.exe", `C:\Windows\System32\svchost.exe`} {
		r, _, err := waitOp(ctx, mustJSON(proto.WaitArgs{Kind: "process_running", Name: name, TimeoutMs: 1000}), nil)
		if err != nil || !r.(proto.WaitResult).Satisfied {
			t.Errorf("%q: %v %+v", name, err, r)
		}
	}
}

func TestDispatchPanic(t *testing.T) {
	handlers["test_panic"] = func(context.Context, json.RawMessage, []byte) (any, []byte, error) { panic("boom") }
	defer delete(handlers, "test_panic")
	if _, _, err := Dispatch(context.Background(), "test_panic", nil, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

// A request is cancelled (and its process killed) when the host goes away.
func TestServeCancel(t *testing.T) {
	client, server := net.Pipe()
	served := make(chan error, 1)
	go func() { served <- Serve(server) }()
	if err := proto.WriteFrame(client, proto.Request{Op: proto.OpExec, Args: mustJSON(proto.ExecArgs{Command: "Start-Sleep 30", TimeoutMs: 60000})}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // let powershell start
	start := time.Now()
	client.Close()
	select {
	case <-served:
		if d := time.Since(start); d > 10*time.Second {
			t.Fatalf("Serve returned after %v", d)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("exec not cancelled")
	}
}

// The watcher stops between requests, so consecutive requests still work.
func TestServeSequential(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go Serve(server)
	for i := 0; i < 5; i++ {
		var p proto.PingResult
		call(t, client, proto.OpPing, nil, nil, &p)
	}
}

// A multi-MB write_file is streamed to the file and the stream stays in sync.
func TestServeWriteFileLarge(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go Serve(server)
	data := make([]byte, 5<<20+123)
	for i := range data {
		data[i] = byte(i * 7)
	}
	path := filepath.Join(t.TempDir(), "big", "f.bin")
	call(t, client, proto.OpWriteFile, proto.PathArgs{Path: path}, data, nil)
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("file content differs (err %v, len %d)", err, len(got))
	}
	if _, err := os.Stat(path + ".hhpart"); err == nil {
		t.Fatal(".hhpart left behind")
	}
	if got := call(t, client, proto.OpReadFile, proto.PathArgs{Path: path}, nil, nil); !bytes.Equal(got, data) {
		t.Fatal("read_file content differs")
	}
	var p proto.PingResult
	call(t, client, proto.OpPing, nil, nil, &p)
}

// A failing write_file returns an error, its payload is skipped and the next request works.
func TestServeWriteFileError(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go Serve(server)
	for _, args := range []json.RawMessage{mustJSON(proto.PathArgs{Path: `Q:\nope\<bad>|f.txt`}), json.RawMessage(`"x"`)} {
		proto.WriteFrame(client, proto.Request{Op: proto.OpWriteFile, Args: args}, make([]byte, 3<<20))
		var resp proto.Response
		if _, err := proto.ReadFrame(client, &resp); err != nil || resp.Error == "" {
			t.Fatalf("%s: want error, got %v %+v", args, err, resp)
		}
		var p proto.PingResult
		call(t, client, proto.OpPing, nil, nil, &p)
	}
	proto.WriteFrame(client, proto.Request{Op: proto.OpReadFile, Args: mustJSON(proto.PathArgs{Path: `C:\nope-hh.txt`})}, nil)
	var resp proto.Response
	if out, err := proto.ReadFrame(client, &resp); err != nil || resp.Error == "" || len(out) != 0 {
		t.Fatalf("read_file missing: %v %+v", err, resp)
	}
	var p proto.PingResult
	call(t, client, proto.OpPing, nil, nil, &p)
}

func TestHashFiles(t *testing.T) {
	dir := t.TempDir()
	var paths, want []string
	for i, content := range []string{"hello", strings.Repeat("x", 3<<20)} {
		p := filepath.Join(dir, fmt.Sprintf("f%d", i))
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(content))
		paths, want = append(paths, p), append(want, hex.EncodeToString(sum[:]))
	}
	paths, want = append(paths, filepath.Join(dir, "missing"), dir), append(want, "", "")
	res, _, err := Dispatch(context.Background(), proto.OpHashFiles, mustJSON(proto.PathsArgs{Paths: paths}), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.(proto.HashesResult).Hashes
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
