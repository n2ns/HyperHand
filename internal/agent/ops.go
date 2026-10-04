package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/kbinani/screenshot"
	"golang.org/x/sys/windows"

	"hyperhand/internal/proto"
)

func timeout(ms int) time.Duration {
	if ms <= 0 {
		return 60 * time.Second
	}
	return time.Duration(ms) * time.Millisecond
}

func execOp(args json.RawMessage, _ []byte) (any, []byte, error) {
	var a proto.ExecArgs
	if err := decode(args, &a); err != nil {
		return nil, nil, err
	}
	var cmd *exec.Cmd
	attr := &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	switch a.Shell {
	case "", "powershell":
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
			"-Command", "[Console]::OutputEncoding=[Text.Encoding]::UTF8;"+a.Command)
	case "cmd":
		cmd = exec.Command("cmd.exe")
		attr.CmdLine = `cmd.exe /d /s /c "` + a.Command + `"`
	default:
		return nil, nil, fmt.Errorf("unknown shell %q", a.Shell)
	}
	cmd.SysProcAttr = attr
	cmd.Dir = a.Cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var r proto.ExecResult
	select {
	case <-done:
	case <-time.After(timeout(a.TimeoutMs)):
		r.TimedOut = true
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		kill.Run()
		<-done
	}
	r.ExitCode = cmd.ProcessState.ExitCode()
	r.Stdout, r.Stderr = toUTF8(stdout.Bytes()), toUTF8(stderr.Bytes())
	return r, nil, nil
}

// toUTF8 keeps valid UTF-8 as is and otherwise decodes from the OEM code page (cmd's output).
const cpOEM = 1 // CP_OEMCP

func toUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	n, _ := windows.MultiByteToWideChar(cpOEM, 0, &b[0], int32(len(b)), nil, 0)
	if n <= 0 {
		return string(b)
	}
	u := make([]uint16, n)
	windows.MultiByteToWideChar(cpOEM, 0, &b[0], int32(len(b)), &u[0], n)
	return string(utf16.Decode(u))
}

func writeFile(args json.RawMessage, payload []byte) (any, []byte, error) {
	var a proto.PathArgs
	if err := decode(args, &a); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
		return nil, nil, err
	}
	return nil, nil, os.WriteFile(a.Path, payload, 0o644)
}

func readFile(args json.RawMessage, _ []byte) (any, []byte, error) {
	var a proto.PathArgs
	if err := decode(args, &a); err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(a.Path)
	return nil, b, err
}

func screenshotOp(json.RawMessage, []byte) (any, []byte, error) {
	img, err := screenshot.CaptureDisplay(0)
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, nil, err
	}
	return nil, buf.Bytes(), nil
}

func waitOp(args json.RawMessage, _ []byte) (any, []byte, error) {
	var a proto.WaitArgs
	if err := decode(args, &a); err != nil {
		return nil, nil, err
	}
	var check func() bool
	switch a.Kind {
	case "process_running":
		check = func() bool { return processRunning(a.Name) }
	case "process_exit":
		check = func() bool { return !processRunning(a.Name) }
	case "file_exists":
		check = func() bool { _, err := os.Stat(a.Path); return err == nil }
	default:
		return nil, nil, fmt.Errorf("unknown wait kind %q", a.Kind)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout(a.TimeoutMs))
	defer cancel()
	for {
		if check() {
			return proto.WaitResult{Satisfied: true}, nil, nil
		}
		select {
		case <-ctx.Done():
			return proto.WaitResult{}, nil, nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func processRunning(name string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".exe") + ".exe"
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.ToLower(windows.UTF16ToString(e.ExeFile[:])) == name {
			return true
		}
	}
	return false
}
