package agent

import (
	"net"
	"testing"

	"hyperhand/internal/proto"
)

func TestPingInstallID(t *testing.T) {
	previous := InstallID
	InstallID = "0123456789abcdef0123456789abcdef"
	defer func() { InstallID = previous }()
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() { defer close(done); Serve(server) }()
	var p proto.PingResult
	call(t, client, proto.OpPing, nil, nil, &p)
	client.Close()
	<-done
	if p.InstallID != InstallID {
		t.Fatalf("ping lost installation identity: %+v", p)
	}
}
