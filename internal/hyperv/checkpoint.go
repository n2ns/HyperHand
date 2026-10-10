package hyperv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ole "github.com/go-ole/go-ole"
)

// Checkpoint is one VM checkpoint (Hyper-V snapshot). ID is the snapshot GUID (the second part of the
// Msvm_VirtualSystemSettingData InstanceID "Microsoft:<vm id>\<snapshot id>", also Get-VMSnapshot's Id); ParentID is
// the parent checkpoint's ID, "" for a root. CreatedAt is RFC 3339 with the host's offset. Kind is "standard" (the
// checkpoint may hold memory) or "production" (application-consistent, restores to Off; Hyper-V's Recovery type counts
// as production). State is the power state the checkpoint saved: "running", "off" or "saved".
type Checkpoint struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ParentID  string `json:"parent_id"`
	CreatedAt string `json:"created_at"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
}

// CheckpointList is a VM's checkpoint tree. CheckpointType is the VM's setting that decides what CreateCheckpoint
// makes: "Disabled", "Production", "ProductionOnly" or "Standard". CurrentParentID is the checkpoint the VM's current
// state branches from ("" when none). Checkpoints are in creation order, so parents come before children.
type CheckpointList struct {
	CheckpointType  string       `json:"checkpoint_type"`
	CurrentParentID string       `json:"current_parent_id"`
	Checkpoints     []Checkpoint `json:"checkpoints"`
}

// ErrCheckpointNotFound is returned (wrapped) when no checkpoint has the given ID.
var ErrCheckpointNotFound = errors.New("checkpoint not found")

// checkpointScript is the PowerShell that lists a VM's checkpoint tree as one JSON object.
const checkpointScript = `$v=Get-VM -Name $vm.Name
$c=@(Get-VMSnapshot -VM $vm | Sort-Object CreationTime | Select-Object @{n='id';e={[string]$_.Id}},@{n='name';e={$_.Name}},@{n='parent_id';e={[string]$_.ParentSnapshotId}},@{n='created_at';e={$_.CreationTime.ToString('yyyy-MM-ddTHH:mm:sszzz')}},@{n='kind';e={if ([string]$_.SnapshotType -eq 'Standard') {'standard'} else {'production'}}},@{n='state';e={([string]$_.State).ToLower()}})
ConvertTo-Json -Compress -Depth 3 -InputObject @{checkpoint_type=[string]$v.CheckpointType; current_parent_id=[string]$v.ParentCheckpointId; checkpoints=$c}`

// ListCheckpoints returns the VM's checkpoint tree.
func ListCheckpoints(vm string) (CheckpointList, error) {
	out, err := vmScript(vm, checkpointScript)
	if err != nil {
		return CheckpointList{}, err
	}
	return parseCheckpoints(out)
}

// parseCheckpoints reads checkpointScript's JSON. ConvertTo-Json writes a one-element array as a bare object and an
// empty array as null, which both decode to the right slice.
func parseCheckpoints(out []byte) (CheckpointList, error) {
	out = bytes.TrimSpace(bytes.TrimPrefix(out, []byte("\xef\xbb\xbf")))
	var raw struct {
		CheckpointType  string          `json:"checkpoint_type"`
		CurrentParentID string          `json:"current_parent_id"`
		Checkpoints     json.RawMessage `json:"checkpoints"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return CheckpointList{}, fmt.Errorf("checkpoint list: %w", err)
	}
	l := CheckpointList{CheckpointType: raw.CheckpointType, CurrentParentID: raw.CurrentParentID, Checkpoints: []Checkpoint{}}
	c := bytes.TrimSpace(raw.Checkpoints)
	switch {
	case len(c) == 0 || bytes.Equal(c, []byte("null")):
	case c[0] == '{':
		l.Checkpoints = make([]Checkpoint, 1)
		if err := json.Unmarshal(c, &l.Checkpoints[0]); err != nil {
			return CheckpointList{}, fmt.Errorf("checkpoint list: %w", err)
		}
	default:
		if err := json.Unmarshal(c, &l.Checkpoints); err != nil {
			return CheckpointList{}, fmt.Errorf("checkpoint list: %w", err)
		}
	}
	return l, nil
}

// CreateCheckpoint creates a checkpoint named name (the VM's CheckpointType decides its kind) and returns it.
func CreateCheckpoint(vm, name string) (Checkpoint, error) {
	out, err := vmScript(vm, `$s=Checkpoint-VM -VM $vm -SnapshotName `+psq(name)+` -Passthru
ConvertTo-Json -Compress -InputObject @{id=[string]$s.Id; name=$s.Name; parent_id=[string]$s.ParentSnapshotId; created_at=$s.CreationTime.ToString('yyyy-MM-ddTHH:mm:sszzz'); kind=$(if ([string]$s.SnapshotType -eq 'Standard') {'standard'} else {'production'}); state=([string]$s.State).ToLower()}`)
	if err != nil {
		return Checkpoint{}, err
	}
	var c Checkpoint
	if err := json.Unmarshal(bytes.TrimSpace(bytes.TrimPrefix(out, []byte("\xef\xbb\xbf"))), &c); err != nil {
		return Checkpoint{}, fmt.Errorf("created checkpoint: %w", err)
	}
	return c, nil
}

// RestoreCheckpoint applies the checkpoint with ID id. Hyper-V determines the restored power state; a running standard
// checkpoint resumes directly.
func RestoreCheckpoint(vm, id string) error {
	_, err := vmScript(vm, `$c=Get-VMSnapshot -VM $vm | Where-Object { [string]$_.Id -eq `+psq(id)+` } | Select-Object -First 1
if (-not $c) { throw ('checkpoint not found: ' + `+psq(id)+`) }
$c | Restore-VMSnapshot -Confirm:$false`)
	return err
}

// snapshotSettings finds the Msvm_VirtualSystemSettingData of the checkpoint with ID id. A snapshot's settings are
// associated with the VM through several association classes, so the same object can appear more than once.
func snapshotSettings(s *session, o *ole.IDispatch, id string) (*ole.IDispatch, error) {
	settings, err := s.assoc(o, "Msvm_VirtualSystemSettingData")
	if err != nil {
		return nil, err
	}
	suffix := "\\" + strings.ToUpper(id)
	for _, sd := range settings {
		if strings.HasPrefix(fmt.Sprint(s.get(sd, "VirtualSystemType")), "Microsoft:Hyper-V:Snapshot:") &&
			strings.HasSuffix(strings.ToUpper(fmt.Sprint(s.get(sd, "InstanceID"))), suffix) {
			return sd, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrCheckpointNotFound, id)
}

// DeleteCheckpoint removes the checkpoint with ID id through Msvm_VirtualSystemSnapshotService: DestroySnapshot
// (its children are re-parented and its disk differences merged) or, with subtree, DestroySnapshotTree (it and all
// its descendants). It waits for the merge job (up to 15 minutes).
// https://learn.microsoft.com/en-us/windows/win32/hyperv_v2/destroysnapshot-msvm-virtualsystemsnapshotservice
// https://learn.microsoft.com/en-us/windows/win32/hyperv_v2/destroysnapshottree-msvm-virtualsystemsnapshotservice
func DeleteCheckpoint(vm, id string, subtree bool) error {
	return withWMI(func(s *session) error {
		o, err := s.find(vm)
		if err != nil {
			return err
		}
		target, err := snapshotSettings(s, o, id)
		if err != nil {
			return err
		}
		svc, err := s.one("SELECT * FROM Msvm_VirtualSystemSnapshotService")
		if err != nil {
			return err
		}
		method := "DestroySnapshot"
		if subtree {
			method = "DestroySnapshotTree"
		}
		out, err := s.call(svc, method, "SnapshotSettingData", s.path(target))
		if err != nil {
			return err
		}
		return s.awaitJob(out, method, 15*time.Minute)
	})
}

// RenameCheckpoint sets the checkpoint's name through Msvm_VirtualSystemManagementService.ModifySystemSettings.
// https://learn.microsoft.com/en-us/windows/win32/hyperv_v2/modifysystemsettings-msvm-virtualsystemmanagementservice
func RenameCheckpoint(vm, id, name string) error {
	return errors.New("RenameCheckpoint is not implemented yet")
}
