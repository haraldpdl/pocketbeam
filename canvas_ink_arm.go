// The on-device Canvas: every call forwards straight to InkView, so the
// panel sees exactly the sequence of primitives it saw before the screens
// were moved behind the interface.

package main

import (
	"image"
	"image/color"

	ink "github.com/dennwc/inkview"
)

// deviceCanvas is the panel. InkView is process-global state (one active
// face, one framebuffer), so a single canvas is the honest model. The
// draw functions take their canvas as an argument; this is what the
// InkView event loop and the background goroutines hand them.
var deviceCanvas = newInkCanvas()

// inkCanvas holds only the face cache: everything else is a direct call
// into InkView's globals.
type inkCanvas struct {
	faces faceCache
}

func newInkCanvas() *inkCanvas {
	c := &inkCanvas{}
	c.faces.open = func(px int, bold bool) (Face, bool) {
		family := ink.DefaultFont
		if bold {
			family = ink.DefaultFontBold
		}
		f := ink.OpenFont(family, px, true)
		// A failed open still yields a usable handle: ink.Font's methods
		// are nil-safe, so drawing with it is a no-op rather than a
		// crash. faceCache decides not to keep it.
		return inkFace{f: f, px: px}, f != nil
	}
	return c
}

// inkFace is an opened InkView font plus the size it was opened at,
// which InkView itself does not report back.
type inkFace struct {
	f  *ink.Font
	px int
}

func (f inkFace) Height() int { return f.px }

func (c *inkCanvas) Size() image.Point { return ink.ScreenSize() }

func (c *inkCanvas) Clear() { ink.ClearScreen() }

func (c *inkCanvas) Fill(r image.Rectangle, col color.Color) { ink.FillArea(r, col) }

func (c *inkCanvas) Rect(r image.Rectangle, col color.Color) { ink.DrawRect(r, col) }

func (c *inkCanvas) Font(px int, bold bool) Face { return c.faces.face(px, bold) }

func (c *inkCanvas) SetFont(f Face, col color.Color) { f.(inkFace).f.SetActive(col) }

func (c *inkCanvas) Text(p image.Point, s string) { ink.DrawString(p, s) }

func (c *inkCanvas) TextWidth(s string) int { return ink.StringWidth(s) }

func (c *inkCanvas) FullUpdate() { ink.FullUpdate() }

func (c *inkCanvas) PartialUpdate(r image.Rectangle) { ink.PartialUpdate(r) }

func (c *inkCanvas) ShowHourglass() { ink.ShowHourglass() }

func (c *inkCanvas) ShowHourglassAt(p image.Point) { ink.ShowHourglassAt(p) }

func (c *inkCanvas) HideHourglass() { ink.HideHourglass() }
