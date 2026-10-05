// Package proto is the wire protocol between the host tray (hyperhand.exe) and the guest agent
// (hyperhand-agent.exe) over a Hyper-V socket. The host sends one request and waits for its response.
//
// A frame is: uint32 header length | uint64 payload length | header JSON | payload bytes.
// The payload carries file contents (streamed: see WriteFrameFrom / ReadHeader) and screenshots (PNG); it is empty
// otherwise.
package proto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
)

// ServiceID is the Hyper-V socket service GUID the agent listens on.
const ServiceID = "3ce544e1-2645-4383-b332-fedf8a18736b"

// Version is set at build time for releases: -ldflags "-X hyperhand/internal/proto.Version=0.1.0".
var Version = "dev"

const (
	OpPing         = "ping"          // -> PingResult
	OpExec         = "exec"          // ExecArgs -> ExecResult
	OpWriteFile    = "write_file"    // PathArgs + payload
	OpReadFile     = "read_file"     // PathArgs -> payload
	OpListDir      = "list_dir"      // PathArgs -> ListDirResult
	OpHashFiles    = "hash_files"    // PathsArgs -> HashesResult
	OpScreenshot   = "screenshot"    // -> payload PNG
	OpClipboardGet = "clipboard_get" // -> TextResult
	OpClipboardSet = "clipboard_set" // TextArgs
	OpFocusWindow  = "focus_window"  // TitleArgs -> FocusResult (the matched title and handle)
	OpListWindows  = "list_windows"  // -> WindowsResult
	OpWindowAt     = "window_at"     // PointArgs -> HandleResult
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

// ExecArgs: Shell is "powershell" (default) or "cmd"; TimeoutMs 0 means 60 s. Admin runs it elevated (the VM's UAC is
// set to elevate administrators without prompting).
type ExecArgs struct {
	Command   string `json:"command"`
	Shell     string `json:"shell,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
	Admin     bool   `json:"admin,omitempty"`
}

type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timed_out"`
}

type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

type ListDirResult struct {
	Entries []DirEntry `json:"entries"`
}

type PathsArgs struct {
	Paths []string `json:"paths"`
}

// HashesResult: the lowercase hex SHA-256 of each path, in order; "" when the file is missing or unreadable.
type HashesResult struct {
	Hashes []string `json:"hashes"`
}

type PathArgs struct {
	Path string `json:"path"`
}

type TextArgs struct {
	Text string `json:"text"`
}

// TitleArgs: a non-zero Handle selects that window and Title is ignored.
type TitleArgs struct {
	Title  string `json:"title"` // case-insensitive substring of the window title
	Handle uint64 `json:"handle,omitempty"`
}

// FocusResult: Text is the focused window's title (an older host reads only it).
type FocusResult struct {
	Text   string `json:"text"`
	Handle uint64 `json:"handle"`
}

// Rect is a window's visible frame in guest screen pixels (the coordinates of vm_screenshot and vm_click).
type Rect struct {
	Left   int32 `json:"left"`
	Top    int32 `json:"top"`
	Right  int32 `json:"right"`
	Bottom int32 `json:"bottom"`
}

// WindowInfo describes a visible top-level window. Owner is the owner window's handle (0 if none); Modal means the
// owner is disabled, as it is while a modal dialog runs.
type WindowInfo struct {
	Handle     uint64 `json:"handle"`
	Title      string `json:"title"`
	Class      string `json:"class"`
	PID        uint32 `json:"pid"`
	Process    string `json:"process"`
	Rect       Rect   `json:"rect"`
	Enabled    bool   `json:"enabled"`
	Foreground bool   `json:"foreground"`
	Minimized  bool   `json:"minimized"`
	Owner      uint64 `json:"owner,omitempty"`
	Modal      bool   `json:"modal"`
}

type PointArgs struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// HandleResult: the top-level window that receives a click at the point; 0 when the point is off screen or no
// window is there.
type HandleResult struct {
	Handle uint64 `json:"handle"`
}

// WindowsResult lists the windows from the top of the Z order down.
type WindowsResult struct {
	Windows []WindowInfo `json:"windows"`
}

type TextResult struct {
	Text string `json:"text"`
}

// WaitArgs.Kind: "process_exit" / "process_running" (Name, e.g. "notepad") or "file_exists" (Path). TimeoutMs 0 means 60 s.
type WaitArgs struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Path      string `json:"path,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type WaitResult struct {
	Satisfied bool `json:"satisfied"`
}

// WriteFrame writes a frame whose payload is in memory.
func WriteFrame(w io.Writer, header any, payload []byte) error {
	return WriteFrameFrom(w, header, int64(len(payload)), bytes.NewReader(payload))
}

// WriteFrameFrom writes a frame whose payload is the next size bytes of src (streamed, not buffered).
func WriteFrameFrom(w io.Writer, header any, size int64, src io.Reader) error {
	h, err := json.Marshal(header)
	if err != nil {
		return err
	}
	buf := make([]byte, 12, 12+len(h))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(h)))
	binary.BigEndian.PutUint64(buf[4:12], uint64(size))
	if _, err := w.Write(append(buf, h...)); err != nil {
		return err
	}
	if size > 0 {
		if _, err := io.CopyN(w, src, size); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads a frame with its payload in memory.
func ReadFrame(r io.Reader, header any) ([]byte, error) {
	size, err := ReadHeader(r, header)
	if err != nil {
		return nil, err
	}
	payload := make([]byte, size)
	_, err = io.ReadFull(r, payload)
	return payload, err
}

// ReadHeader reads a frame's header and returns its payload size; the caller must then consume exactly that many
// bytes from r (e.g. io.CopyN to a file) before reading the next frame.
func ReadHeader(r io.Reader, header any) (int64, error) {
	var prefix [12]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return 0, err
	}
	h := make([]byte, binary.BigEndian.Uint32(prefix[0:4]))
	if _, err := io.ReadFull(r, h); err != nil {
		return 0, err
	}
	size := int64(binary.BigEndian.Uint64(prefix[4:12]))
	if err := json.Unmarshal(h, header); err != nil {
		// Skip the payload so the stream stays in sync.
		io.CopyN(io.Discard, r, size)
		return 0, err
	}
	return size, nil
}
