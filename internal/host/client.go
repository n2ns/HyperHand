// Package host is the host side of HyperHand: the agent client and the MCP tools.
package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"hyperhand/internal/proto"
)

// Client talks to one guest agent: it keeps one connection and sends one request at a time. Before reusing the
// connection it probes it and redials if it is dead (e.g. after the agent restarted). If the request could not be sent
// it reconnects and sends it once more; a request that was sent is never resent, so a command cannot run twice.
type Client struct {
	dial func(context.Context) (net.Conn, error)
	gate chan struct{} // serializes calls and Close; waiting calls can be canceled
	conn net.Conn
}

func NewClient(dial func(context.Context) (net.Conn, error)) *Client {
	return &Client{dial: dial, gate: make(chan struct{}, 1)}
}

// Call sends op with args (JSON) and payload, decodes the result into result (if not nil) and returns the response payload.
func (c *Client) Call(ctx context.Context, op string, args any, payload []byte, result any) ([]byte, error) {
	var out bytes.Buffer
	if _, err := c.CallIO(ctx, op, args, bytes.NewReader(payload), int64(len(payload)), &out, result); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// CallIO is Call with streamed payloads: the request payload is the next srcSize bytes of src (nil when srcSize is 0)
// and the response payload is copied to dst (nil discards it). It returns the response payload size. A request whose
// send failed is resent only if src can be rewound (src nil or an io.Seeker).
func (c *Client) CallIO(ctx context.Context, op string, args any, src io.Reader, srcSize int64, dst io.Writer, result any) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case c.gate <- struct{}{}:
	}
	defer func() { <-c.gate }()
	// Cancellation and the gate may become ready together; do not touch the
	// connection for a request that was canceled while it was waiting.
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	req := proto.Request{Op: op}
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return 0, err
		}
		req.Args = b
	}
	if dst == nil {
		dst = io.Discard
	}
	var err error
	for i := range 2 {
		if i > 0 && src != nil {
			s, ok := src.(io.Seeker)
			if !ok {
				break
			}
			if _, serr := s.Seek(0, io.SeekStart); serr != nil {
				break
			}
		}
		if c.conn != nil && !alive(c.conn) { // e.g. the agent or the VM restarted since the last call
			c.conn.Close()
			c.conn = nil
		}
		if c.conn == nil {
			dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			c.conn, err = c.dial(dctx)
			cancel()
			if err != nil {
				c.conn = nil
				return 0, fmt.Errorf("connect to agent: %w", err)
			}
		}
		var resp proto.Response
		var n int64
		var sent bool
		if n, sent, err = c.roundtrip(ctx, &req, src, srcSize, dst, &resp); err == nil {
			if resp.Error != "" {
				return n, errors.New(resp.Error)
			}
			if result != nil && len(resp.Result) > 0 {
				if err := json.Unmarshal(resp.Result, result); err != nil {
					return n, err
				}
			}
			return n, nil
		}
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
		}
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		if sent {
			break
		}
	}
	return 0, fmt.Errorf("agent: %w", err)
}

// alive probes an idle connection with a 1 ms read: a timeout means it is still open. EOF or any other error means it
// is dead, and so does data (nothing is ever pending between requests).
func alive(conn net.Conn) bool {
	if conn.SetReadDeadline(time.Now().Add(time.Millisecond)) != nil {
		return false
	}
	var b [1]byte
	_, err := conn.Read(b[:])
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout() && conn.SetReadDeadline(time.Time{}) == nil
}

func (c *Client) roundtrip(ctx context.Context, req *proto.Request, src io.Reader, srcSize int64, dst io.Writer, resp *proto.Response) (n int64, sent bool, err error) {
	conn := c.conn
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	if err := proto.WriteFrameFrom(conn, req, srcSize, src); err != nil {
		stop()
		return 0, false, err
	}
	size, err := proto.ReadHeader(conn, resp)
	if err == nil {
		n, err = io.CopyN(dst, conn, size)
	}
	if !stop() && err == nil { // canceled after the answer: the deadline is set, drop the connection
		conn.Close()
		c.conn = nil
	}
	return n, true, err
}

// Close drops the connection; the next Call dials again.
func (c *Client) Close() {
	c.gate <- struct{}{}
	defer func() { <-c.gate }()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

// Manager keeps one Client per VM.
type Manager struct {
	Backend Backend
	mu      sync.Mutex
	clients map[string]*Client
}

// Client returns the agent client for a VM name ("" = the only running VM).
func (m *Manager) Client(vm string) (*Client, error) {
	b := m.backend()
	v, err := b.Find(vm)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clients == nil {
		m.clients = map[string]*Client{}
	}
	c := m.clients[v.ID]
	if c == nil {
		id := v.ID
		c = NewClient(func(ctx context.Context) (net.Conn, error) { return b.Dial(ctx, id) })
		m.clients[v.ID] = c
	}
	return c, nil
}

// Drop closes and forgets the client for a VM id, e.g. after a checkpoint restore restarted its agent.
func (m *Manager) Drop(id string) {
	m.mu.Lock()
	c := m.clients[id]
	delete(m.clients, id)
	m.mu.Unlock()
	if c != nil {
		c.Close()
	}
}
