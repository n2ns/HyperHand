package host

import (
	"bytes"
	"image"
	"image/png"
	"sync"

	"hyperhand/internal/proto"
)

type observationVersion struct {
	Revision uint64
	Epoch    uint64
	Busy     uint64
}

// beginMutation spans external operations (exec, launch, restore) that do not
// hold the input lock. A screenshot may describe their progress, but must not
// authorize an action while such an operation is still changing the guest.
func (s *observationStore) beginMutation(vm string, lifecycle bool) func() {
	advance := func(start bool) {
		s.revisions.mu.Lock()
		defer s.revisions.mu.Unlock()
		v := s.revisions.byVM[vm]
		v.Revision++
		if lifecycle {
			v.Epoch++
		}
		if start {
			v.Busy++
		} else {
			v.Busy--
		}
		s.revisions.byVM[vm] = v
	}
	advance(true)
	var once sync.Once
	return func() { once.Do(func() { advance(false) }) }
}

// Revisions are shared by every task's observation store. A task switch cannot
// make a screenshot taken before another task's input current again.
type observationRevisions struct {
	mu   sync.Mutex
	byVM map[string]observationVersion
}

func (s *observationStore) version(vm string) observationVersion {
	s.revisions.mu.Lock()
	defer s.revisions.mu.Unlock()
	return s.revisions.byVM[vm]
}

// revise invalidates coordinates, while stable UIA runtime IDs can be relocated.
func (s *observationStore) revise(vm string) {
	s.revisions.mu.Lock()
	defer s.revisions.mu.Unlock()
	v := s.revisions.byVM[vm]
	v.Revision++
	s.revisions.byVM[vm] = v
}

// invalidate also discards control identity across power/restore/agent changes.
func (s *observationStore) invalidate(vm string) {
	s.revisions.mu.Lock()
	defer s.revisions.mu.Unlock()
	v := s.revisions.byVM[vm]
	v.Revision++
	v.Epoch++
	s.revisions.byVM[vm] = v
}

func sameWindowIdentity(a, b proto.WindowInfo) bool {
	return a.Handle == b.Handle && a.PID == b.PID && a.Process == b.Process && a.Class == b.Class
}

func (a *action) freshCoordinates(o *observation, x, y int) error {
	const next = "call vm_observe again and use its observation_id"
	if o.Revision != a.d.taskObs(a.ctx).version(a.vm).Revision {
		return refuse(codeStaleObservation, next, nil, "VM %q changed after observation %s; its coordinates are no longer current", a.vm, o.ID)
	}
	if len(o.SourcePNG) == 0 {
		return refuse(codeStaleObservation, "call vm_observe with screenshot: true, or use a control with a runtime ID", nil, "observation %s has no source image to verify this coordinate target", o.ID)
	}
	data, _, _, err := a.d.raw.Screenshot(a.vm)
	if err != nil {
		return refuse(codeStaleObservation, next, nil, "cannot verify the image of observation %s: %v", o.ID, err)
	}
	changed, err := pointImageChanged(o.SourcePNG, data, o.Crop, x, y)
	if err != nil {
		return refuse(codeStaleObservation, next, nil, "cannot verify the image of observation %s: %v", o.ID, err)
	}
	if changed {
		return refuse(codeStaleObservation, next, map[string]any{"x": x, "y": y}, "screen content near (%d, %d) changed since observation %s", x, y, o.ID)
	}
	// Window movement during the screenshot must not be hidden by the cached list.
	if err := a.relist(); err != nil {
		return agentRequired(err)
	}
	if o.Revision != a.d.taskObs(a.ctx).version(a.vm).Revision {
		return refuse(codeStaleObservation, next, nil, "VM %q changed while verifying observation %s", a.vm, o.ID)
	}
	if err := a.checkSession(true); err != nil {
		return err
	}
	return a.fresh()
}

// pointImageChanged checks a 64x64 neighbourhood in original screen pixels.
// It deliberately ignores distant animation and small caret/cursor changes.
// This is a best-effort visual check, not proof that the UI's meaning is unchanged.
func pointImageChanged(before, after []byte, crop screenshotRegion, x, y int) (bool, error) {
	old, err := png.Decode(bytes.NewReader(before))
	if err != nil {
		return false, err
	}
	cur, err := png.Decode(bytes.NewReader(after))
	if err != nil {
		return false, err
	}
	if old.Bounds() != cur.Bounds() || !image.Pt(x, y).In(cur.Bounds()) {
		return true, nil
	}
	area := image.Rect(x-32, y-32, x+32, y+32).Intersect(old.Bounds()).Intersect(image.Rect(crop.X, crop.Y, crop.X+crop.Width, crop.Y+crop.Height))
	if area.Empty() {
		return true, nil
	}
	changed := 0
	for py := area.Min.Y; py < area.Max.Y; py++ {
		for px := area.Min.X; px < area.Max.X; px++ {
			r1, g1, b1, _ := old.At(px, py).RGBA()
			r2, g2, b2, _ := cur.At(px, py).RGBA()
			// Ignore minor colour variation; 24 is measured on an 8-bit channel.
			if channelChanged(r1, r2) || channelChanged(g1, g2) || channelChanged(b1, b2) {
				changed++
			}
		}
	}
	return changed*20 > area.Dx()*area.Dy(), nil
}

func channelChanged(a, b uint32) bool {
	if a > b {
		return a-b > 24*257
	}
	return b-a > 24*257
}
