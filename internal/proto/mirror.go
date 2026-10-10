package proto

import "hyperhand/internal/mirror"

const (
	OpMirrorScan  = "mirror_scan"  // PathArgs -> mirror.Manifest
	OpMirrorApply = "mirror_apply" // MirrorApplyArgs + concatenated changed file contents -> mirror.Result
)

// MirrorApplyArgs binds execution to the exact source and target manifests shown in the plan.
// The payload contains copy/overwrite file contents in mirror.Diff order.
type MirrorApplyArgs struct {
	Path   string          `json:"path"`
	Source mirror.Manifest `json:"source"`
	Target mirror.Manifest `json:"target"`
	Force  bool            `json:"force,omitempty"`
}
