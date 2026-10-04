// Package host is the host side of HyperHand: the agent client and the MCP tools.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"hyperhand/internal/hvsock"
	"hyperhand/internal/hyperv"
	"hyperhand/internal/proto"
)

// Client talks to one guest agent: it keeps one connection and sends one request at a time. Before reusing the
// connection it probes it and redials if it is dead (e.g. after the agent restarted). If the request could not be sent
// it reconnects and sends it once more; a request that was sent is never resent, so a command cannot run twice.
type Client struct {
	dial func(context.Context) (net.Conn, error)
	mu   sync.Mutex
	conn net.Conn
}

func NewClient(dial func(context.Context) (net.Conn, error)) *Client { return &Client{dial: dial} }

// Call sends op with args (JSON) and payload, decodes the result into result (if not nil) and returns the response payload.
func (c *Client) Call(ctx context.Context, op string, args any, payload []byte, result any) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	req := proto.Request{Op: op}
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		req.Args = b
	}
	var err error
	for range 2 {
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
				return nil, fmt.Errorf("connect to agent: %w", err)
			}
		}
		var resp proto.Response
		var out []byte
		var sent bool
		if out, sent, err = c.roundtrip(ctx, &req, payload, &resp); err == nil {
			if resp.Error != "" {
				return nil, errors.New(resp.Error)
			}
			if result != nil && len(resp.Result) > 0 {
				if err := json.Unmarshal(resp.Result, result); err != nil {
					return nil, err
				}
			}
			return out, nil
		}
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if sent {
			break
		}
	}
	return nil, fmt.Errorf("agent: %w", err)
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

func (c *Client) roundtrip(ctx context.Context, req *proto.Request, payload []byte, resp *proto.Response) (out []byte, sent bool, err error) {
	conn := c.conn
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	if err := proto.WriteFrame(conn, req, payload); err != nil {
		stop()
		return nil, false, err
	}
	out, err = proto.ReadFrame(conn, resp)
	if !stop() && err == nil { // canceled after the answer: the deadline is set, drop the connection
		conn.Close()
		c.conn = nil
	}
	return out, true, err
}

// Close drops the connection; the next Call dials again.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

// Manager keeps one Client per VM.
type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client
}

// Client returns the agent client for a VM name ("" = the only running VM).
func (m *Manager) Client(vm string) (*Client, error) {
	v, err := hyperv.Find(vm)
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
		c = NewClient(func(ctx context.Context) (net.Conn, error) { return hvsock.Dial(ctx, id) })
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
