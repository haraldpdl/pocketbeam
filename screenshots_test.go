//go:build !arm

package main

import (
	"bytes"
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
			committed := decodePNG(t, path)
			got := renderScreenshot(s)
			if got.Bounds() != committed.Bounds() {
				t.Fatalf("%s is %v, rendered screen is %v (run `make screenshots`)",
					path, committed.Bounds(), got.Bounds())
			}
			if d, differs := firstDiff(got, committed); differs {
				t.Fatalf("%s differs from the rendered screen at (%d,%d): committed %v, rendered %v (run `make screenshots`)",
					path, d.x, d.y, d.b, d.a)
			}
		})
	}
}

// TestScreenshotDirHasNoStrays fails when docs/screenshots holds a PNG no
// screen renders any more. Those are drift too: a renamed or dropped
// screen leaves an image behind that nothing regenerates, and the README
// keeps showing it.
func TestScreenshotDirHasNoStrays(t *testing.T) {
	rendered := map[string]bool{}
	for _, s := range screenshots() {
		rendered[s.name+".png"] = true
	}
	entries, err := os.ReadDir(screenshotDir)
	if err != nil {
		t.Fatalf("read %s: %v", screenshotDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".png" {
			continue
		}
		if !rendered[e.Name()] {
			t.Errorf("%s is not rendered by any screen, delete it",
				filepath.Join(screenshotDir, e.Name()))
		}
	}
}

// TestREADMEShowsEveryScreenshot is the other half of the stray check: a
// rendered screen nobody embeds is a picture the install guide could have
// used and an image that is regenerated for nothing, so a new screen has
// to reach the README in the same change that adds it.
func TestREADMEShowsEveryScreenshot(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	for _, s := range screenshots() {
		ref := screenshotDir + "/" + s.name + ".png"
		if !bytes.Contains(readme, []byte(ref)) {
			t.Errorf("README.md does not show %s", ref)
		}
	}
}

// TestScreenshotsAreDeterministic renders each screen twice in the same
// process and requires the two to be identical. The drift gate above can
// only work on a renderer that draws the same pixels every time, so map
// iteration order or randomness reaching a screen has to fail here rather
// than as an unexplainable red build on an unrelated pull request.
func TestScreenshotsAreDeterministic(t *testing.T) {
	for _, s := range screenshots() {
		t.Run(s.name, func(t *testing.T) {
			first := renderScreenshot(s)
			second := renderScreenshot(s)
			if d, differs := firstDiff(first, second); differs {
				t.Fatalf("two renders of %s differ at (%d,%d): %v then %v", s.name, d.x, d.y, d.a, d.b)
			}
		})
	}
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

// pixelDiff is where two images disagree and what each has there.
type pixelDiff struct {
	x, y int
	a, b pixel
}

// pixel is one point as the comparison sees it, so a failure can print
// the two values instead of only their position.
type pixel struct{ r, g, b, a uint32 }

// firstDiff reports the first pixel where a and b disagree, scanning in
// reading order so the message points at the topmost change.
func firstDiff(a *image.Gray, b image.Image) (pixelDiff, bool) {
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			av, bv := at(a, x, y), at(b, x, y)
			if av != bv {
				return pixelDiff{x: x, y: y, a: av, b: bv}, true
			}
		}
	}
	return pixelDiff{}, false
}

// at reads one pixel in the comparison's own representation.
func at(img image.Image, x, y int) pixel {
	r, g, b, a := img.At(x, y).RGBA()
	return pixel{r: r, g: g, b: b, a: a}
}

// decodePNG loads a committed screenshot.
func decodePNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (run `make screenshots`)", err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return img
}
