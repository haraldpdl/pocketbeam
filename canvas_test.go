//go:build !arm

package main

import (
	"image"
	"strings"
	"testing"
)

// recordCanvas is an imageCanvas that also keeps the strings a screen
// drew, so a test can assert on what a screen says without reading
// pixels back. It renders for real underneath, which is what makes the
// measured text widths (and so the centred labels) honest.
type recordCanvas struct {
	*imageCanvas
	texts []string
	boxes []textBox
}

// textBox is a drawn string plus the rectangle it claims: the position
// it was drawn at, the measured width, and the height of the face that
// was active. Screens place their lines by hard-coded offsets, so this
// is what a test needs to catch two of them landing on top of each
// other.
type textBox struct {
	s string
	r image.Rectangle
}

func newRecordCanvas() *recordCanvas {
	return &recordCanvas{imageCanvas: newImageCanvas(image.Point{X: 1264, Y: 1680})}
}

func (c *recordCanvas) Text(p image.Point, s string) {
	c.texts = append(c.texts, s)
	c.boxes = append(c.boxes, textBox{
		s: s,
		r: image.Rect(p.X, p.Y, p.X+c.imageCanvas.TextWidth(s), p.Y+c.imageCanvas.active.Height()),
	})
	c.imageCanvas.Text(p, s)
}

// overlappingText returns the first pair of drawn strings whose glyph
// boxes intersect, if any.
func (c *recordCanvas) overlappingText() (textBox, textBox, bool) {
	for i, a := range c.boxes {
		for _, b := range c.boxes[i+1:] {
			if a.r.Overlaps(b.r) {
				return a, b, true
			}
		}
	}
	return textBox{}, textBox{}, false
}

// drew reports whether any drawn string contains want.
func (c *recordCanvas) drew(want string) bool {
	for _, s := range c.texts {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func TestImageCanvasClearsToWhite(t *testing.T) {
	c := newImageCanvas(image.Point{X: 20, Y: 10})
	if got := c.img.GrayAt(5, 5); got.Y != 0xff {
		t.Fatalf("cleared canvas pixel = %#x, want 0xff", got.Y)
	}
	c.Fill(image.Rect(2, 2, 4, 4), black)
	if got := c.img.GrayAt(3, 3); got.Y != 0x00 {
		t.Errorf("filled pixel = %#x, want 0x00", got.Y)
	}
	if got := c.img.GrayAt(5, 5); got.Y != 0xff {
		t.Errorf("pixel outside the fill = %#x, want 0xff", got.Y)
	}
}

// Rect must stay a one-pixel outline: the toggle and the buttons rely on
// the inside being untouched.
func TestImageCanvasRectIsAnOutline(t *testing.T) {
	c := newImageCanvas(image.Point{X: 20, Y: 20})
	c.Rect(image.Rect(4, 4, 10, 10), black)
	for _, tc := range []struct {
		x, y int
		want uint8
	}{
		{4, 4, 0x00},  // top-left corner
		{9, 9, 0x00},  // bottom-right corner
		{7, 4, 0x00},  // top edge
		{7, 7, 0xff},  // inside stays clear
		{10, 7, 0xff}, // just outside the right edge
	} {
		if got := c.img.GrayAt(tc.x, tc.y).Y; got != tc.want {
			t.Errorf("pixel (%d,%d) = %#x, want %#x", tc.x, tc.y, got, tc.want)
		}
	}
}

// Fills outside the image must be clipped rather than panic: layouts for
// a smaller panel can push a rect past the edge.
func TestImageCanvasFillClipsToBounds(t *testing.T) {
	c := newImageCanvas(image.Point{X: 8, Y: 8})
	c.Fill(image.Rect(-10, -10, 100, 100), black)
	if got := c.img.GrayAt(0, 0).Y; got != 0x00 {
		t.Errorf("pixel = %#x, want 0x00", got)
	}
}

func TestImageCanvasTextMeasuresAndDraws(t *testing.T) {
	c := newImageCanvas(image.Point{X: 400, Y: 100})
	f := c.Font(40, true)
	if f.Height() != 40 {
		t.Fatalf("face height = %d, want 40", f.Height())
	}
	c.SetFont(f, black)
	w := c.TextWidth("Sync Now")
	if w <= 0 {
		t.Fatalf("text width = %d, want > 0", w)
	}
	if narrow := c.TextWidth("i"); narrow >= w {
		t.Errorf("width of %q = %d, not narrower than %q (%d)", "i", narrow, "Sync Now", w)
	}
	c.Text(image.Point{X: 10, Y: 10}, "Sync Now")
	if inked(c.img, image.Rect(10, 10, 10+w, 10+f.Height())) == 0 {
		t.Error("no pixels inked where the string was drawn")
	}
}

// A face that fails to open must not be cached, so the next draw retries
// it instead of the screen losing that size for the rest of the session.
func TestFaceCacheKeepsOnlyOpenedFaces(t *testing.T) {
	var opens int
	ok := false
	fc := faceCache{open: func(px int, bold bool) (Face, bool) {
		opens++
		return imageFace{px: px}, ok
	}}
	fc.face(32, false)
	fc.face(32, false)
	if opens != 2 {
		t.Fatalf("failed opens = %d, want 2 (not cached)", opens)
	}
	ok = true
	fc.face(32, false)
	fc.face(32, false)
	if opens != 3 {
		t.Errorf("opens after a success = %d, want 3 (cached)", opens)
	}
	if got := fc.face(32, true).Height(); got != 32 {
		t.Errorf("bold face height = %d, want 32", got)
	}
	if opens != 4 {
		t.Errorf("opens = %d, want 4: weight is part of the key", opens)
	}
}

// inked counts the non-white pixels in r, the cheapest way to ask
// whether a draw call put anything on the canvas.
func inked(img *image.Gray, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.GrayAt(x, y).Y != 0xff {
				n++
			}
		}
	}
	return n
}

// The recorder has to be a drop-in Canvas for the screens under test.
var _ Canvas = (*recordCanvas)(nil)
