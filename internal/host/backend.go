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
	ListCheckpoints(string) ([]hyperv.Checkpoint, error)
	CreateCheckpoint(string, string) error
	RestoreCheckpoint(string, string) error
	Screenshot(string) ([]byte, int, int, error)
	Click(string, int, int, int, bool) error
	Drag(string, int, int, int, int) error
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
