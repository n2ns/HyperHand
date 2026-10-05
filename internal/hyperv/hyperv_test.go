package hyperv

import (
	"context"
	"errors"
	"image/color"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWaitStateJobCompletes(t *testing.T) {
	jobs := []stateJob{{State: 2}, {State: 3}, {State: 4}, {State: 6}, {State: 7}}
	reads := 0
	err := waitStateJob(context.Background(), 0, func() (stateJob, error) {
		if reads >= len(jobs) {
			t.Fatal("polled again after completion")
		}
		j := jobs[reads]
		reads++
		return j, nil
	})
	if err != nil || reads != len(jobs) {
		t.Fatalf("reads=%d error=%v", reads, err)
	}
}

func TestWaitStateJobSurfacesAsyncFailure(t *testing.T) {
	for _, state := range []int{7, 8, 9, 10} {
		reads := 0
		err := waitStateJob(context.Background(), 0, func() (stateJob, error) {
			reads++
			if reads == 1 {
				return stateJob{State: 4}, nil
			}
			return stateJob{State: state, ErrorCode: 32768, Description: "insufficient resources"}, nil
		})
		if err == nil || !strings.Contains(err.Error(), "32768") || !strings.Contains(err.Error(), "insufficient resources") || reads != 2 {
			t.Fatalf("state=%d reads=%d error=%v", state, reads, err)
		}
	}
}

func TestWaitStateJobDoesNotAcceptAbnormalTerminationWithoutErrorCode(t *testing.T) {
	for _, state := range []int{8, 9, 10, 0, 32768} {
		if err := waitStateJob(context.Background(), 0, func() (stateJob, error) { return stateJob{State: state}, nil }); err == nil {
			t.Fatalf("accepted state %d", state)
		}
	}
}

func TestWaitStateJobStopsOnTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reads := 0
	// Cancellation after the first observation deterministically exercises the
	// bounded wait without allowing another query or any new state-change request.
	err := waitStateJob(ctx, time.Hour, func() (stateJob, error) { reads++; cancel(); return stateJob{State: 4}, nil })
	if !errors.Is(err, context.Canceled) || reads != 1 || !strings.Contains(err.Error(), "may still be running") {
		t.Fatalf("reads=%d error=%v", reads, err)
	}
	deadline, done := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer done()
	err = waitStateJob(deadline, 0, func() (stateJob, error) { t.Fatal("queried after deadline"); return stateJob{}, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
}

func TestWaitStateJobQueryFailure(t *testing.T) {
	want := errors.New("job disappeared")
	err := waitStateJob(context.Background(), 0, func() (stateJob, error) { return stateJob{}, want })
	if !errors.Is(err, want) {
		t.Fatalf("query error hidden: %v", err)
	}
}

func TestWaitStateJobPendingDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	reads := 0
	err := waitStateJob(ctx, time.Hour, func() (stateJob, error) { reads++; return stateJob{State: 4}, nil })
	if !errors.Is(err, context.DeadlineExceeded) || reads > 1 {
		t.Fatalf("deadline did not interrupt polling wait: reads=%d error=%v", reads, err)
	}
}

func TestStateName(t *testing.T) {
	// Msvm_ComputerSystem.EnabledState uses Hyper-V-specific values for saved/paused VMs.
	for state, want := range map[int]string{
		2: "Running", 3: "Off", 32769: "Saved", 32768: "Paused",
		0: "0", 6: "6", 9: "9", 32770: "32770",
	} {
		if got := stateName(state); got != want {
			t.Errorf("stateName(%d) = %q, want %q", state, got, want)
		}
	}
}

func TestParseKeys(t *testing.T) {
	cases := map[string][]int{
		"enter":          {0x0D},
		"Ctrl+Shift+Esc": {0x11, 0x10, 0x1B},
		"win+r":          {0x5B, 'R'},
		"alt+F4":         {0x12, 0x73},
		"f12":            {0x7B},
		"ctrl+5":         {0x11, '5'},
		"ctrl + /":       {0x11, 0xBF},
		"ctrl+plus":      {0x11, 0xBB},
	}
	for in, want := range cases {
		got, err := parseKeys(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseKeys(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ctrl+", "f13", "foo", "ctrl+xx"} {
		if _, err := parseKeys(bad); err == nil {
			t.Errorf("parseKeys(%q) should fail", bad)
		}
	}
}

func TestRGB565ToRGBA(t *testing.T) {
	// 2x1: pure red (0xF800), pure blue (0x001F), little-endian.
	img, err := rgb565ToRGBA([]byte{0x00, 0xF8, 0x1F, 0x00}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := img.RGBAAt(0, 0); got != (color.RGBA{255, 0, 0, 255}) {
		t.Errorf("pixel 0 = %v", got)
	}
	if got := img.RGBAAt(1, 0); got != (color.RGBA{0, 0, 255, 255}) {
		t.Errorf("pixel 1 = %v", got)
	}
	// 0x07E0 = pure green, row 2 of a 1x2 image.
	img, _ = rgb565ToRGBA([]byte{0, 0, 0xE0, 0x07}, 1, 2)
	if got := img.RGBAAt(0, 1); got != (color.RGBA{0, 255, 0, 255}) {
		t.Errorf("green = %v", got)
	}
	if _, err := rgb565ToRGBA([]byte{1, 2}, 2, 1); err == nil {
		t.Error("short data should fail")
	}
}

func TestVariantBytes(t *testing.T) {
	for _, size := range []int{16, 24} {
		want := []byte{0x00, 0x7F, 0xFF}
		raw := make([]byte, len(want)*size)
		for i, b := range want {
			raw[i*size] = 0x11 // VT_UI1 tag, must not be read
			raw[i*size+8] = b
		}
		if got := variantBytes(raw, size); !reflect.DeepEqual(got, want) {
			t.Errorf("variantBytes(size %d) = %v, want %v", size, got, want)
		}
	}
}

func TestParseCheckpoints(t *testing.T) {
	one := Checkpoint{Name: "干净", CreationTime: "2026-10-04 10:00:00"}
	cases := map[string][]Checkpoint{
		"": nil,
		"\xef\xbb\xbf{\"Name\":\"干净\",\"CreationTime\":\"2026-10-04 10:00:00\"}\r\n":           {one},
		`[{"Name":"干净","CreationTime":"2026-10-04 10:00:00"},{"Name":"b","CreationTime":"x"}]`: {one, {Name: "b", CreationTime: "x"}},
	}
	for in, want := range cases {
		got, err := parseCheckpoints([]byte(in))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseCheckpoints(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}
