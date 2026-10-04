package agent

import (
	"encoding/json"
	"net"
	"os"
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
	for _, want := range []string{`cd /d "C:\work dir"`, `. 'C:\t\cmd.ps1'`, `OutputEncoding`, `>"C:\t\out" 2>"C:\t\err"`, `>"C:\t\code" echo %errorlevel%`} {
		if !strings.Contains(ps, want) {
			t.Errorf("powershell wrapper lacks %q:\n%s", want, ps)
		}
	}
	c := adminWrapper(proto.ExecArgs{Command: "dir /b", Shell: "cmd"}, `C:\w`, `C:\t\cmd.ps1`, `C:\t\out`, `C:\t\err`, `C:\t\code`)
	if !strings.Contains(c, `dir /b >"C:\t\out" 2>"C:\t\err"`) || strings.Contains(c, "powershell") {
		t.Errorf("cmd wrapper:\n%s", c)
	}
}
