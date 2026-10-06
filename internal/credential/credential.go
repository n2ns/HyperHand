// Package credential keeps each VM's unlock password in Windows Credential Manager (a generic credential of the current
// user, protected by DPAPI and kept on this machine). It is read only by the user's own tray/MCP process.
package credential

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	advapi32    = windows.NewLazySystemDLL("advapi32.dll")
	pCredReadW  = advapi32.NewProc("CredReadW")
	pCredWriteW = advapi32.NewProc("CredWriteW")
	pCredDelete = advapi32.NewProc("CredDeleteW")
	pCredFree   = advapi32.NewProc("CredFree")
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
)

// credentialW is CREDENTIALW.
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// Target is the Credential Manager target name for a VM.
func Target(vm string) string { return "HyperHand:" + vm }

// Read returns the stored user name and password for vm; ok is false when none is stored.
func Read(vm string) (user, password string, ok bool, err error) {
	target, err := windows.UTF16PtrFromString(Target(vm))
	if err != nil {
		return "", "", false, err
	}
	var c *credentialW
	r, _, e := pCredReadW.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&c)))
	if r == 0 {
		if errors.Is(e, windows.ERROR_NOT_FOUND) {
			return "", "", false, nil
		}
		return "", "", false, e
	}
	defer pCredFree.Call(uintptr(unsafe.Pointer(c)))
	if c.UserName != nil {
		user = windows.UTF16PtrToString(c.UserName)
	}
	if n := c.CredentialBlobSize / 2; n > 0 {
		password = windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(c.CredentialBlob)), n))
	}
	return user, password, true, nil
}

// Write stores user and password for vm, replacing any earlier ones. The password is stored as UTF-16, as Credential
// Manager does for passwords it saves itself.
func Write(vm, user, password string) error {
	target, err := windows.UTF16PtrFromString(Target(vm))
	if err != nil {
		return err
	}
	u, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return err
	}
	blob, err := windows.UTF16FromString(password)
	if err != nil {
		return err
	}
	blob = blob[:len(blob)-1] // without the terminating NUL
	c := credentialW{Type: credTypeGeneric, TargetName: target, UserName: u, Persist: credPersistLocalMachine}
	if len(blob) > 0 {
		c.CredentialBlobSize = uint32(len(blob) * 2)
		c.CredentialBlob = (*byte)(unsafe.Pointer(&blob[0]))
	}
	if r, _, e := pCredWriteW.Call(uintptr(unsafe.Pointer(&c)), 0); r == 0 {
		return e
	}
	return nil
}

// Delete removes the stored credential for vm; it is not an error when none is stored.
func Delete(vm string) error {
	target, err := windows.UTF16PtrFromString(Target(vm))
	if err != nil {
		return err
	}
	if r, _, e := pCredDelete.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0); r == 0 && !errors.Is(e, windows.ERROR_NOT_FOUND) {
		return e
	}
	return nil
}
