// hyperhand-agent runs in the VM user's desktop session, shows a tray icon and serves
// requests from the host over a Hyper-V socket. "hyperhand-agent.exe install" installs it
// to %LOCALAPPDATA%\HyperHand and starts it at logon (HKCU Run key, no admin needed);
// "hyperhand-agent.exe uninstall" stops it, removes the Run value and deletes
// %LOCALAPPDATA%\HyperHand and C:\Users\Public\HyperHand (also no admin needed).
//
// Build: go build -ldflags "-H windowsgui" ./cmd/hyperhand-agent
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"hyperhand/internal/agent"
	"hyperhand/internal/hvsock"
)

var mutex windows.Handle

func main() {
	if len(os.Args) > 1 && os.Args[1] == "install" {
		if err := install(); err != nil {
			windows.MessageBox(0, windows.StringToUTF16Ptr(err.Error()), windows.StringToUTF16Ptr("HyperHand"), 0x10)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "uninstall" {
		uninstall()
		return
	}
	h, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr("HyperHandAgent"))
	if err == windows.ERROR_ALREADY_EXISTS {
		return
	}
	mutex = h
	windows.NewLazySystemDLL("user32.dll").NewProc("SetProcessDPIAware").Call()
	if exe, err := os.Executable(); err == nil {
		go func() { // the previous version may still be exiting after update_agent
			for i := 0; i < 20; i++ {
				if err := os.Remove(exe + ".old"); err == nil || os.IsNotExist(err) {
					return
				}
				time.Sleep(500 * time.Millisecond)
			}
		}()
	}
	agent.AfterUpdate = restart
	systray.Run(onReady, nil)
}

func onReady() {
	systray.SetIcon(makeIcon())
	systray.SetTooltip("HyperHand")
	status := systray.AddMenuItem("等待宿主机连接", "")
	status.Disable()
	quit := systray.AddMenuItem("退出", "")
	go func() {
		<-quit.ClickedCh
		systray.Quit()
	}()
	go func() {
		for {
			l, err := hvsock.Listen()
			if err != nil {
				time.Sleep(time.Second)
				continue
			}
			for {
				c, err := l.Accept()
				if err != nil {
					break
				}
				status.SetTitle("宿主机已连接")
				agent.Serve(c)
				c.Close()
				status.SetTitle("等待宿主机连接")
			}
			l.Close()
			time.Sleep(time.Second)
		}
	}()
}

// restart starts the (already replaced) exe and exits.
func restart() {
	windows.CloseHandle(mutex)
	if exe, err := os.Executable(); err == nil {
		exec.Command(exe, os.Args[1:]...).Start()
	}
	os.Exit(0)
}

func hidden(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return c
}

func install() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "HyperHand")
	dst := filepath.Join(dir, "hyperhand-agent.exe")
	// Stop any running instance (other than this process).
	hidden("taskkill", "/F", "/IM", "hyperhand-agent.exe", "/FI", "PID ne "+strconv.Itoa(os.Getpid())).Run()
	time.Sleep(500 * time.Millisecond)
	if !samePath(self, dst) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		// The killed instance may still hold its image for a moment.
		for i := 0; ; i++ {
			if err = copyFile(self, dst); err == nil || i == 20 {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if err != nil {
			return err
		}
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue("HyperHandAgent", `"`+dst+`"`); err != nil {
		return err
	}
	return exec.Command(dst).Start()
}

// uninstall undoes install (and the C:\Users\Public staging copy); every step runs even if
// an earlier one fails, and the result is shown in a message box.
func uninstall() {
	var done, failed []string
	pid := strconv.Itoa(os.Getpid())
	hidden("taskkill", "/F", "/IM", "hyperhand-agent.exe", "/FI", "PID ne "+pid).Run()
	for i := 0; i < 20; i++ {
		out, _ := hidden("tasklist", "/NH", "/FI", "IMAGENAME eq hyperhand-agent.exe", "/FI", "PID ne "+pid).Output()
		if !strings.Contains(strings.ToLower(string(out)), "hyperhand-agent.exe") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	done = append(done, "已停止其他 hyperhand-agent.exe 进程")

	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE); err != nil {
		failed = append(failed, `HKCU\...\Run\HyperHandAgent: `+err.Error())
	} else {
		if err := k.DeleteValue("HyperHandAgent"); err != nil && err != registry.ErrNotExist {
			failed = append(failed, `HKCU\...\Run\HyperHandAgent: `+err.Error())
		} else {
			done = append(done, `HKCU\...\Run\HyperHandAgent`)
		}
		k.Close()
	}

	self, _ := os.Executable()
	for _, dir := range []string{filepath.Join(os.Getenv("LOCALAPPDATA"), "HyperHand"), `C:\Users\Public\HyperHand`} {
		inside := self != "" && strings.HasPrefix(strings.ToLower(self), strings.ToLower(dir)+`\`)
		err := os.RemoveAll(dir) // removes what it can even when some entries fail
		switch {
		case err == nil:
			done = append(done, dir)
		case inside: // our own exe is locked until we exit
			c := cleanupCommand(dir, os.Getpid())
			if err := c.Start(); err != nil {
				failed = append(failed, dir+": "+err.Error())
			} else {
				done = append(done, dir+"（本程序退出后删除）")
			}
		default:
			failed = append(failed, dir+": "+err.Error())
		}
	}

	msg := "已移除:\n" + strings.Join(done, "\n")
	flags := uint32(0x40) // MB_ICONINFORMATION
	if len(failed) > 0 {
		msg += "\n\n失败:\n" + strings.Join(failed, "\n")
		flags = 0x30 // MB_ICONWARNING
	}
	windows.MessageBox(0, windows.StringToUTF16Ptr(msg), windows.StringToUTF16Ptr("HyperHand 卸载"), flags)
}

// cleanupCommand waits for the uninstaller (including its message box) to exit.
func cleanupCommand(dir string, pid int) *exec.Cmd {
	script := `$ErrorActionPreference = 'Stop'
try { $process = [System.Diagnostics.Process]::GetProcessById(` + strconv.Itoa(pid) + `) }
catch [System.ArgumentException] { $process = $null }
if ($null -ne $process) { $process.WaitForExit(); $process.Dispose() }
$target = '` + strings.ReplaceAll(dir, "'", "''") + `'
for ($attempt = 0; $attempt -lt 20; $attempt++) {
    try {
        if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Recurse -Force }
        exit 0
    } catch { Start-Sleep -Milliseconds 250 }
}
exit 1`
	var encoded bytes.Buffer
	binary.Write(&encoded, binary.LittleEndian, utf16.Encode([]rune(script)))
	c := hidden("powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded.Bytes()))
	// Do not inherit a working directory that the helper itself must remove.
	c.Dir = filepath.Dir(dir)
	return c
}

func samePath(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// makeIcon returns a 32x32 .ico (PNG-compressed entry): a blue rounded square with a white dot.
func makeIcon() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			dx, dy := float64(x)-15.5, float64(y)-15.5
			switch {
			case dx*dx+dy*dy < 36:
				img.Set(x, y, color.White)
			case (x > 1 && x < 30 && y > 1 && y < 30):
				img.Set(x, y, color.NRGBA{0x1e, 0x6f, 0xd9, 0xff})
			}
		}
	}
	var p bytes.Buffer
	png.Encode(&p, img)
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, []uint16{0, 1, 1}) // reserved, type=icon, count
	b.Write([]byte{32, 32, 0, 0})                            // width, height, colors, reserved
	binary.Write(&b, binary.LittleEndian, []uint16{1, 32})   // planes, bpp
	binary.Write(&b, binary.LittleEndian, []uint32{uint32(p.Len()), 22})
	b.Write(p.Bytes())
	return b.Bytes()
}
