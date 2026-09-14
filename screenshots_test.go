//go:build !arm

package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestScreenshotsAreUpToDate is the drift gate: it re-renders every
// screenshot and compares it with the committed file, so a change to a
// screen that is not reflected in the README images fails the build.
//
// The comparison is on decoded pixels, not on the file bytes: the images
// only have to show what the code draws, and a change in the PNG
// encoder's compression would otherwise fail this for nothing.
func TestScreenshotsAreUpToDate(t *testing.T) {
	for _, s := range screenshots() {
		t.Run(s.name, func(t *testing.T) {
			path := filepath.Join(screenshotDir, s.name+".png")
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("%v (run `make screenshots`)", err)
			}
			defer f.Close()
			committed, err := png.Decode(f)
			if err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			got := renderScreenshot(s)
			if got.Bounds() != committed.Bounds() {
				t.Fatalf("%s is %v, rendered screen is %v (run `make screenshots`)",
					path, committed.Bounds(), got.Bounds())
			}
			if x, y, ok := firstDiff(got, committed); !ok {
				t.Fatalf("%s differs from the rendered screen at (%d,%d) (run `make screenshots`)", path, x, y)
			}
		})
	}
}

// firstDiff reports the first pixel where a and b disagree.
func firstDiff(a *image.Gray, b image.Image) (int, int, bool) {
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != bb || aa != ba {
				return x, y, false
			}
		}
	}
	return 0, 0, true
}

// The rendered screens must actually contain the screen, not an empty
// panel: a backend that silently drew nothing would otherwise sail
// through the comparison above once its blank output was committed.
func TestScreenshotsAreNotBlank(t *testing.T) {
	for _, s := range screenshots() {
		img := renderScreenshot(s)
		if n := inked(img, img.Bounds()); n < 1000 {
			t.Errorf("screenshot %s has %d inked pixels, want a drawn screen", s.name, n)
		}
	}
}
