package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows"

	"hyperhand/internal/broker"
	"hyperhand/internal/credential"
	"hyperhand/internal/proto"
)

// settingsWindow is the host settings window opened from the tray: the MCP endpoint, the service state and the
// host-side settings of each VM. At most one is open; it runs on its own locked OS thread, beside the tray's.
type settingsWindow struct {
	wnd      *ui.Main
	info     *trayInfo
	url      *ui.Edit
	port     *ui.Edit
	editPort *ui.CheckBox
	apply    *ui.Button
	service  *ui.Static
	enhanced *ui.Static
	list     *ui.ListView
	onStart  *ui.CheckBox
	console  *ui.Button
	setPw    *ui.Button
	clearPw  *ui.Button
	settings *hostSettings
	style    *windowStyle
	vms      []vmRow // rows shown in list, in order
}

type vmRow struct {
	name, state string
	password    bool
}

// trayInfo is what the tray tells the settings window about the running MCP server.
type trayInfo struct {
	port      int
	explicit  bool   // -port was given on the command line, so the saved port does not apply
	listenErr string // why the MCP server is not running, or ""
	settings  *hostSettings
	restart   func() // restarts the tray, which then reads the saved port
}

func (t *trayInfo) url() string { return fmt.Sprintf("http://127.0.0.1:%d/mcp", t.port) }

const settingsTimer = 1

var (
	settingsMu   sync.Mutex
	settingsHwnd win.HWND // the open settings window, or 0
)

// showSettings brings the open settings window to the front, or opens a new one.
func showSettings(info *trayInfo) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if settingsHwnd != 0 {
		if settingsHwnd.IsIconic() {
			settingsHwnd.ShowWindow(co.SW_RESTORE)
		}
		settingsHwnd.SetForegroundWindow()
		return
	}
	settingsHwnd = 1 // opening; a second click meanwhile does nothing
	go func() {
		runtime.LockOSThread() // the window and its message loop belong to this thread
		defer func() {
			if r := recover(); r != nil { // windigo panics on Win32 errors; keep the tray alive
				log.Print("settings window: ", r)
				msgBox(fmt.Sprintf("The settings window failed: %v", r), windows.MB_ICONERROR)
			}
			settingsMu.Lock()
			settingsHwnd = 0
			settingsMu.Unlock()
		}()
		newSettingsWindow(info).wnd.RunAsMain()
	}()
}

func newSettingsWindow(info *trayInfo) *settingsWindow {
	settings := info.settings
	url := info.url()
	wnd := ui.NewMain(ui.OptsMain().
		Title("HyperHand Settings").
		Size(ui.Dpi(600, 600)).
		ClassBrush(win.HBRUSH(co.COLOR_WINDOW + 1)).
		Center(true))
	style := newWindowStyle()
	me := &settingsWindow{wnd: wnd, info: info, settings: settings, style: style}

	const left, valueX, right = 24, 168, 576 // margins and the value column
	label := func(text string, y int) *ui.Static {
		return ui.NewStatic(wnd, ui.OptsStatic().Text(text).Position(ui.Dpi(left, y)).Size(ui.Dpi(valueX-left-8, 20)))
	}
	value := func(text string, y, width int) *ui.Static {
		return ui.NewStatic(wnd, ui.OptsStatic().Text(text).Position(ui.Dpi(valueX, y)).Size(ui.Dpi(width, 20)))
	}
	section := func(text string, y int) *ui.Static {
		s := ui.NewStatic(wnd, ui.OptsStatic().Text(text).Position(ui.Dpi(left, y)).Size(ui.Dpi(right-left, 24)))
		ui.NewStatic(wnd, ui.OptsStatic().Position(ui.Dpi(left, y+28)).Size(ui.Dpi(right-left, 2)).CtrlStyle(co.SS(0x10))) // SS_ETCHEDHORZ
		return s
	}

	title := ui.NewStatic(wnd, ui.OptsStatic().Text("HyperHand").Position(ui.Dpi(left, 16)).Size(ui.Dpi(300, 34)))
	subtitle := ui.NewStatic(wnd, ui.OptsStatic().Text("Host settings for AI control of Hyper-V virtual machines").
		Position(ui.Dpi(left, 52)).Size(ui.Dpi(right-left, 20)))

	mcpHead := section("MCP server", 88)
	lblEndpoint := label("Endpoint", 132)
	me.url = ui.NewEdit(wnd, ui.OptsEdit().Text(url).Position(ui.Dpi(valueX, 128)).Width(ui.DpiX(300)).
		CtrlStyle(co.ES_AUTOHSCROLL|co.ES_READONLY))
	copyURL := ui.NewButton(wnd, ui.OptsButton().Text("&Copy").Position(ui.Dpi(right-96, 127)).Width(ui.DpiX(96)))
	lblPort := label("Port", 166)
	me.port = ui.NewEdit(wnd, ui.OptsEdit().Text(fmt.Sprint(info.port)).Position(ui.Dpi(valueX, 162)).Width(ui.DpiX(72)).
		CtrlStyle(co.ES_AUTOHSCROLL|co.ES_NUMBER|co.ES_READONLY))
	portText := "Change port"
	if info.explicit {
		portText = "Change port (set by -port)"
	}
	me.editPort = ui.NewCheckBox(wnd, ui.OptsCheckBox().Text(portText).Position(ui.Dpi(valueX+88, 165)))
	me.apply = ui.NewButton(wnd, ui.OptsButton().Text("&Apply and restart").Position(ui.Dpi(right-140, 161)).Width(ui.DpiX(140)))
	lblServer := label("Status", 198)
	serverText, serverColor := "● Running", colorGood
	if info.listenErr != "" {
		serverText, serverColor = "● Not running: "+info.listenErr, colorBad
	}
	server := value(serverText, 198, right-valueX)

	hostHead := section("Host", 236)
	lblService := label("Background service", 280)
	me.service = value("Checking...", 280, right-valueX)
	lblVersion := label("Version", 308)
	version := value(proto.Version, 308, 200)
	logs := ui.NewButton(wnd, ui.OptsButton().Text("Open &log folder").Position(ui.Dpi(right-140, 302)).Width(ui.DpiX(140)))
	me.enhanced = ui.NewStatic(wnd, ui.OptsStatic().Position(ui.Dpi(left, 336)).Size(ui.Dpi(right-left, 34)))

	vmHead := section("Virtual machines", 376)
	me.list = ui.NewListView(wnd, ui.OptsListView().
		Position(ui.Dpi(left, 418)).
		Size(ui.Dpi(right-left, 112)).
		CtrlStyle(co.LVS_REPORT|co.LVS_NOSORTHEADER|co.LVS_SHOWSELALWAYS|co.LVS_SINGLESEL|co.LVS_SHAREIMAGELISTS).
		CtrlExStyle(co.LVS_EX_FULLROWSELECT|co.LVS_EX_DOUBLEBUFFER).
		Column("Name", ui.DpiX(190)).
		Column("State", ui.DpiX(90)).
		Column("Unlock password", ui.DpiX(130)).
		Column("Console when started", ui.DpiX(130)))
	me.onStart = ui.NewCheckBox(wnd, ui.OptsCheckBox().Text("Open console when &started").Position(ui.Dpi(left, 544)))
	me.clearPw = ui.NewButton(wnd, ui.OptsButton().Text("C&lear password").Position(ui.Dpi(right-120, 540)).Width(ui.DpiX(120)))
	me.setPw = ui.NewButton(wnd, ui.OptsButton().Text("Set unlock pass&word...").Position(ui.Dpi(right-120-8-160, 540)).Width(ui.DpiX(160)))
	me.console = ui.NewButton(wnd, ui.OptsButton().Text("&Open console").Position(ui.Dpi(right-120-8-160-8-110, 540)).Width(ui.DpiX(110)))

	wnd.On().WmCreate(func(_ ui.WmCreate) int {
		settingsMu.Lock()
		settingsHwnd = wnd.Hwnd()
		settingsMu.Unlock()
		setFont(title.Hwnd(), style.title)
		for _, s := range []*ui.Static{mcpHead, hostHead, vmHead} {
			setFont(s.Hwnd(), style.section)
		}
		for _, s := range []*ui.Static{subtitle, lblEndpoint, lblPort, lblServer, lblService, lblVersion} {
			style.colors[s.Hwnd()] = colorLabel
		}
		style.colors[server.Hwnd()] = serverColor
		style.colors[me.enhanced.Hwnd()] = colorWarn
		style.colors[version.Hwnd()] = colorText
		style.readOnly[me.url.Hwnd()] = true
		style.readOnly[me.port.Hwnd()] = true
		explorerTheme(me.list.Hwnd())
		me.updateButtons()
		me.apply.Hwnd().EnableWindow(false)
		me.editPort.Hwnd().EnableWindow(!info.explicit)
		me.refresh()
		wnd.Hwnd().SetTimer(settingsTimer, 5000)
		return 0
	})
	wnd.On().WmCtlColorStatic(style.ctlColor)
	wnd.On().WmDestroy(style.free)
	wnd.On().WmTimer(settingsTimer, me.refresh)
	me.editPort.On().BnClicked(func() {
		on := me.editPort.IsChecked()
		const emSetReadOnly = 0x00CF
		me.port.Hwnd().SendMessage(co.WM(emSetReadOnly), win.WPARAM(boolInt(!on)), 0)
		style.readOnly[me.port.Hwnd()] = !on
		me.port.Hwnd().InvalidateRect(nil, true)
		me.apply.Hwnd().EnableWindow(on)
		if on {
			me.port.Hwnd().SetFocus()
		} else {
			me.port.SetText(fmt.Sprint(info.port))
		}
	})
	me.apply.On().BnClicked(me.applyPort)
	copyURL.On().BnClicked(func() {
		if err := setClipboard(wnd.Hwnd(), url); err != nil {
			wnd.Hwnd().MessageBox("Copying failed: "+err.Error(), "HyperHand", co.MB_ICONERROR)
		}
	})
	logs.On().BnClicked(func() {
		exec.Command("explorer.exe", filepath.Join(os.Getenv("LOCALAPPDATA"), "HyperHand")).Start()
	})
	me.list.On().LvnItemChanged(func(_ *win.NMLISTVIEW) { me.updateButtons() })
	me.onStart.On().BnClicked(func() {
		if name, ok := me.selected(); ok {
			if err := settings.setOnStart(name, me.onStart.IsChecked()); err != nil {
				wnd.Hwnd().MessageBox("Saving the setting failed: "+err.Error(), "HyperHand", co.MB_ICONERROR)
			}
			me.refresh()
		}
	})
	me.console.On().BnClicked(func() {
		if name, ok := me.selected(); ok {
			go func() {
				if err := openConsole(name); err != nil {
					msgBox("Opening the console failed:\n"+err.Error(), windows.MB_ICONERROR)
				}
			}()
		}
	})
	me.setPw.On().BnClicked(func() {
		if name, ok := me.selected(); ok {
			go func() {
				setPassword(name)
				wnd.UiThread(me.refresh)
			}()
		}
	})
	me.clearPw.On().BnClicked(func() {
		if name, ok := me.selected(); ok {
			if err := credential.Delete(name); err != nil {
				wnd.Hwnd().MessageBox("Clearing the unlock password failed: "+err.Error(), "HyperHand", co.MB_ICONERROR)
			}
			me.refresh()
		}
	})
	return me
}

// refresh reads the VMs from the service in the background and then updates the window.
func (me *settingsWindow) refresh() {
	go func() {
		vms, err := (&broker.Client{}).ListVMs()
		rows := make([]vmRow, len(vms))
		for i, v := range vms {
			_, _, stored, _ := credential.Read(v.Name)
			rows[i] = vmRow{v.Name, stateText(v.State), stored}
		}
		enhanced := enhancedSessionAllowed()
		me.wnd.UiThread(func() { me.show(rows, err, enhanced) })
	}()
}

func (me *settingsWindow) show(rows []vmRow, err error, enhanced bool) {
	if err != nil {
		me.service.Hwnd().SetWindowText("● Unavailable: " + err.Error() + " (run hyperhand.exe install)")
		me.style.colors[me.service.Hwnd()] = colorBad
	} else {
		me.service.Hwnd().SetWindowText("● Running")
		me.style.colors[me.service.Hwnd()] = colorGood
	}
	if enhanced {
		me.enhanced.Hwnd().SetWindowText("⚠ This host allows enhanced session mode. Use a basic session in Virtual Machine Connection " +
			"(View > Enhanced Session off), or HyperHand's screenshots and input reach the lock screen.")
	} else {
		me.enhanced.Hwnd().SetWindowText("")
	}
	selected, _ := me.selected()
	me.vms = rows
	me.list.SetRedraw(false)
	me.list.DeleteAllItems()
	for _, r := range rows {
		item := me.list.AddItem(r.name, r.state, yesNo(r.password, "Stored", "Not stored"), yesNo(me.settings.onStart(r.name), "Yes", "No"))
		if r.name == selected {
			item.Select(true)
		}
	}
	me.list.SetRedraw(true)
	if len(rows) > 0 && me.list.SelectedItemCount() == 0 {
		me.list.Item(0).Select(true)
	}
	me.updateButtons()
}

// selected returns the name of the selected VM.
func (me *settingsWindow) selected() (string, bool) {
	items := me.list.SelectedItems()
	if len(items) != 1 || items[0].Index() >= len(me.vms) {
		return "", false
	}
	return me.vms[items[0].Index()].name, true
}

func (me *settingsWindow) updateButtons() {
	name, ok := me.selected()
	for _, h := range []win.HWND{me.onStart.Hwnd(), me.console.Hwnd(), me.setPw.Hwnd()} {
		h.EnableWindow(ok)
	}
	password := false
	if ok {
		password = me.vms[me.list.SelectedItems()[0].Index()].password
		me.onStart.SetCheck(me.settings.onStart(name))
	} else {
		me.onStart.SetCheck(false)
	}
	me.clearPw.Hwnd().EnableWindow(ok && password)
}

// applyPort saves a new MCP port and restarts the tray to listen on it.
func (me *settingsWindow) applyPort() {
	h := me.wnd.Hwnd()
	p, err := parsePort(me.port.Text())
	if err != nil {
		h.MessageBox(err.Error(), "HyperHand", co.MB_ICONWARNING)
		return
	}
	if p == me.info.port && me.info.listenErr == "" {
		h.MessageBox(fmt.Sprintf("HyperHand already uses port %d.", p), "HyperHand", co.MB_ICONINFORMATION)
		return
	}
	if p != me.info.port {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			h.MessageBox(fmt.Sprintf("Port %d cannot be used: %v", p, err), "HyperHand", co.MB_ICONWARNING)
			return
		}
		ln.Close()
	}
	msg := fmt.Sprintf("HyperHand will restart and serve MCP at http://127.0.0.1:%d/mcp. Requests in progress are interrupted. "+
		"Change the server URL in your MCP clients to match.\n\nRestart now?", p)
	if r, _ := h.MessageBox(msg, "HyperHand", co.MB_OKCANCEL|co.MB_ICONQUESTION); r != co.ID_OK {
		return
	}
	if err := me.settings.setPort(p); err != nil {
		h.MessageBox("Saving the port failed: "+err.Error(), "HyperHand", co.MB_ICONERROR)
		return
	}
	me.info.restart()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func yesNo(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

var (
	pOpenClipboard    = user32.NewProc("OpenClipboard")
	pCloseClipboard   = user32.NewProc("CloseClipboard")
	pEmptyClipboard   = user32.NewProc("EmptyClipboard")
	pSetClipboardData = user32.NewProc("SetClipboardData")
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	pGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	pGlobalLock       = kernel32.NewProc("GlobalLock")
	pGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	pGlobalFree       = kernel32.NewProc("GlobalFree")
)

// setClipboard puts text on the clipboard as CF_UNICODETEXT.
func setClipboard(owner win.HWND, text string) error {
	const cfUnicodeText, gmemMoveable = 13, 0x0002
	u, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	if r, _, e := pOpenClipboard.Call(uintptr(owner)); r == 0 {
		return e
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	h, _, e := pGlobalAlloc.Call(gmemMoveable, uintptr(len(u)*2))
	if h == 0 {
		return e
	}
	p, _, e := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h)
		return e
	}
	copy(unsafe.Slice(*(**uint16)(unsafe.Pointer(&p)), len(u)), u)
	pGlobalUnlock.Call(h)
	if r, _, e := pSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		pGlobalFree.Call(h) // the clipboard owns h only on success
		return e
	}
	return nil
}
