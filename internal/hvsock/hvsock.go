// Package hvsock connects the host and the guest agent over a Hyper-V socket on proto.ServiceID.
package hvsock

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"

	"hyperhand/internal/proto"
)

// Listen is used by the guest agent: it accepts connections from the host (the parent partition).
func Listen() (net.Listener, error) {
	svc, err := guid.FromString(proto.ServiceID)
	if err != nil {
		return nil, err
	}
	return winio.ListenHvsock(&winio.HvsockAddr{VMID: winio.HvsockGUIDParent(), ServiceID: svc})
}

// Dial is used by the host: it connects to the agent in the VM with the given VM ID (Msvm_ComputerSystem.Name).
func Dial(ctx context.Context, vmID string) (net.Conn, error) {
	vm, err := guid.FromString(vmID)
	if err != nil {
		return nil, err
	}
	svc, err := guid.FromString(proto.ServiceID)
	if err != nil {
		return nil, err
	}
	return winio.Dial(ctx, &winio.HvsockAddr{VMID: vm, ServiceID: svc})
}
