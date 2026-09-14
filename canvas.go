// The drawing seam between the screens and the panel. Every screen
// paints through a Canvas, so the same drawing code runs on the device
// through InkView (canvas_ink_arm.go) and into an image on a build
// machine (canvas_image.go) for screenshots and tests.
//
// The interface mirrors InkView's model rather than inventing one: a
// single active face plus colour that the following Text / TextWidth
// calls use, and a string positioned by the top-left corner of its glyph
// box. Keeping the semantics identical is what makes the device
// behaviour unchanged by the move.

package main

import (
	"image"
	"image/color"
	"sync"
)

// The four shades the UI paints with, restated here as plain colours so
// a screen does not need the InkView package to name one. They are the
// values ink.Black / ink.DarkGray / ink.LightGray / ink.White carry.
var (
	black     = color.Gray{Y: 0x00}
	darkGray  = color.Gray{Y: 0x55}
	lightGray = color.Gray{Y: 0xaa}
	white     = color.Gray{Y: 0xff}
)

// Face is a font opened by a Canvas, at one size and weight. It is
// opaque: only the canvas that opened it can draw with it.
type Face interface {
	// Height is the pixel height the face was opened at. Draw code
	// centres text vertically against it, because a string is positioned
	// by the top-left corner of its glyph box rather than its baseline.
	Height() int
}

// Canvas is everything the screens need from the panel. It is
// deliberately small: only the calls the UI actually makes are here, so
// a second backend stays cheap to write and to keep honest.
type Canvas interface {
	// Size is the panel size in pixels; computeLayout derives every
	// rectangle from it.
	Size() image.Point
	// Clear paints the whole panel white.
	Clear()
	// Fill paints r in col; Rect draws a one-pixel outline of r.
	Fill(r image.Rectangle, col color.Color)
	Rect(r image.Rectangle, col color.Color)
	// Font opens (or returns a cached) face at px pixels, bold or
	// regular. Callers scale px through layout.fpx first.
	Font(px int, bold bool) Face
	// SetFont makes f and col active for the Text and TextWidth calls
	// that follow.
	SetFont(f Face, col color.Color)
	// Text draws s in the active face with p as the top-left corner of
	// the glyph box; TextWidth measures s in the active face.
	Text(p image.Point, s string)
	TextWidth(s string) int
	// FullUpdate pushes the whole panel, PartialUpdate only r. Both are
	// no-ops off-device, where there is no panel to refresh.
	FullUpdate()
	PartialUpdate(r image.Rectangle)
	// ShowHourglass raises the busy icon in the middle of the panel,
	// ShowHourglassAt at p; HideHourglass restores what it covered.
	ShowHourglass()
	ShowHourglassAt(p image.Point)
	HideHourglass()
}

// faceKey identifies one opened face: the already-scaled pixel height
// plus the weight.
type faceKey struct {
	px   int
	bold bool
}

// faceCache holds the faces a canvas has opened. Opening reads and
// parses the face on every call, and each screen needs four to six of
// them, so opening them per draw put that cost on every repaint -
// including the ones a download or sync progress tick fires several
// times a second. A face is immutable once opened (SetFont only selects
// it and a colour for the draw calls that follow), so one handle per
// size and weight serves the whole app.
//
// The faces live for the process lifetime deliberately: InkView calls
// Close from its event loop on EVT_EXIT without joining our draw
// goroutines, so closing a shared handle there could free a face a
// progress tick is between SetFont and Text on. The relaunch path
// (syscall.Exec) already relies on the process teardown to release them.
//
// mu guards the map because the goroutines that push partial updates
// (sync progress, update download, the library-refresh spinner) draw
// outside the InkView event loop.
type faceCache struct {
	// open returns the backend's face for px and bold. ok reports
	// whether it is worth keeping: see face below.
	open func(px int, bold bool) (f Face, ok bool)

	mu sync.Mutex
	m  map[faceKey]Face
}

// face returns the shared face for px and weight. Only a face that
// opened is memoised: opening can fail transiently on the device
// (memory pressure while the sync goroutine streams a book, a briefly
// busy /ebrmain mount), and caching that result would retire the size
// for the rest of the session - the backend hands back a usable
// no-op face, so text would silently keep painting in whichever face
// was last active. Retrying costs one failed open per draw and the text
// is back on the next pass.
func (fc *faceCache) face(px int, bold bool) Face {
	k := faceKey{px: px, bold: bold}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if f := fc.m[k]; f != nil {
		return f
	}
	f, ok := fc.open(px, bold)
	if !ok {
		return f
	}
	if fc.m == nil {
		fc.m = make(map[faceKey]Face)
	}
	fc.m[k] = f
	return f
}
