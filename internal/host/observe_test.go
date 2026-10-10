package host

import (
	"errors"
	"fmt"
	"net"
	"testing"

	"hyperhand/internal/proto"
)

func TestClampRegion(t *testing.T) {
	for _, tt := range []struct {
		name string
		rect proto.Rect
		want screenshotRegion
		ok   bool
	}{
		{"inside", proto.Rect{Left: 8, Top: 4, Right: 40, Bottom: 30}, screenshotRegion{X: 8, Y: 4, Width: 32, Height: 26}, true},
		{"whole screen", proto.Rect{Right: 64, Bottom: 40}, screenshotRegion{Width: 64, Height: 40}, true},
		{"partly off right and bottom", proto.Rect{Left: 50, Top: 20, Right: 100, Bottom: 60}, screenshotRegion{X: 50, Y: 20, Width: 14, Height: 20}, true},
		{"partly off left and top", proto.Rect{Left: -10, Top: -5, Right: 10, Bottom: 5}, screenshotRegion{Width: 10, Height: 5}, true},
		{"larger than the screen", proto.Rect{Left: -10, Top: -5, Right: 100, Bottom: 50}, screenshotRegion{Width: 64, Height: 40}, true},
		{"minimized", proto.Rect{Left: -32000, Top: -32000, Right: -31840, Bottom: -31960}, screenshotRegion{}, false},
		{"touching the right edge", proto.Rect{Left: 64, Top: 0, Right: 70, Bottom: 10}, screenshotRegion{}, false},
		{"empty", proto.Rect{Left: 5, Top: 5, Right: 5, Bottom: 9}, screenshotRegion{}, false},
	} {
		got, ok := clampRegion(tt.rect, 64, 40)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: got %+v %v, want %+v %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

// observationFor builds the observation vm_observe stores for a crop of the given screenshot scaled to maxSize.
func observationFor(t *testing.T, data []byte, crop screenshotRegion, maxSize int) *observation {
	t.Helper()
	_, g, err := transformScreenshot(data, screenshotOptions{Region: &crop, MaxSize: maxSize})
	if err != nil {
		t.Fatal(err)
	}
	return &observation{ID: "o-test", HasImage: true, Crop: screenshotRegion{X: g.X, Y: g.Y, Width: g.Width, Height: g.Height}, Scale: g.ScaleX, OutputWidth: g.OutputWidth, OutputHgt: g.OutputHeight}
}

func TestObservationToScreenRoundTrip(t *testing.T) {
	data, _ := screenshotFixture(t, 64, 40)
	for _, tt := range []struct {
		crop    screenshotRegion
		maxSize int
	}{
		{screenshotRegion{Width: 64, Height: 40}, 0},
		{screenshotRegion{X: 8, Y: 4, Width: 32, Height: 26}, 0},
		{screenshotRegion{X: 8, Y: 4, Width: 32, Height: 26}, 16},
		{screenshotRegion{X: 50, Y: 20, Width: 14, Height: 20}, 7},
		{screenshotRegion{Width: 64, Height: 40}, 5},
	} {
		o := observationFor(t, data, tt.crop, tt.maxSize)
		if o.Crop != tt.crop {
			t.Fatalf("crop %+v stored as %+v", tt.crop, o.Crop)
		}
		// Each output pixel maps to the centre of its source area: the top-left pixel lands within the first
		// 1/Scale screen pixels of the crop origin.
		if x, y, err := o.toScreen(0, 0); err != nil || x < tt.crop.X || y < tt.crop.Y || float64(x-tt.crop.X) > 1/o.Scale || float64(y-tt.crop.Y) > 1/o.Scale {
			t.Errorf("%+v/%d: top-left maps to (%d, %d) %v", tt.crop, tt.maxSize, x, y, err)
		}
		for v := 0; v < o.OutputHgt; v++ {
			for u := 0; u < o.OutputWidth; u++ {
				x, y, err := o.toScreen(u, v)
				if err != nil {
					t.Fatal(err)
				}
				if x < tt.crop.X || y < tt.crop.Y || x >= tt.crop.X+tt.crop.Width || y >= tt.crop.Y+tt.crop.Height {
					t.Fatalf("%+v/%d: image pixel (%d, %d) maps to (%d, %d) outside the crop", tt.crop, tt.maxSize, u, v, x, y)
				}
				// Mapping back is monotonic and, with Scale 1, the identity.
				if o.Scale == 1 && (x != tt.crop.X+u || y != tt.crop.Y+v) {
					t.Fatalf("unscaled pixel (%d, %d) maps to (%d, %d)", u, v, x, y)
				}
			}
		}
		if _, _, err := o.toScreen(o.OutputWidth, 0); err == nil {
			t.Error("accepted a pixel outside the image")
		}
	}
}

func TestAgentUnreachable(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("connect to agent: %w", errors.New("dial hvsock: no such VM")),
		fmt.Errorf("agent: %w", errors.New("EOF")),
		&net.OpError{Op: "dial", Err: errors.New("refused")},
	} {
		if !agentUnreachable(err) {
			t.Errorf("%v should count as unreachable", err)
		}
	}
	for _, err := range []error{errors.New(`unknown op "list_controls"`), errors.New("list_controls timed out after 10s"), errors.New("window not found")} {
		if agentUnreachable(err) {
			t.Errorf("%v is an agent answer, not an outage", err)
		}
	}
	if !uiaTimedOut(errors.New("list_controls timed out after 10s")) || !uiaTimedOut(errors.New("UIA call interrupted")) || uiaTimedOut(errors.New("window not found")) {
		t.Error("uiaTimedOut")
	}
}
