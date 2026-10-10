package host

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"hyperhand/internal/proto"
)

// fakeGuest answers ping and session_state; states are returned in order, the last one repeating.
type fakeGuest struct {
	states  []proto.SessionStateResult
	stateEr error
	pingErr error
	input   []string // keys and typed text, in order
	proto   int      // the protocol ping reports; 0 means proto.Protocol
}

func (g *fakeGuest) protocol() int {
	if g.proto == 0 {
		return proto.Protocol
	}
	return g.proto
}

func (g *fakeGuest) call(_ context.Context, _, op string, _ any, _ []byte, result any) ([]byte, error) {
	var r any
	switch op {
	case proto.OpPing:
		if g.pingErr != nil {
			return nil, g.pingErr
		}
		r = proto.PingResult{Version: "dev", Protocol: g.protocol(), Hostname: "PC", User: `PC\tok`}
	case proto.OpSessionState:
		if g.stateEr != nil {
			return nil, g.stateEr
		}
		r = g.states[0]
		if len(g.states) > 1 {
			g.states = g.states[1:]
		}
	default:
		return nil, errors.New("unexpected op " + op)
	}
	b, _ := json.Marshal(r)
	return nil, json.Unmarshal(b, result)
}

func (g *fakeGuest) unlocker(password string, stored bool) unlocker {
	return unlocker{
		input:    &sync.Mutex{},
		call:     g.call,
		keys:     func(_, k string) error { g.input = append(g.input, "key:"+k); return nil },
		typeText: func(_, t string) error { g.input = append(g.input, "text:"+t); return nil },
		password: func(string) (string, bool, error) { return password, stored, nil },
		sleep:    func(time.Duration) {},
	}
}

var (
	locked      = proto.SessionStateResult{Locked: true, Console: true, LogonUI: true} // lock screen curtain
	passwordBox = proto.SessionStateResult{Locked: true, Console: true, LogonUI: true, SecureDesktop: true}
	unlocked    = proto.SessionStateResult{Console: true}
)

func TestUnlockTypesStoredPasswordOnce(t *testing.T) {
	g := &fakeGuest{states: []proto.SessionStateResult{locked, passwordBox, passwordBox, unlocked}}
	r, err := g.unlocker("s3cret", true).unlock(context.Background(), "Win10")
	if err != nil || r != sessionUnlocked {
		t.Fatalf("%q %v", r, err)
	}
	want := []string{"key:ctrl", "key:ctrl+a", "text:s3cret", "key:enter"}
	if !slices.Equal(g.input, want) {
		t.Errorf("input %v, want %v", g.input, want)
	}
}

func TestUnlockNeverTypesWhenItShouldNot(t *testing.T) {
	for _, c := range []struct {
		name   string
		states []proto.SessionStateResult
		pw     string
		stored bool
		want   string // error substring; empty means success without typing
	}{
		{"not locked", []proto.SessionStateResult{unlocked}, "pw", true, ""},
		{"no password", []proto.SessionStateResult{locked}, "", false, "no unlock password"},
		{"non-ASCII", []proto.SessionStateResult{locked}, "密码", true, "non-ASCII"},
		{"UAC prompt", []proto.SessionStateResult{locked, {Locked: true, Console: true, LogonUI: true, SecureDesktop: true, Consent: true}}, "pw", true, "UAC prompt"},
		{"unlocked meanwhile", []proto.SessionStateResult{locked, unlocked}, "pw", true, ""},
		// Input still on the user's desktop: typing would reach the foreground program.
		{"user desktop", []proto.SessionStateResult{locked, locked}, "pw", true, "user's desktop"},
		{"no LogonUI", []proto.SessionStateResult{locked, {Locked: true, Console: true, SecureDesktop: true}}, "pw", true, "LogonUI"},
		{"not console", []proto.SessionStateResult{{Locked: true, LogonUI: true, SecureDesktop: true}}, "pw", true, "not the VM console"},
	} {
		g := &fakeGuest{states: c.states}
		_, err := g.unlocker(c.pw, c.stored).unlock(context.Background(), "Win10")
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.want)
		}
		for _, in := range g.input {
			if strings.HasPrefix(in, "text:") {
				t.Errorf("%s: typed %q", c.name, in)
			}
		}
	}
}

func TestUnlockWrongPasswordIsNotRetried(t *testing.T) {
	g := &fakeGuest{states: []proto.SessionStateResult{locked, passwordBox}}
	_, err := g.unlocker("wrong", true).unlock(context.Background(), "Win10")
	if err == nil || !strings.Contains(err.Error(), "not retried") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("err %v", err)
	}
	typed := 0
	for _, in := range g.input {
		if strings.HasPrefix(in, "text:") {
			typed++
		}
	}
	if typed != 1 {
		t.Errorf("typed %d times: %v", typed, g.input)
	}
}

func TestUnlockOldAgent(t *testing.T) {
	g := &fakeGuest{stateEr: errors.New(`unknown op "session_state"`)}
	if _, err := g.unlocker("pw", true).unlock(context.Background(), "Win10"); err == nil || !strings.Contains(err.Error(), "vm_update_agent") {
		t.Errorf("err %v", err)
	}
	if len(g.input) != 0 {
		t.Errorf("input %v", g.input)
	}
}

func TestReady(t *testing.T) {
	ctx := context.Background()
	g := &fakeGuest{states: []proto.SessionStateResult{unlocked}}
	if r, err := g.unlocker("", false).ready(ctx, "Win10", time.Second); err != nil || r.Unlocked || r.Agent.Version != "dev" || r.Agent.Protocol != proto.Protocol {
		t.Errorf("unlocked: %+v %v", r, err)
	}
	g = &fakeGuest{states: []proto.SessionStateResult{locked}}
	if _, err := g.unlocker("", false).ready(ctx, "Win10", time.Second); err == nil || !strings.Contains(err.Error(), "locked") || !strings.Contains(err.Error(), "no unlock password") {
		t.Errorf("locked without password: %v", err)
	}
	g = &fakeGuest{states: []proto.SessionStateResult{locked, locked, passwordBox, passwordBox, unlocked}}
	if r, err := g.unlocker("pw", true).ready(ctx, "Win10", time.Second); err != nil || !r.Unlocked {
		t.Errorf("locked with password: %+v %v", r, err)
	}
	// Locked right after the agent first answered (automatic sign-in, then lock).
	g = &fakeGuest{states: []proto.SessionStateResult{unlocked, locked}}
	if _, err := g.unlocker("", false).ready(ctx, "Win10", time.Second); err == nil || !strings.Contains(err.Error(), "no unlock password") {
		t.Errorf("relocked: %v", err)
	}
	g = &fakeGuest{pingErr: errors.New("connection refused")}
	if _, err := g.unlocker("pw", true).ready(ctx, "Win10", 0); err == nil || !strings.Contains(err.Error(), "did not answer") || asToolError(err).Code != codeAgentRequired {
		t.Errorf("no agent: %v", err)
	}
	// An agent with an older protocol is refused before the session is touched.
	g = &fakeGuest{states: []proto.SessionStateResult{locked}, proto: proto.Protocol - 1}
	if _, err := g.unlocker("pw", true).ready(ctx, "Win10", time.Second); err == nil || asToolError(err).Code != codeAgentOutdated || asToolError(err).Next != "call vm_update_agent" {
		t.Errorf("outdated: %v", err)
	}
	if len(g.input) != 0 {
		t.Errorf("outdated agent got input %v", g.input)
	}
}

func TestLockedHint(t *testing.T) {
	base := errors.New("could not bring it to the foreground")
	g := &fakeGuest{states: []proto.SessionStateResult{locked}}
	if err := g.unlocker("", false).lockedHint(context.Background(), "", base); !errors.Is(err, base) || !strings.Contains(err.Error(), "vm_unlock") {
		t.Errorf("locked: %v", err)
	}
	g = &fakeGuest{states: []proto.SessionStateResult{unlocked}}
	if err := g.unlocker("", false).lockedHint(context.Background(), "", base); err != base {
		t.Errorf("unlocked: %v", err)
	}
}
