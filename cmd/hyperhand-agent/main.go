// hyperhand-agent runs in the VM user's desktop session, shows a tray icon and serves
// requests from the host over a Hyper-V socket. "hyperhand-agent.exe install" installs it
// to %LOCALAPPDATA%\HyperHand and registers a logon task.
//
// Build: go build -ldflags "-H windowsgui" ./cmd/hyperhand-agent
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"hyperhand/internal/agent"
	"hyperhand/internal/hvsock"
)

const taskName = "HyperHandAgent"

var mutex windows.Handle

func main() {
	if len(os.Args) > 1 && os.Args[1] == "install" {
		if err := install(); err != nil {
			windows.MessageBox(0, windows.StringToUTF16Ptr(err.Error()), windows.StringToUTF16Ptr("HyperHand"), 0x10)
			os.Exit(1)
		}
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
		if err := copyFile(self, dst); err != nil {
			return err
		}
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	out, err := hidden("schtasks", "/Create", "/F", "/TN", taskName, "/SC", "ONLOGON", "/RL", "LIMITED",
		"/IT", "/RU", u.Username, "/TR", `"`+dst+`"`).CombinedOutput()
	if err != nil {
		return fmtErr("schtasks /Create", out, err)
	}
	// Run through the task so the agent lands in the user's interactive session.
	if out, err := hidden("schtasks", "/Run", "/TN", taskName).CombinedOutput(); err != nil {
		return fmtErr("schtasks /Run", out, err)
	}
	return nil
}

func fmtErr(what string, out []byte, err error) error {
	return fmt.Errorf("%s: %v\n%s", what, err, out)
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
