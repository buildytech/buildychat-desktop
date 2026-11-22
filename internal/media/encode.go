// Package media is the desktop host's picture work (ADR 0065, contract cases/media-*.json): the WebP sizes of the ZIP
// export (the same plan and grade as apps/xai-runtime/src/media-encode.ts, which uses sharp) and the content-addressed
// store of Media results. The encoder is libwebp (gen2brain/webp, no CGO); resize, orientation, grade and sharpen are
// done here, so the output matches sharp by tolerance (contract encode/ goldens), never by bytes.
package media

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	_ "image/gif"  // decoder
	_ "image/jpeg" // decoder
	_ "image/png"  // decoder
	"math"

	"github.com/disintegration/imaging"
	"github.com/gen2brain/webp" // encoder; registers the WebP decoder
)

// Tier is one output size: its width (capped at the original), WebP quality and sharpen sigma (0: none).
type Tier struct {
	Width   int     `json:"width"`
	Quality int     `json:"quality"`
	Sharpen float64 `json:"sharpen"`
}

// Filters is the grade: contrast around mid grey and a cooler white balance (FILTERS in media-encode.ts).
type Filters struct {
	Contrast float64 `json:"contrast"`
	Cool     float64 `json:"cool"`
}

// Variant is one encoded size.
type Variant struct {
	Width   int
	Height  int
	Quality int
	Sharpen float64
	Data    []byte
}

// ErrUnreadable means the bytes are not a picture this host decodes.
var ErrUnreadable = errors.New("unreadable picture")

// Decode reads a picture and applies its EXIF orientation (sharp .rotate()).
func Decode(data []byte) (image.Image, error) {
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, ErrUnreadable
	}
	if b := img.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, ErrUnreadable
	}
	return img, nil
}

// Plan is the sizes for an original `width` px wide: each tier capped at the original, one size per width (the
// smaller tier wins), in tier order.
func Plan(width int, tiers []Tier) []Tier {
	out := []Tier{}
	seen := map[int]bool{}
	for _, t := range tiers {
		w := min(t.Width, width)
		if seen[w] {
			continue
		}
		seen[w] = true
		t.Width = w
		out = append(out, t)
	}
	return out
}

// Encode makes one size from the original: resize (Lanczos-3, never larger), grade, optional sharpen, WebP.
func Encode(src image.Image, tier Tier, f Filters) (Variant, error) {
	b := src.Bounds()
	w := min(tier.Width, b.Dx())
	h := int(math.Round(float64(b.Dy()) * float64(w) / float64(b.Dx())))
	h = max(h, 1)
	var img *image.NRGBA
	if w == b.Dx() && h == b.Dy() {
		img = imaging.Clone(src)
	} else {
		img = imaging.Resize(src, w, h, imaging.Lanczos)
	}
	grade(img, f)
	if tier.Sharpen > 0 {
		img = sharpen(img, tier.Sharpen)
	}
	var out bytes.Buffer
	if err := webp.Encode(&out, img, webp.Options{Quality: tier.Quality, Method: 4}); err != nil {
		return Variant{}, err
	}
	return Variant{Width: w, Height: h, Quality: tier.Quality, Sharpen: tier.Sharpen, Data: out.Bytes()}, nil
}

func clamp8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(math.Round(v))
}

// grade is sharp's recomb([[1-cool,0,0],[0,1,0],[0,0,1+cool]]) then linear(a, 128(1-a)), a = 1 + contrast. Alpha stays.
func grade(img *image.NRGBA, f Filters) {
	a := 1 + f.Contrast
	off := 128 * (1 - a)
	var lut [3][256]uint8
	gains := [3]float64{1 - f.Cool, 1, 1 + f.Cool}
	for c := 0; c < 3; c++ {
		for v := 0; v < 256; v++ {
			lut[c][v] = clamp8(a*(float64(v)*gains[c]) + off)
		}
	}
	p := img.Pix
	for i := 0; i+3 < len(p); i += 4 {
		p[i], p[i+1], p[i+2] = lut[0][p[i]], lut[1][p[i+1]], lut[2][p[i+2]]
	}
}

// sharpen follows libvips sharpen as sharp calls it (m1 1 flat, m2 2 jagged, x1 2, y2 10, y3 20, on L* 0-100): the
// difference of the lightness and its Gaussian blur (sigma) is bent by m1 up to x1 and m2 beyond, capped at +y2 / -y3,
// and added to every channel (lightness only, approximated in BT.601 luma).
func sharpen(img *image.NRGBA, sigma float64) *image.NRGBA {
	const m1, m2, x1, y2, y3 = 1.0, 2.0, 2.0, 10.0, 20.0
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	luma := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*img.Stride + x*4
			luma.Pix[y*luma.Stride+x] = clamp8(0.299*float64(img.Pix[i]) + 0.587*float64(img.Pix[i+1]) + 0.114*float64(img.Pix[i+2]))
		}
	}
	blurred := imaging.Blur(luma, sigma)
	out := imaging.Clone(img)
	const toL, fromL = 100.0 / 255.0, 255.0 / 100.0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			l := float64(luma.Pix[y*luma.Stride+x])
			bl := float64(blurred.Pix[y*blurred.Stride+x*4])
			d := (l - bl) * toL
			var mod float64
			if math.Abs(d) <= x1 {
				mod = m1 * d
			} else {
				mod = math.Copysign(m1*x1+m2*(math.Abs(d)-x1), d)
			}
			mod = math.Max(-y3, math.Min(y2, mod)) * fromL
			i := y*out.Stride + x*4
			out.Pix[i] = clamp8(float64(out.Pix[i]) + mod)
			out.Pix[i+1] = clamp8(float64(out.Pix[i+1]) + mod)
			out.Pix[i+2] = clamp8(float64(out.Pix[i+2]) + mod)
		}
	}
	return out
}

// Luma is BT.601 luma of a picture as float64, row-major, for the similarity check of the tests.
func Luma(img image.Image) ([]float64, int, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			out[y*w+x] = 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
		}
	}
	return out, w, h
}
