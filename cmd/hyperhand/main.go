// Command hyperhand is the HyperHand host tray program: an MCP server (Streamable HTTP) that controls Hyper-V VMs.
//
//	hyperhand.exe [-port 8770]   run the tray and the MCP server (needs elevation)
//	hyperhand.exe install        run this exe at logon, elevated (scheduled task)
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"fyne.io/systray"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"hyperhand/internal/host"
	"hyperhand/internal/proto"
)

func main() {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "HyperHand")
	os.MkdirAll(dir, 0o755)
	if f, err := os.OpenFile(filepath.Join(dir, "hyperhand.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(f)
	}

	if len(os.Args) > 1 && os.Args[1] == "install" {
		if err := install(); err != nil {
			log.Print("install: ", err)
			msgBox("HyperHand install failed:\n"+err.Error(), windows.MB_ICONERROR)
		}
		return
	}

	port := flag.Int("port", 8770, "MCP HTTP port on 127.0.0.1")
	flag.Parse()
	if !windows.GetCurrentProcessToken().IsElevated() {
		if err := runAs(os.Args[1:]...); err != nil {
			log.Print("elevate: ", err)
		}
		return
	}
	name, _ := windows.UTF16PtrFromString(`Local\HyperHandTray`)
	if _, err := windows.CreateMutex(nil, false, name); err == windows.ERROR_ALREADY_EXISTS {
		return
	}
	if err := registerService(); err != nil {
		log.Print("register Hyper-V socket service: ", err)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", *port)
	status := "MCP: " + url
	if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port)); err != nil {
		log.Print(err)
		status = "Error: " + err.Error()
	} else {
		srv := host.NewServer(&host.Manager{})
		mux := http.NewServeMux()
		mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
		go func() { log.Print(http.Serve(ln, mux)) }()
		log.Print("listening on ", url)
	}

	systray.Run(func() {
		systray.SetIcon(icon())
		systray.SetTooltip("HyperHand")
		st := systray.AddMenuItem(status, "")
		st.Disable()
		systray.AddSeparator()
		quit := systray.AddMenuItem("退出", "")
		go func() {
			<-quit.ClickedCh
			systray.Quit()
		}()
	}, nil)
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
