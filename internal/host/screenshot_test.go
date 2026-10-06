package host

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"
)

func screenshotFixture(t *testing.T, w, h int) ([]byte, *image.NRGBA) {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 20), G: uint8(y * 30), B: 100, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), im
}

func TestTransformScreenshotUnchanged(t *testing.T) {
	data, _ := screenshotFixture(t, 7, 5)
	for _, opts := range []screenshotOptions{
		{}, {MaxSize: 7}, {MaxSize: 20},
		{Region: &screenshotRegion{Width: 7, Height: 5}},
	} {
		got, g, err := transformScreenshot(data, opts)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, data) {
			t.Fatal("untransformed screenshot changed bytes")
		}
		want := screenshotGeometry{OriginalWidth: 7, OriginalHeight: 5, Width: 7, Height: 5, OutputWidth: 7, OutputHeight: 5, ScaleX: 1, ScaleY: 1}
		if g != want {
			t.Fatalf("geometry = %+v, want %+v", g, want)
		}
	}
}

func TestTransformScreenshotCropPixels(t *testing.T) {
	data, src := screenshotFixture(t, 7, 5)
	got, g, err := transformScreenshot(data, screenshotOptions{Region: &screenshotRegion{X: 2, Y: 1, Width: 5, Height: 4}})
	if err != nil {
		t.Fatal(err)
	}
	want := screenshotGeometry{OriginalWidth: 7, OriginalHeight: 5, X: 2, Y: 1, Width: 5, Height: 4, OutputWidth: 5, OutputHeight: 4, ScaleX: 1, ScaleY: 1}
	if g != want {
		t.Fatalf("geometry = %+v, want %+v", g, want)
	}
	im, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	if im.Bounds() != image.Rect(0, 0, 5, 4) {
		t.Fatal(im.Bounds())
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 5; x++ {
			if color.NRGBAModel.Convert(im.At(x, y)) != src.At(x+2, y+1) {
				t.Fatalf("wrong cropped pixel at %d,%d", x, y)
			}
		}
	}
}

func TestTransformScreenshotScaleGeometry(t *testing.T) {
	for _, tt := range []struct{ w, h, limit, ow, oh int }{
		{7, 5, 4, 4, 3}, {5, 7, 4, 3, 4}, {7, 1, 1, 1, 1},
		{1, 7, 1, 1, 1}, {4, 4, 2, 2, 2},
	} {
		data, _ := screenshotFixture(t, tt.w, tt.h)
		got, g, err := transformScreenshot(data, screenshotOptions{MaxSize: tt.limit})
		if err != nil {
			t.Fatal(err)
		}
		if g.OutputWidth != tt.ow || g.OutputHeight != tt.oh || g.ScaleX != float64(tt.ow)/float64(tt.w) || g.ScaleY != float64(tt.oh)/float64(tt.h) {
			t.Fatalf("%+v: geometry %+v", tt, g)
		}
		im, err := png.Decode(bytes.NewReader(got))
		if err != nil || im.Bounds() != image.Rect(0, 0, tt.ow, tt.oh) {
			t.Fatalf("output dimensions: %v, %v", im, err)
		}
	}
}

func TestTransformScreenshotCropAndScalePixels(t *testing.T) {
	data, src := screenshotFixture(t, 8, 6)
	got, g, err := transformScreenshot(data, screenshotOptions{Region: &screenshotRegion{X: 2, Y: 1, Width: 4, Height: 4}, MaxSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if g.X != 2 || g.Y != 1 || g.Width != 4 || g.Height != 4 || g.ScaleX != 0.5 || g.ScaleY != 0.5 {
		t.Fatalf("geometry %+v", g)
	}
	im, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	// Each output pixel samples the centre of its 2x2 source area, after the crop offset.
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			if color.NRGBAModel.Convert(im.At(x, y)) != src.At(3+2*x, 2+2*y) {
				t.Fatalf("wrong scaled pixel at %d,%d", x, y)
			}
		}
	}
}

func TestTransformScreenshotInvalid(t *testing.T) {
	data, _ := screenshotFixture(t, 7, 5)
	for _, r := range []screenshotRegion{
		{}, {Width: 0, Height: 1}, {Width: 1, Height: -1},
		{X: -1, Width: 1, Height: 1}, {Y: -1, Width: 1, Height: 1},
		{X: 7, Width: 1, Height: 1}, {Y: 5, Width: 1, Height: 1},
		{X: 1, Width: 7, Height: 1}, {Y: 1, Width: 1, Height: 5},
		{X: math.MaxInt, Width: math.MaxInt, Height: 1},
		{Y: 1, Width: 1, Height: math.MaxInt},
	} {
		if _, _, err := transformScreenshot(data, screenshotOptions{Region: &r}); err == nil {
			t.Fatalf("accepted invalid region %+v", r)
		}
	}
	if _, _, err := transformScreenshot(data, screenshotOptions{MaxSize: -1}); err == nil {
		t.Fatal("accepted negative max_size")
	}
	for _, invalid := range [][]byte{nil, {}, []byte("not a PNG"), data[:len(data)/2]} {
		if _, _, err := transformScreenshot(invalid, screenshotOptions{}); err == nil {
			t.Fatal("accepted invalid PNG")
		}
	}
}
