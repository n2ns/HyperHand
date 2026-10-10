package host

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"sync"
	"testing"

	"hyperhand/internal/proto"
)

func encodeFreshnessImage(t *testing.T, im image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPointImageChanged(t *testing.T) {
	old := image.NewNRGBA(image.Rect(0, 0, 200, 120))
	draw.Draw(old, old.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	before := encodeFreshnessImage(t, old)
	for _, tt := range []struct {
		name string
		area image.Rectangle
		want bool
	}{
		{"same page", image.Rectangle{}, false},
		{"new button at same coordinates", image.Rect(60, 40, 100, 60), true},
		{"distant animation", image.Rect(150, 0, 200, 120), false},
		{"blinking caret", image.Rect(80, 40, 82, 60), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cur := image.NewNRGBA(old.Bounds())
			draw.Draw(cur, cur.Bounds(), old, image.Point{}, draw.Src)
			draw.Draw(cur, tt.area, &image.Uniform{color.Black}, image.Point{}, draw.Src)
			got, err := pointImageChanged(before, encodeFreshnessImage(t, cur), screenshotRegion{Width: 200, Height: 120}, 80, 50)
			if err != nil || got != tt.want {
				t.Fatalf("changed=%v err=%v, want %v", got, err, tt.want)
			}
		})
	}
	resized := encodeFreshnessImage(t, image.NewNRGBA(image.Rect(0, 0, 201, 120)))
	if changed, err := pointImageChanged(before, resized, screenshotRegion{Width: 200, Height: 120}, 80, 50); !changed || err != nil {
		t.Fatalf("resolution change: %v %v", changed, err)
	}
}

type freshnessInput struct {
	*fakeInput
	shot []byte
}

func (b *freshnessInput) Screenshot(string) ([]byte, int, int, error) {
	return b.shot, 800, 600, nil
}

func TestCoordinateClickRefusesChangedPageAtSameWindowRect(t *testing.T) {
	td := newTestDeps(t, newAgent(10))
	id := td.put(options(), .5, nil)
	o, _ := td.obs.get(id)
	im := image.NewNRGBA(image.Rect(0, 0, 800, 600))
	draw.Draw(im, im.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	o.SourcePNG = encodeFreshnessImage(t, im)
	// Scaled image (50,50) maps to original pixel (201,151).
	draw.Draw(im, image.Rect(180, 140, 220, 165), &image.Uniform{color.Black}, image.Point{}, draw.Src)
	td.deps.raw = &freshnessInput{fakeInput: td.b, shot: encodeFreshnessImage(t, im)}
	if r, m := td.call(t, "vm_click", map[string]any{"observation_id": id, "x": 50, "y": 50, "observe_after": "none"}); !r.IsError || m["error"] != codeStaleObservation || td.b.events() != "" {
		t.Fatalf("clicked changed page using scaled old pixels: %v", m)
	}
	if td.obs.version("A").Revision != 0 {
		t.Fatal("visual preflight refusal invalidated other observations")
	}
}

func TestDragRefusesChangedDestination(t *testing.T) {
	td := newTestDeps(t, newAgent(10))
	id := td.put(options(), 1, nil)
	o, _ := td.obs.get(id)
	im := image.NewNRGBA(image.Rect(0, 0, 800, 600))
	draw.Draw(im, im.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	o.SourcePNG = encodeFreshnessImage(t, im)
	draw.Draw(im, image.Rect(380, 290, 420, 315), &image.Uniform{color.Black}, image.Point{}, draw.Src)
	td.deps.raw = &freshnessInput{fakeInput: td.b, shot: encodeFreshnessImage(t, im)}
	args := map[string]any{"observation_id": id, "from": map[string]int{"x": 50, "y": 50}, "to": map[string]int{"x": 300, "y": 250}, "observe_after": "none"}
	if r, m := td.call(t, "vm_drag", args); !r.IsError || m["error"] != codeStaleObservation || td.b.events() != "" {
		t.Fatalf("dragged to changed destination: %v", m)
	}
}

func TestObservationBusyAndMissingImageRefuse(t *testing.T) {
	for _, reason := range []string{"busy", "missing image", "whole screen revision"} {
		td := newTestDeps(t, newAgent(10))
		id := td.put(nil, 1, nil)
		switch reason {
		case "busy":
			end := td.obs.beginMutation("A", false)
			defer end()
		case "missing image":
			o, _ := td.obs.get(id)
			o.SourcePNG = nil
		case "whole screen revision":
			td.obs.revise("A")
		}
		if r, m := td.call(t, "vm_click", map[string]any{"observation_id": id, "x": 50, "y": 50, "observe_after": "none"}); !r.IsError || m["error"] != codeStaleObservation || td.b.events() != "" {
			t.Fatalf("accepted %s: %v", reason, m)
		}
	}
}

func TestObservationRevisionAfterInputAndFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		td := newTestDeps(t, newAgent(10))
		id := td.put(options(), 1, nil)
		if failed {
			td.b.fail = errors.New("input may have been applied")
		}
		td.call(t, "vm_click", map[string]any{"observation_id": id, "x": 50, "y": 50, "observe_after": "none"})
		if td.obs.version("A").Revision != 1 {
			t.Fatal("dispatched input did not advance the revision")
		}
		td.b.fail = nil
		if r, m := td.call(t, "vm_click", map[string]any{"observation_id": id, "x": 50, "y": 50, "observe_after": "none"}); !r.IsError || m["error"] != codeStaleObservation {
			t.Fatalf("accepted old coordinates after input (failed=%v): %v", failed, m)
		}
		if td.obs.version("A").Revision != 1 {
			t.Fatal("preflight refusal advanced the revision")
		}
	}
}

func TestObserveAfterCapturesNewRevision(t *testing.T) {
	td := newTestDeps(t, newAgent(10))
	id := td.put(options(), 1, nil)
	td.deps.observe = func(ctx context.Context, in observeIn) (*observeOut, []byte, error) {
		version := td.taskObs(ctx).version(in.VM)
		o := &observation{VM: in.VM, Window: options(), HasImage: true, SourcePNG: actionScreenshot(), Scale: 1, Crop: screenshotRegion{X: 100, Y: 50, Width: 500, Height: 400}, OutputWidth: 500, OutputHgt: 400, Revision: version.Revision, Epoch: version.Epoch}
		td.taskObs(ctx).put(o)
		return &observeOut{ObservationID: o.ID, VM: in.VM}, nil, nil
	}
	r, m := td.call(t, "vm_click", map[string]any{"observation_id": id, "x": 50, "y": 50, "settle_ms": 1})
	if r.IsError {
		t.Fatal(m)
	}
	after := m["after"].(map[string]any)["observation_id"]
	if r, m = td.call(t, "vm_click", map[string]any{"observation_id": after, "x": 50, "y": 50, "observe_after": "none"}); r.IsError {
		t.Fatalf("after-action observation was already stale: %v", m)
	}
}

func TestObservationRevisionPreservesRuntimeRelocation(t *testing.T) {
	for _, stable := range []bool{false, true} {
		f := controlAgent(proto.ControlActionResult{Rect: &proto.Rect{Left: 300, Top: 200, Right: 400, Bottom: 240}}, nil)
		td := newTestDeps(t, f)
		nodes := controlNodes()
		if !stable {
			nodes[1].RuntimeID = ""
		}
		id := td.put(options(), 1, nodes)
		td.obs.revise("A")
		r, m := td.call(t, "vm_click", map[string]any{"observation_id": id, "index": 1, "observe_after": "none"})
		if stable && (r.IsError || td.b.events() != "click 350,220 b1 c1 []") {
			t.Fatalf("runtime relocation failed: %v", m)
		}
		if !stable && (!r.IsError || m["error"] != codeStaleObservation || td.b.events() != "") {
			t.Fatalf("old rectangle without runtime ID accepted: %v", m)
		}
	}
}

func TestObservationEpochAndWindowIdentity(t *testing.T) {
	for _, wholeScreen := range []bool{false, true} {
		for _, change := range []string{"epoch", "pid", "class", "process"} {
			td := newTestDeps(t, controlAgent(proto.ControlActionResult{}, nil))
			id := td.put(options(), 1, controlNodes())
			if wholeScreen {
				id = td.screenObservation(options(), controlNodes())
			}
			ws := testWindows()
			switch change {
			case "epoch":
				td.obs.invalidate("A")
			case "pid":
				ws[0].PID++
			case "class":
				ws[0].Class = "another class"
			case "process":
				ws[0].Process = "other.exe"
			}
			td.f.results[proto.OpListWindows] = proto.WindowsResult{Windows: ws}
			if r, m := td.call(t, "vm_click", map[string]any{"observation_id": id, "index": 1, "observe_after": "none"}); !r.IsError || m["error"] != codeStaleObservation || td.b.events() != "" {
				t.Fatalf("accepted %s change (screen=%v): %v", change, wholeScreen, m)
			}
		}
	}
}

func TestObservationStoresShareVersionsNotEntries(t *testing.T) {
	a := newObservationStore()
	b := newObservationStoreWithRevisions(a.revisions)
	o := &observation{VM: "A"}
	a.put(o)
	if _, err := b.get(o.ID); code(err) != codeStaleObservation {
		t.Fatal("another task could retrieve the observation")
	}
	end := b.beginMutation("A", true)
	if v := a.version("A"); v.Revision != 1 || v.Epoch != 1 || v.Busy != 1 {
		t.Fatalf("shared lifecycle start: %+v", v)
	}
	end()
	end()
	if v := a.version("A"); v.Revision != 2 || v.Epoch != 2 || v.Busy != 0 {
		t.Fatalf("shared lifecycle end: %+v", v)
	}
	b.clear("A")
	if _, err := a.get(o.ID); err != nil {
		t.Fatalf("another task cleared our observation: %v", err)
	}
	a.clear("A")
	if _, err := a.get(o.ID); code(err) != codeStaleObservation {
		t.Fatal("own task cleanup retained the observation")
	}
}

func TestObserveRejectsMutationAndWindowChangesDuringCapture(t *testing.T) {
	for _, change := range []string{"revision", "window"} {
		t.Run(change, func(t *testing.T) {
			b := newObserveBackend(t)
			store := newObservationStore()
			f := &fakeCall{results: map[string]any{proto.OpListWindows: observeWindows(), proto.OpListControls: observeControls()}}
			d := &deps{raw: b, obs: store, input: &sync.Mutex{}}
			d.call = func(ctx context.Context, vm, op string, args any, payload []byte, result any) ([]byte, error) {
				data, err := f.call(ctx, vm, op, args, payload, result)
				if op == proto.OpListControls {
					if change == "revision" {
						store.revise(vm)
					} else {
						ws := observeWindows()
						ws.Windows[0].Rect.Left++
						f.results[proto.OpListWindows] = ws
					}
				}
				return data, err
			}
			if _, _, err := d.observeVM(context.Background(), observeIn{VM: "A", Handle: 10, Controls: true}); code(err) != codeStaleObservation {
				t.Fatalf("capture accepted mid-capture %s change: %v", change, err)
			}
			if len(store.byID) != 0 {
				t.Fatal("inconsistent observation was stored")
			}
		})
	}
}

func TestObserveDuringMutationReturnsProgressWithRisk(t *testing.T) {
	b := newObserveBackend(t)
	store := newObservationStore()
	f := &fakeCall{results: map[string]any{proto.OpListWindows: observeWindows()}}
	d := &deps{raw: b, obs: store, input: &sync.Mutex{}, call: f.call}
	end := store.beginMutation("A", false)
	defer end()
	out, png, err := d.observeVM(context.Background(), observeIn{VM: "A", Handle: 10})
	if err != nil {
		t.Fatalf("progress observation was refused: %v", err)
	}
	if len(png) == 0 || !strings.Contains(out.StaleRisk, "mutating operation in progress") {
		t.Fatalf("missing progress image or risk: %+v", out)
	}
	o, err := store.get(out.ObservationID)
	if err != nil {
		t.Fatalf("progress observation was not stored: %v", err)
	}
	a := &action{d: d, ctx: context.Background(), vm: "A", obs: o}
	if err := a.fresh(); code(err) != codeStaleObservation {
		t.Fatalf("progress observation authorized input during mutation: %v", err)
	}
	end()
	if o.Revision == store.version("A").Revision {
		t.Fatal("progress coordinates stayed current after the mutation completed")
	}
}
