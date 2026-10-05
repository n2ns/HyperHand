package broker

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

func TestRejectUnrelatedNamedPipeServer(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\HyperHandBrokerTest-%d-%d`, os.Getpid(), time.Now().UnixNano())
	l, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case server := <-accepted:
		defer server.Close()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := verifyServer(c); err == nil {
		t.Fatal("accepted unrelated process as the privileged broker")
	}
}

func TestServicePipeRejectsInvalidIdentity(t *testing.T) {
	if _, err := pipeSecurity("S-1-1-0)(A;;GA;;;WD)", "S-1-5-80-1"); err == nil {
		t.Fatal("accepted SDDL injection")
	}
}

func TestShutdownClosesActiveAndRejectsNewConnections(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{listener: l, conns: make(map[net.Conn]bool)}
	local, peer := net.Pipe()
	defer peer.Close()
	if !s.track(local) {
		t.Fatal("failed to track connection")
	}
	s.close()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("active connection stayed open")
	}
	late, latePeer := net.Pipe()
	defer latePeer.Close()
	if s.track(late) {
		t.Fatal("accepted a connection after shutdown")
	}
	_ = latePeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := latePeer.Read(make([]byte, 1)); err == nil {
		t.Fatal("late connection stayed open")
	}
}
