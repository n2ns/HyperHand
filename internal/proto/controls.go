package proto

const OpListControls = "list_controls"

type ControlsArgs struct {
	Handle   uint64 `json:"handle"`
	PID      uint32 `json:"pid"`
	MaxDepth int    `json:"max_depth,omitempty"`
	MaxNodes int    `json:"max_nodes,omitempty"`
}

// ControlInfo is read-only metadata; Rect uses physical screen pixels. Index is local to this snapshot.
// ControlType is a Microsoft UIA control type ID. The root has Parent -1 and Depth 0.
type ControlInfo struct {
	Index        int    `json:"index"`
	Parent       int    `json:"parent"`
	Depth        int    `json:"depth"`
	Name         string `json:"name"`
	ControlType  int32  `json:"control_type"`
	AutomationID string `json:"automation_id"`
	ClassName    string `json:"class_name"`
	PID          uint32 `json:"pid"`
	Enabled      bool   `json:"enabled"`
	Offscreen    bool   `json:"offscreen"`
	Rect         Rect   `json:"rect"`
}

type ControlsResult struct {
	Nodes      []ControlInfo `json:"nodes"`
	Truncated  bool          `json:"truncated"`
	Truncation []string      `json:"truncation,omitempty"`
}
