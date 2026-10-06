package main

import (
	"fmt"
	"log"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"

	"hyperhand/internal/credential"
)

// stateText is a VM state as the settings window shows it.
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
