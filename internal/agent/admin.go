package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"hyperhand/internal/proto"
)

var (
	shell32          = windows.NewLazySystemDLL("shell32.dll")
	pShellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x40
	seeMaskNoAsync        = 0x100
	seeMaskFlagNoUI       = 0x400
)

// runElevated starts file with params via ShellExecuteEx "runas" (hidden) and returns the process handle.
func runElevated(file, params string) (windows.Handle, error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	f, _ := windows.UTF16PtrFromString(file)
	p, _ := windows.UTF16PtrFromString(params)
	sei := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		lpVerb:       verb,
		lpFile:       f,
		lpParameters: p,
		nShow:        windows.SW_HIDE,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	if r, _, err := pShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei))); r == 0 {
		return 0, fmt.Errorf("ShellExecuteEx runas: %w", err)
	}
	if sei.hProcess == 0 {
		return 0, fmt.Errorf("ShellExecuteEx runas: no process handle")
	}
	return sei.hProcess, nil
}

// adminWrapper returns the .cmd wrapper run elevated: it changes to cwd, runs the command (via cmd /s /c for shell
// "cmd", or the script ps1 for powershell) with stdout/stderr redirected to out/errf and writes the exit code to code.
func adminWrapper(a proto.ExecArgs, cwd, ps1, out, errf, code string) string {
	// Like normal exec's cmd /s /c; % doubled so the batch file passes it through unexpanded.
	run := `cmd.exe /d /s /c "` + strings.ReplaceAll(a.Command, "%", "%%") + `"`
	if a.Shell == "" || a.Shell == "powershell" {
		// -File (not -Command ". script") so `exit N` in the command becomes the exit code.
		run = `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "` + ps1 + `"`
	}
	return "@echo off\r\nchcp 65001 >nul\r\n" +
		`cd /d "` + cwd + `" || (>"` + code + `" echo 1& exit /b 1)` + "\r\n" +
		run + ` >"` + out + `" 2>"` + errf + `"` + "\r\n" +
		`>"` + code + `" echo %errorlevel%` + "\r\n"
}

// adminScript returns the ps1 run elevated for powershell; the last line gives the exit code
// -Command would: 1 when the last statement failed (also for a failing native command).
func adminScript(command string) string {
	// UTF-8 BOM so Windows PowerShell reads the script as UTF-8.
	return "\xEF\xBB\xBF[Console]::OutputEncoding=[Text.Encoding]::UTF8\r\n" + command +
		"\r\nif (-not $?) { exit 1 }\r\n"
}

func execAdmin(ctx context.Context, a proto.ExecArgs) (any, []byte, error) {
	if a.Shell != "" && a.Shell != "powershell" && a.Shell != "cmd" {
		return nil, nil, fmt.Errorf("unknown shell %q", a.Shell)
	}
	dir, err := os.MkdirTemp("", "hh-admin-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	cwd := a.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	ps1, out, errf, code := filepath.Join(dir, "cmd.ps1"), filepath.Join(dir, "out"), filepath.Join(dir, "err"), filepath.Join(dir, "code")
	wrapper := filepath.Join(dir, "run.cmd")
	if err := os.WriteFile(ps1, []byte(adminScript(a.Command)), 0o644); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(wrapper, []byte(adminWrapper(a, cwd, ps1, out, errf, code)), 0o644); err != nil {
		return nil, nil, err
	}
	h, err := runElevated("cmd.exe", `/d /c "`+wrapper+`"`)
	if err != nil {
		return nil, nil, err
	}
	defer windows.CloseHandle(h)
	var r proto.ExecResult
	deadline := time.Now().Add(timeout(a.TimeoutMs))
	for {
		if ev, _ := windows.WaitForSingleObject(h, 200); ev != uint32(windows.WAIT_TIMEOUT) {
			break
		}
		if ctx.Err() != nil {
			break
		}
		if time.Now().After(deadline) {
			r.TimedOut = true
			break
		}
	}
	if r.TimedOut || ctx.Err() != nil {
		if pid, err := windows.GetProcessId(h); err == nil {
			if k, err := runElevated("taskkill.exe", "/T /F /PID "+strconv.Itoa(int(pid))); err == nil {
				windows.WaitForSingleObject(k, 10000)
				windows.CloseHandle(k)
			}
		}
		windows.WaitForSingleObject(h, 5000)
		if !r.TimedOut {
			return nil, nil, ctx.Err()
		}
	}
	if b, err := os.ReadFile(code); err == nil {
		r.ExitCode, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	} else {
		var c uint32
		windows.GetExitCodeProcess(h, &c)
		r.ExitCode = int(int32(c))
	}
	so, _ := os.ReadFile(out)
	se, _ := os.ReadFile(errf)
	r.Stdout, r.Stderr = toUTF8(so), toUTF8(se)
	return r, nil, nil
}
