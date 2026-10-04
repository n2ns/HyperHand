// Package proto is the wire protocol between the host tray (hyperhand.exe) and the guest agent
// (hyperhand-agent.exe) over a Hyper-V socket. The host sends one request and waits for its response.
//
// A frame is: uint32 header length | uint64 payload length | header JSON | payload bytes.
// The payload carries file contents and screenshots (PNG); it is empty otherwise.
package proto

import (
	"encoding/binary"
	"encoding/json"
	"io"
)

// ServiceID is the Hyper-V socket service GUID the agent listens on.
const ServiceID = "3ce544e1-2645-4383-b332-fedf8a18736b"

const Version = "0.1.0"

const (
	OpPing         = "ping"          // -> PingResult
	OpExec         = "exec"          // ExecArgs -> ExecResult
	OpWriteFile    = "write_file"    // PathArgs + payload
	OpReadFile     = "read_file"     // PathArgs -> payload
	OpScreenshot   = "screenshot"    // -> payload PNG
	OpClipboardGet = "clipboard_get" // -> TextResult
	OpClipboardSet = "clipboard_set" // TextArgs
	OpFocusWindow  = "focus_window"  // TitleArgs -> TextResult (the matched title)
	OpWait         = "wait"          // WaitArgs -> WaitResult
	OpUpdateAgent  = "update_agent"  // payload = new exe; the agent answers, replaces itself and restarts
)

type Request struct {
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
}

type Response struct {
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type PingResult struct {
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
	User     string `json:"user"`
}

// ExecArgs: Shell is "powershell" (default) or "cmd"; TimeoutMs 0 means 60 s.
type ExecArgs struct {
	Command   string `json:"command"`
	Shell     string `json:"shell,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timed_out"`
}

type PathArgs struct {
	Path string `json:"path"`
}

type TextArgs struct {
	Text string `json:"text"`
}

type TitleArgs struct {
	Title string `json:"title"` // case-insensitive substring of the window title
}

type TextResult struct {
	Text string `json:"text"`
}

// WaitArgs.Kind: "process_exit" / "process_running" (Name, e.g. "acad") or "file_exists" (Path). TimeoutMs 0 means 60 s.
type WaitArgs struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Path      string `json:"path,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type WaitResult struct {
	Satisfied bool `json:"satisfied"`
}

func WriteFrame(w io.Writer, header any, payload []byte) error {
	h, err := json.Marshal(header)
	if err != nil {
		return err
	}
	buf := make([]byte, 12, 12+len(h))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(h)))
	binary.BigEndian.PutUint64(buf[4:12], uint64(len(payload)))
	if _, err := w.Write(append(buf, h...)); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err = w.Write(payload)
	}
	return err
}

func ReadFrame(r io.Reader, header any) ([]byte, error) {
	var prefix [12]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, err
	}
	h := make([]byte, binary.BigEndian.Uint32(prefix[0:4]))
	if _, err := io.ReadFull(r, h); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(h, header); err != nil {
		return nil, err
	}
	payload := make([]byte, binary.BigEndian.Uint64(prefix[4:12]))
	_, err := io.ReadFull(r, payload)
	return payload, err
}
