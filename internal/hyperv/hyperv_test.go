package hyperv

import (
	"image/color"
	"reflect"
	"testing"
)

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
