// Command hyperhand is the HyperHand host tray program: an MCP server (Streamable HTTP) that controls Hyper-V VMs.
//
//	hyperhand.exe [-port 8770]   run the tray and MCP server as the current user
//	hyperhand.exe install        install the dedicated Hyper-V service and user logon task
//	hyperhand.exe uninstall      remove the host service and user logon task
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	"fyne.io/systray"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"hyperhand/internal/broker"
	"hyperhand/internal/host"
	"hyperhand/internal/proto"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "service" {
		if err := broker.Run(); err != nil {
			log.Print("service: ", err)
			os.Exit(1)
		}
		return
	}
	// Before the log file is opened, so uninstall can delete its folder.
	if len(os.Args) > 1 && os.Args[1] == "uninstall" {
		uninstall()
		return
	}
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "HyperHand")
	os.MkdirAll(dir, 0o755)
	if f, err := os.OpenFile(filepath.Join(dir, "hyperhand.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(f)
	}

	if len(os.Args) > 1 && os.Args[1] == "install" {
		if err := install(); err != nil {
			log.Print("install: ", err)
			msgBox("HyperHand install failed:\n"+err.Error(), windows.MB_ICONERROR)
			os.Exit(1)
		}
		return
	}

	port := flag.Int("port", 8770, "MCP HTTP port on 127.0.0.1")
	restartParent := flag.Uint("restart-parent", 0, "internal: wait for the previous tray process")
	flag.Parse()
	if *restartParent != 0 {
		if err := waitForPrevious(uint32(*restartParent)); err != nil {
			log.Print("restart: ", err)
			return
		}
	}
	name, _ := windows.UTF16PtrFromString(`Local\HyperHandTray`)
	mutex, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(mutex)
		return
	}
	if err != nil {
		log.Print("single instance: ", err)
		return
	}
	defer windows.CloseHandle(mutex)

	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", *port)
	status := "MCP: " + url
	var httpServer *http.Server
	if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port)); err != nil {
		log.Print(err)
		status = "Error: " + err.Error()
	} else {
		srv := host.NewServer(&host.Manager{})
		mux := http.NewServeMux()
		mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
		httpServer = &http.Server{Handler: mux}
		go func() { log.Print(httpServer.Serve(ln)) }()
		log.Print("listening on ", url)
	}
	var restart atomic.Bool
	systray.Run(func() {
		systray.SetIcon(icon())
		systray.SetTooltip("HyperHand")
		st := systray.AddMenuItem(status, "")
		st.Disable()
		if httpServer != nil {
			go func() {
				if _, err := (&broker.Client{}).ListVMs(); err != nil {
					log.Print("Hyper-V service unavailable: ", err)
					st.SetTitle("后台服务不可用，请运行 hyperhand.exe install")
				}
			}()
		}
		systray.AddSeparator()
		reload := systray.AddMenuItem("重启", "重启 HyperHand 托盘和 MCP，不重启虚拟机")
		quit := systray.AddMenuItem("退出", "")
		go func() {
			select {
			case <-reload.ClickedCh:
				restart.Store(true)
			case <-quit.ClickedCh:
			}
			systray.Quit()
		}()
	}, nil)
	if httpServer != nil {
		httpServer.Close()
	}
	if restart.Load() {
		if err := startReplacement(*port); err != nil {
			log.Print("restart: ", err)
			msgBox("HyperHand restart failed:\n"+err.Error(), windows.MB_ICONERROR)
		}
	}
}

// The replacement waits for this process, including its mutex and pipe handles, to exit.
func startReplacement(port int) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "-port", strconv.Itoa(port), "-restart-parent", strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func waitForPrevious(pid uint32) error {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err == windows.ERROR_INVALID_PARAMETER { // Already exited.
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	state, err := windows.WaitForSingleObject(h, 30000)
	if err != nil {
		return err
	}
	if state != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("previous HyperHand process did not exit")
	}
	return nil
}

// registerService registers proto.ServiceID as a Hyper-V socket guest communication service.
func registerService() error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\`+proto.ServiceID, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer k.Close()
	if v, _, err := k.GetStringValue("ElementName"); err == nil && v == "HyperHand" {
		return nil
	}
	return k.SetStringValue("ElementName", "HyperHand")
}

// runAs relaunches this executable elevated (UAC prompt) with args.
func runAs(args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	for i, a := range args {
		args[i] = syscall.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(self)
	params, _ := windows.UTF16PtrFromString(strings.Join(args, " "))
	return windows.ShellExecute(0, verb, file, params, nil, windows.SW_NORMAL)
}

func msgBox(text string, flags uint32) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("HyperHand")
	windows.MessageBox(0, t, c, flags)
}
