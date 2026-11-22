package media

// The encoder against the contract's goldens (buildytech/chat contract/runtime-http/encode, made by sharp): every size
// has the golden's width, height, quality and sharpen, a WebP within the bytes ratio and an SSIM at least ssimMin.

import (
	"bytes"
	"encoding/json"
	"image"
	"io/fs"
	"math"
	"testing"

	buildychat "github.com/buildytech/buildychat-desktop"
	"github.com/gen2brain/webp"
)

type golden struct {
	Tolerance struct {
		SSIMMin    float64    `json:"ssimMin"`
		BytesRatio [2]float64 `json:"bytesRatio"`
	} `json:"tolerance"`
	Pictures []struct {
		Picture  string `json:"picture"`
		Original struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"original"`
		Filters  Filters `json:"filters"`
		Variants []struct {
			File    string  `json:"file"`
			Width   int     `json:"width"`
			Height  int     `json:"height"`
			Bytes   int     `json:"bytes"`
			Quality int     `json:"quality"`
			Sharpen float64 `json:"sharpen"`
		} `json:"variants"`
	} `json:"pictures"`
}

type constantsTiers struct {
	Encode struct {
		Tiers []Tier `json:"tiers"`
	} `json:"encode"`
}

// SSIM is the mean SSIM of two luma planes of one size over 8x8 windows, stride 4 (constants of Wang et al.).
func SSIM(a, b []float64, w, h int) float64 {
	const c1, c2 = (0.01 * 255) * (0.01 * 255), (0.03 * 255) * (0.03 * 255)
	sum, n := 0.0, 0
	for y := 0; y+8 <= h; y += 4 {
		for x := 0; x+8 <= w; x += 4 {
			var ma, mb float64
			for j := 0; j < 8; j++ {
				for i := 0; i < 8; i++ {
					ma += a[(y+j)*w+x+i]
					mb += b[(y+j)*w+x+i]
				}
			}
			ma /= 64
			mb /= 64
			var va, vb, cov float64
			for j := 0; j < 8; j++ {
				for i := 0; i < 8; i++ {
					da, db := a[(y+j)*w+x+i]-ma, b[(y+j)*w+x+i]-mb
					va += da * da
					vb += db * db
					cov += da * db
				}
			}
			va /= 63
			vb /= 63
			cov /= 63
			sum += ((2*ma*mb + c1) * (2*cov + c2)) / ((ma*ma + mb*mb + c1) * (va + vb + c2))
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return sum / float64(n)
}

func TestEncoderMatchesTheGoldens(t *testing.T) {
	contract := buildychat.Contract()
	raw, err := fs.ReadFile(contract, "encode/index.json")
	if err != nil {
		t.Fatalf("no encode goldens embedded: %v", err)
	}
	var g golden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	rawC, _ := fs.ReadFile(contract, "constants.json")
	var c constantsTiers
	if err := json.Unmarshal(rawC, &c); err != nil || len(c.Encode.Tiers) == 0 {
		t.Fatal("no tiers in constants.json")
	}
	if len(g.Pictures) < 3 || g.Tolerance.SSIMMin < 0.9 {
		t.Fatalf("goldens incomplete: %d pictures", len(g.Pictures))
	}
	for _, p := range g.Pictures {
		data, err := fs.ReadFile(contract, "pictures/"+p.Picture)
		if err != nil {
			t.Fatal(err)
		}
		img, err := Decode(data)
		if err != nil {
			t.Fatalf("%s: %v", p.Picture, err)
		}
		if b := img.Bounds(); b.Dx() != p.Original.Width || b.Dy() != p.Original.Height {
			t.Errorf("%s: original %dx%d, golden %dx%d", p.Picture, b.Dx(), b.Dy(), p.Original.Width, p.Original.Height)
		}
		plan := Plan(img.Bounds().Dx(), c.Encode.Tiers)
		if len(plan) != len(p.Variants) {
			t.Errorf("%s: %d sizes, golden %d", p.Picture, len(plan), len(p.Variants))
			continue
		}
		for i, want := range p.Variants {
			got, err := Encode(img, plan[i], p.Filters)
			if err != nil {
				t.Fatalf("%s: %v", want.File, err)
			}
			if got.Width != want.Width || got.Height != want.Height || got.Quality != want.Quality || got.Sharpen != want.Sharpen {
				t.Errorf("%s: %dx%d q%d s%v, golden %dx%d q%d s%v", want.File, got.Width, got.Height, got.Quality, got.Sharpen, want.Width, want.Height, want.Quality, want.Sharpen)
				continue
			}
			goldenBytes, err := fs.ReadFile(contract, "encode/"+want.File)
			if err != nil {
				t.Fatal(err)
			}
			ours, err1 := webp.Decode(bytes.NewReader(got.Data))
			theirs, err2 := webp.Decode(bytes.NewReader(goldenBytes))
			if err1 != nil || err2 != nil {
				t.Fatalf("%s: decode %v %v", want.File, err1, err2)
			}
			s := similarity(ours, theirs)
			ratio := float64(len(got.Data)) / float64(want.Bytes)
			t.Logf("%s: %d bytes (golden %d, ratio %.3f), SSIM %.4f", want.File, len(got.Data), want.Bytes, ratio, s)
			if ratio < g.Tolerance.BytesRatio[0] || ratio > g.Tolerance.BytesRatio[1] {
				t.Errorf("%s: size ratio %.3f outside %v", want.File, ratio, g.Tolerance.BytesRatio)
			}
			if s < g.Tolerance.SSIMMin || math.IsNaN(s) {
				t.Errorf("%s: SSIM %.4f below %.2f", want.File, s, g.Tolerance.SSIMMin)
			}
		}
	}
}

func similarity(a, b image.Image) float64 {
	la, w, h := Luma(a)
	lb, w2, h2 := Luma(b)
	if w != w2 || h != h2 {
		return 0
	}
	return SSIM(la, lb, w, h)
}
