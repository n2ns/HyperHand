// Package hyperv controls a Hyper-V VM from the host through WMI (root\virtualization\v2): screen, mouse, keyboard,
// state, and copying a file into the guest. Needs an elevated process. vm is the VM name; "" means the only running VM.
package hyperv

import "errors"

var errTODO = errors.New("not implemented")

type VM struct {
	Name  string `json:"name"`
	ID    string `json:"id"` // Msvm_ComputerSystem.Name, used for the Hyper-V socket
	State string `json:"state"`
}

func ListVMs() ([]VM, error) { return nil, errTODO }

// Find resolves a VM name ("" = the only running VM).
func Find(vm string) (VM, error) { return VM{}, errTODO }

// Screenshot returns the VM screen as PNG at its current resolution (pixel coordinates = mouse coordinates).
func Screenshot(vm string) (png []byte, width, height int, err error) { return nil, 0, 0, errTODO }

// Click: button 1 left, 2 right, 3 middle.
func Click(vm string, x, y, button int, double bool) error { return errTODO }

func Drag(vm string, x1, y1, x2, y2 int) error { return errTODO }

// Scroll: positive delta scrolls up.
func Scroll(vm string, x, y, delta int) error { return errTODO }

// TypeText types ASCII text.
func TypeText(vm, text string) error { return errTODO }

// PressKeys presses a key or a combination such as "enter", "esc", "f2", "ctrl+v", "win+r", "alt+f4".
func PressKeys(vm, keys string) error { return errTODO }

func Start(vm string) error { return errTODO }

func Stop(vm string) error { return errTODO }

// CopyToGuest copies a host file into the guest (enables the Guest Service Interface if needed; no guest password).
func CopyToGuest(vm, hostPath, guestPath string) error { return errTODO }
