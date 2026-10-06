package main

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"hyperhand/internal/broker"
	"hyperhand/internal/credential"
	"hyperhand/internal/hyperv"
)

// vmMenu lists the Hyper-V VMs with their state in the tray menu and refreshes it every few seconds. Each VM has a
// submenu to open its console, choose whether vm_start opens it, and store or clear its unlock password.
type vmMenu struct {
	header, note *systray.MenuItem
	items        []*vmItem
	settings     *consoleSettings
}

type vmItem struct {
	item, console, onStart, set, clear *systray.MenuItem
	mu                                 sync.Mutex
	name                               string // the VM shown; set before its title so a click never acts on another VM than shown
}

func (it *vmItem) vm() string {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.name
}

func newVMMenu(settings *consoleSettings) *vmMenu {
	m := &vmMenu{header: systray.AddMenuItem("Virtual machines", "Hyper-V VMs and their state"), settings: settings}
	m.note = m.header.AddSubMenuItem("Loading...", "")
	m.note.Disable()
	go func() {
		for {
			vms, err := (&broker.Client{}).ListVMs()
			m.show(vms, err)
			time.Sleep(5 * time.Second)
		}
	}()
	return m
}

func (m *vmMenu) show(vms []hyperv.VM, err error) {
	switch {
	case err != nil:
		m.note.SetTitle("Unavailable: " + err.Error())
		m.note.Show()
		vms = nil
	case len(vms) == 0:
		m.note.SetTitle("None")
		m.note.Show()
	default:
		m.note.Hide()
	}
	for len(m.items) < len(vms) {
		m.items = append(m.items, newVMItem(m.header, m.settings))
	}
	for i, it := range m.items {
		if i >= len(vms) {
			it.item.Hide()
			continue
		}
		v := vms[i]
		it.mu.Lock()
		it.name = v.Name
		it.mu.Unlock()
		_, _, stored, _ := credential.Read(v.Name)
		title := fmt.Sprintf("%s: %s", v.Name, stateText(v.State))
		if stored {
			title += " (unlock password stored)"
		}
		it.item.SetTitle(title)
		if stored {
			it.clear.Enable()
		} else {
			it.clear.Disable()
		}
		if m.settings.onStart(v.Name) {
			it.onStart.Check()
		} else {
			it.onStart.Uncheck()
		}
		it.item.Show()
	}
}

func newVMItem(parent *systray.MenuItem, settings *consoleSettings) *vmItem {
	it := &vmItem{item: parent.AddSubMenuItem("", "")}
	it.console = it.item.AddSubMenuItem("Open console", "Open the VM in Virtual Machine Connection (no UAC prompt)")
	it.onStart = it.item.AddSubMenuItemCheckbox("Open console when started", "Open Virtual Machine Connection when vm_start starts this VM", false)
	it.set = it.item.AddSubMenuItem("Set unlock password...", "Store the password (or PIN) the guest lock screen asks for, for vm_start and vm_unlock")
	it.clear = it.item.AddSubMenuItem("Clear unlock password", "")
	go func() {
		for {
			select {
			case <-it.console.ClickedCh:
				if name := it.vm(); name != "" {
					if enhancedSessionAllowed() {
						msgBox(enhancedSessionWarning, windows.MB_ICONWARNING)
					}
					if err := openConsole(name); err != nil {
						msgBox("Opening the console failed:\n"+err.Error(), windows.MB_ICONERROR)
					}
				}
			case <-it.onStart.ClickedCh:
				if name := it.vm(); name != "" {
					on := !settings.onStart(name)
					if err := settings.setOnStart(name, on); err != nil {
						msgBox("Saving the setting failed:\n"+err.Error(), windows.MB_ICONERROR)
						continue
					}
					if on {
						it.onStart.Check()
						if enhancedSessionAllowed() {
							msgBox(enhancedSessionWarning, windows.MB_ICONWARNING)
						}
					} else {
						it.onStart.Uncheck()
					}
				}
			case <-it.set.ClickedCh:
				if name := it.vm(); name != "" {
					setPassword(name)
				}
			case <-it.clear.ClickedCh:
				if name := it.vm(); name != "" {
					if err := credential.Delete(name); err != nil {
						msgBox("Clearing the unlock password failed:\n"+err.Error(), windows.MB_ICONERROR)
					}
				}
			}
		}
	}()
	return it
}

func stateText(state string) string {
	switch state {
	case "Running":
		return "running"
	case "Off":
		return "off"
	case "Saved":
		return "saved"
	case "Paused":
		return "paused"
	}
	return state
}

var (
	credui                       = windows.NewLazySystemDLL("credui.dll")
	pCredUIPromptForCredentialsW = credui.NewProc("CredUIPromptForCredentialsW")
)

// creduiInfoW is CREDUI_INFOW.
type creduiInfoW struct {
	Size    uint32
	Parent  uintptr
	Message *uint16
	Caption *uint16
	Banner  uintptr
}

// setPassword asks for vm's unlock password in the Windows credential dialog and stores it.
func setPassword(vm string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	const (
		flagsDoNotPersist = 0x2
		flagsAlwaysShowUI = 0x80
		flagsGeneric      = 0x40000
		errCancelled      = 1223
	)
	user, _, _, _ := credential.Read(vm)
	userBuf := make([]uint16, 514)
	if u, err := windows.UTF16FromString(user); err == nil && len(u) <= len(userBuf) {
		copy(userBuf, u)
	}
	pwBuf := make([]uint16, 257)
	defer clear(pwBuf)
	msg, _ := windows.UTF16PtrFromString(fmt.Sprintf("Unlock password for VM %s: the password or PIN its lock screen asks for. "+
		"HyperHand types it on the VM's keyboard when vm_start or vm_unlock finds the session locked. "+
		"The user name is only a note. Stored in Windows Credential Manager for your user on this PC.", vm))
	caption, _ := windows.UTF16PtrFromString("HyperHand")
	target, _ := windows.UTF16PtrFromString(credential.Target(vm))
	info := creduiInfoW{Message: msg, Caption: caption}
	info.Size = uint32(unsafe.Sizeof(info))
	var save int32
	r, _, _ := pCredUIPromptForCredentialsW.Call(uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(target)), 0, 0,
		uintptr(unsafe.Pointer(&userBuf[0])), uintptr(len(userBuf)), uintptr(unsafe.Pointer(&pwBuf[0])), uintptr(len(pwBuf)),
		uintptr(unsafe.Pointer(&save)), flagsGeneric|flagsAlwaysShowUI|flagsDoNotPersist)
	if r == errCancelled {
		return
	}
	if r != 0 {
		msgBox("The password dialog failed: "+windows.Errno(r).Error(), windows.MB_ICONERROR)
		return
	}
	pw := windows.UTF16ToString(pwBuf)
	for _, c := range pw {
		if c > 127 {
			msgBox("The password contains non-ASCII characters, which the Hyper-V keyboard cannot type. It was not stored.", windows.MB_ICONERROR)
			return
		}
	}
	if err := credential.Write(vm, windows.UTF16ToString(userBuf), pw); err != nil {
		log.Print("store unlock password: ", err)
		msgBox("Storing the unlock password failed:\n"+err.Error(), windows.MB_ICONERROR)
	}
}
