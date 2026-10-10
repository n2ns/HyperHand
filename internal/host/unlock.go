package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"hyperhand/internal/credential"
	"hyperhand/internal/proto"
)

// errNoPassword: the session is locked and no unlock password is stored for the VM.
var errNoPassword = errors.New("no unlock password is stored for this VM: set one in the HyperHand tray menu (the VM's submenu, Set unlock password...), or unlock it in VMConnect")

// agentStartTimeout is how long vm_start waits for the guest agent after the VM runs.
const agentStartTimeout = 90 * time.Second

// unlocker detects a locked guest session and unlocks it by typing the stored password on the Hyper-V keyboard.
type unlocker struct {
	input    sync.Locker // held by every HyperHand tool that sends keyboard or mouse input
	call     agentCall
	keys     func(vm, keys string) error
	typeText func(vm, text string) error
	password func(vm string) (string, bool, error)
	sleep    func(time.Duration)
}

func newUnlocker(call agentCall, b Backend, input sync.Locker) unlocker {
	return unlocker{
		input:    input,
		call:     call,
		keys:     b.PressKeys,
		typeText: b.TypeText,
		password: func(vm string) (string, bool, error) {
			_, p, ok, err := credential.Read(vm)
			return p, ok, err
		},
		sleep: time.Sleep,
	}
}

// state asks the agent for its session's lock state.
func (u unlocker) state(ctx context.Context, vm string) (proto.SessionStateResult, error) {
	var s proto.SessionStateResult
	if _, err := u.call(ctx, vm, proto.OpSessionState, nil, nil, &s); err != nil {
		if strings.HasPrefix(err.Error(), "unknown op") {
			return s, errors.New("the guest agent is too old to report whether the session is locked; run vm_update_agent")
		}
		return s, err
	}
	return s, nil
}

// lockedHint adds the reason to an error from an input-related tool when the guest session turns out to be locked.
func (u unlocker) lockedHint(ctx context.Context, vm string, err error) error {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if s, e := u.state(sctx, vm); e == nil && s.Locked {
		return fmt.Errorf("%w (the guest session is locked: run vm_unlock)", err)
	}
	return err
}

// waitAgent pings the agent until it answers or timeout passes.
func (u unlocker) waitAgent(ctx context.Context, vm string, timeout time.Duration) (proto.PingResult, error) {
	var err error
	for end := time.Now().Add(timeout); ; u.sleep(2 * time.Second) {
		var p proto.PingResult
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = u.call(pctx, vm, proto.OpPing, nil, nil, &p)
		cancel()
		if err == nil {
			return p, nil
		}
		if ctx.Err() != nil {
			return p, ctx.Err()
		}
		if !time.Now().Before(end) {
			return p, err
		}
	}
}

// unlock unlocks vm's session with the stored password. It types the password only while the agent reports the session
// locked and no UAC prompt open, and types it once: a wrong password is reported, not retried, so that the account is
// not locked out.
func (u unlocker) unlock(ctx context.Context, vm string) (string, error) {
	s, err := u.state(ctx, vm)
	if err != nil {
		return "", err
	}
	if !s.Locked {
		return "the session is not locked", nil
	}
	pw, ok, err := u.password(vm)
	if err != nil {
		return "", fmt.Errorf("read the stored unlock password: %w", err)
	}
	if !ok {
		return "", errNoPassword
	}
	for _, r := range pw {
		if r > 127 {
			return "", errors.New("the stored unlock password contains non-ASCII characters, which the Hyper-V keyboard cannot type")
		}
	}
	if !s.Console {
		return "", errNotConsole
	}
	// Ctrl dismisses the lock screen curtain without typing a character.
	if err := u.keys(vm, "ctrl"); err != nil {
		return "", err
	}
	u.sleep(1500 * time.Millisecond)
	if s, err = u.state(ctx, vm); err != nil {
		return "", err
	}
	if !s.Locked {
		return "the session is not locked", nil
	}
	if err := u.typePassword(ctx, vm, pw); err != nil {
		return "", err
	}
	for i := 0; i < 15; i++ {
		u.sleep(time.Second)
		if s, err = u.state(ctx, vm); err == nil && !s.Locked {
			return "unlocked", nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", errors.New("the session is still locked 15 s after the stored password was typed; it was not retried, so that the account is not locked out. Check the stored password (or PIN) and the screen with vm_screenshot")
}

// typePassword types pw and Enter into the password box. It holds the input lock, so that no other HyperHand tool sends
// input meanwhile, and checks the password box right before typing: Ctrl+A (which selects anything left in the box,
// so the password replaces it) goes first, as it types no character wherever it lands.
func (u unlocker) typePassword(ctx context.Context, vm, pw string) error {
	u.input.Lock()
	defer u.input.Unlock()
	if err := u.keys(vm, "ctrl+a"); err != nil {
		return err
	}
	// Bounded: the agent may be busy with another request, and the input lock blocks every input tool meanwhile.
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	s, err := u.state(sctx, vm)
	cancel()
	if err != nil {
		return fmt.Errorf("the password was not typed: %w", err)
	}
	if err := passwordBoxReady(s); err != nil {
		return fmt.Errorf("the password was not typed: %w", err)
	}
	if err := u.typeText(vm, pw); err != nil {
		return errors.New("typing the password failed") // the error could quote the password
	}
	return u.keys(vm, "enter")
}

var errNotConsole = errors.New("the agent's session is not the VM console session (an enhanced session, remote desktop or another user's session), so the Hyper-V keyboard would not type into it; the password was not typed")

// passwordBoxReady checks, right before the password is typed, that keystrokes go to the sign-in screen of the agent's
// own session: Windows reports it locked, it is the console session, LogonUI runs in it, input goes to the secure
// desktop (not to the user's programs) and no UAC prompt is open there.
func passwordBoxReady(s proto.SessionStateResult) error {
	switch {
	case !s.Locked:
		return errors.New("the session is not locked")
	case !s.Console:
		return errNotConsole
	case s.Consent:
		return errors.New("a UAC prompt is open in the guest")
	case !s.LogonUI:
		return errors.New("the sign-in screen (LogonUI) is not running in the session")
	case !s.SecureDesktop:
		return errors.New("keyboard input still goes to the user's desktop, not to the sign-in screen's password box")
	}
	return nil
}

// relockDelay: Windows can lock a session right after an automatic sign-in, after the agent already answered.
const relockDelay = 3 * time.Second

// ready waits for the agent of a running VM and unlocks its session if needed. The result describes a usable desktop;
// an error says why the desktop is not usable.
func (u unlocker) ready(ctx context.Context, vm string, agentTimeout time.Duration) (string, error) {
	p, err := u.waitAgent(ctx, vm, agentTimeout)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("the guest agent did not answer within %v: no user is signed in, or the agent is not installed. HyperHand can unlock a locked session only while the agent runs in it: %w", agentTimeout, err)
	}
	agent := fmt.Sprintf("agent %s running on %s as %s", p.Version, p.Hostname, p.User)
	s, err := u.state(ctx, vm)
	if err != nil {
		return "", fmt.Errorf("%s, but: %w", agent, err)
	}
	if !s.Locked {
		u.sleep(relockDelay)
		if s, err = u.state(ctx, vm); err != nil {
			return "", fmt.Errorf("%s, but: %w", agent, err)
		}
	}
	if !s.Locked {
		return agent + "; desktop unlocked", nil
	}
	if _, err := u.unlock(ctx, vm); err != nil {
		return "", fmt.Errorf("%s, but the session is locked: %w", agent, err)
	}
	return agent + "; session was locked and has been unlocked", nil
}

// lockedInput serializes the keyboard and mouse input of HyperHand tools with the unlock sequence.
type lockedInput struct {
	Backend
	mu sync.Locker
}

func (b lockedInput) Click(vm string, x, y, button, count int, modifiers []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Backend.Click(vm, x, y, button, count, modifiers)
}

func (b lockedInput) Drag(vm string, x1, y1, x2, y2 int, modifiers []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Backend.Drag(vm, x1, y1, x2, y2, modifiers)
}

func (b lockedInput) Scroll(vm string, x, y, delta int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Backend.Scroll(vm, x, y, delta)
}

func (b lockedInput) PressKeys(vm, keys string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Backend.PressKeys(vm, keys)
}

func (b lockedInput) TypeText(vm, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Backend.TypeText(vm, text)
}
