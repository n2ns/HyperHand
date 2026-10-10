package host

import (
	"context"
	"net"

	"hyperhand/internal/broker"
	"hyperhand/internal/hyperv"
)

// Backend runs Hyper-V operations outside the unprivileged MCP process.
type Backend interface {
	ListVMs() ([]hyperv.VM, error)
	Find(string) (hyperv.VM, error)
	Start(string) error
	Stop(string) error
	Shutdown(string) error
	ListCheckpoints(string) ([]hyperv.Checkpoint, error)
	CreateCheckpoint(string, string) error
	RestoreCheckpoint(string, string) error
	DeleteCheckpoint(vm, name string) error // removes one checkpoint (not its children); implemented by vm_end_turn's work
	Screenshot(string) ([]byte, int, int, error)
	Click(vm string, x, y, button, count int, modifiers []string) error
	Drag(vm string, x1, y1, x2, y2 int, modifiers []string) error
	Scroll(string, int, int, int) error
	PressKeys(string, string) error
	TypeText(string, string) error
	CopyToGuest(string, string, string) error
	Dial(context.Context, string) (net.Conn, error)
}

func (m *Manager) backend() Backend {
	if m.Backend != nil {
		return m.Backend
	}
	return &broker.Client{}
}
