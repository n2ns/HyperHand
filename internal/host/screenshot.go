package host

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
)

type screenshotRegion struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type screenshotOptions struct {
	Region  *screenshotRegion
	MaxSize int
}

// screenshotGeometry uses original screenshot pixels for the crop and output/crop ratios for scale.
type screenshotGeometry struct {
	OriginalWidth  int     `json:"original_width"`
	OriginalHeight int     `json:"original_height"`
	X              int     `json:"x"`
	Y              int     `json:"y"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	OutputWidth    int     `json:"output_width"`
	OutputHeight   int     `json:"output_height"`
	ScaleX         float64 `json:"scale_x"`
	ScaleY         float64 `json:"scale_y"`
}

func transformScreenshot(data []byte, opts screenshotOptions) ([]byte, screenshotGeometry, error) {
	var g screenshotGeometry
	if opts.MaxSize < 0 {
		return nil, g, fmt.Errorf("max_size must not be negative")
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, g, fmt.Errorf("decode screenshot: %w", err)
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	r := screenshotRegion{Width: w, Height: h}
	if opts.Region != nil {
		r = *opts.Region
	}
	// Subtraction after checking the origin avoids overflowing x+width or y+height.
	if r.X < 0 || r.Y < 0 || r.Width <= 0 || r.Height <= 0 || r.X > w || r.Y > h || r.Width > w-r.X || r.Height > h-r.Y {
		return nil, g, fmt.Errorf("region must have positive dimensions and fit within the %dx%d screenshot", w, h)
	}
	ow, oh := r.Width, r.Height
	if opts.MaxSize > 0 && max(ow, oh) > opts.MaxSize {
		if ow >= oh {
			oh = max(1, int(math.Round(float64(oh)*float64(opts.MaxSize)/float64(ow))))
			ow = opts.MaxSize
		} else {
			ow = max(1, int(math.Round(float64(ow)*float64(opts.MaxSize)/float64(oh))))
			oh = opts.MaxSize
		}
	}
	g = screenshotGeometry{
		OriginalWidth: w, OriginalHeight: h,
		X: r.X, Y: r.Y, Width: r.Width, Height: r.Height,
		OutputWidth: ow, OutputHeight: oh,
		ScaleX: float64(ow) / float64(r.Width), ScaleY: float64(oh) / float64(r.Height),
	}
	if r.X == 0 && r.Y == 0 && r.Width == w && r.Height == h && ow == w && oh == h {
		return data, g, nil
	}
	dst := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	if ow == r.Width && oh == r.Height {
		draw.Draw(dst, dst.Bounds(), src, image.Pt(r.X, r.Y), draw.Src)
	} else {
		// Nearest-neighbour sampling at pixel centres preserves exact source colours without another dependency.
		for y := 0; y < oh; y++ {
			sy := r.Y + min(r.Height-1, int((float64(y)+0.5)*float64(r.Height)/float64(oh)))
			for x := 0; x < ow; x++ {
				sx := r.X + min(r.Width-1, int((float64(x)+0.5)*float64(r.Width)/float64(ow)))
				dst.Set(x, y, src.At(sx, sy))
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, g, fmt.Errorf("encode screenshot: %w", err)
	}
	return out.Bytes(), g, nil
}
