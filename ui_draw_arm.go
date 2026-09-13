// Shared drawing primitives: the list-row / hairline / toggle idiom every
// screen is built from, and the paginated list used by the pickers and
// the profile list.

package main

import (
	"image"

	ink "github.com/dennwc/inkview"
)

// drawCenteredText writes s centered inside rect using the given font. fontPx
// is the font's pixel height (used for vertical centering since DrawString
// interprets Y as the top-left corner).
func drawCenteredText(f *ink.Font, rect image.Rectangle, s string, fontPx int) {
	w := ink.StringWidth(s)
	x := rect.Min.X + (rect.Dx()-w)/2
	y := rect.Min.Y + (rect.Dy()-fontPx)/2
	ink.DrawString(image.Point{X: x, Y: y}, s)
}

// drawSectionLabel paints a small, muted all-caps header above a
// group of rows.
func (a *app) drawSectionLabel(f *ink.Font, label string, y int) {
	f.SetActive(ink.DarkGray)
	ink.DrawString(image.Point{X: a.layout.margin, Y: y}, label)
}

// drawListRow paints one full-width tappable row with a bold title on
// top, an optional muted subtitle underneath, a hairline separator along
// the top edge, and an optional right-edge chevron for drill-downs.
// Used by Settings, Shelf/Dir pickers, Profile list, and the first-run
// backend picker so every list in the app shares one visual language.
//
// DrawString interprets Y as the top-left corner of the glyph box (see
// drawCenteredText), so every position here is expressed as the text's
// top edge, not its baseline.
func (a *app) drawListRow(titleF, subF *ink.Font, r image.Rectangle, title, subtitle string, chevron bool) {
	a.drawHairline(r.Min.X, r.Max.X, r.Min.Y)

	titleH := a.layout.fpx(36)
	subH := a.layout.fpx(28)
	gap := a.layout.sy(12)

	titleF.SetActive(ink.Black)
	if subtitle == "" {
		titleY := r.Min.Y + (r.Dy()-titleH)/2
		ink.DrawString(image.Point{X: r.Min.X, Y: titleY}, truncate(title, 48))
	} else {
		totalH := titleH + gap + subH
		titleY := r.Min.Y + (r.Dy()-totalH)/2
		subY := titleY + titleH + gap
		ink.DrawString(image.Point{X: r.Min.X, Y: titleY}, truncate(title, 40))
		subF.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: r.Min.X, Y: subY}, truncate(subtitle, 48))
	}

	if chevron {
		// Reuse titleF (same bold face) rather than opening a fresh
		// font per row; picker screens can call this 10+ times per
		// frame and per-row font handles are measurable on e-ink.
		titleF.SetActive(ink.DarkGray)
		ch := ">"
		chW := ink.StringWidth(ch)
		ink.DrawString(
			image.Point{X: r.Max.X - a.layout.sx(20) - chW, Y: r.Min.Y + (r.Dy()-titleH)/2},
			ch)
	}
}

// drawHairline paints a 1-pixel light-gray horizontal rule. The shared
// separator element for list rows and section dividers across every
// screen.
func (a *app) drawHairline(x1, x2, y int) {
	ink.FillArea(image.Rect(x1, y, x2, y+1), ink.LightGray)
}

// drawToggle paints a two-position pill indicating a boolean
// setting. On: filled dark with the thumb on the right. Off: filled light
// with the thumb on the left. Sharp-edged because rounded rects are not
// part of the ink package and fake rounding reads worse on e-ink than a
// clean rectangle.
func (a *app) drawToggle(r image.Rectangle, on bool) {
	ink.DrawRect(r, ink.Black)
	inset := r.Inset(a.layout.sx(4))
	thumbW := inset.Dy()
	if on {
		ink.FillArea(inset, ink.DarkGray)
		thumb := image.Rect(inset.Max.X-thumbW, inset.Min.Y, inset.Max.X, inset.Max.Y)
		ink.FillArea(thumb, ink.Black)
		ink.DrawRect(thumb, ink.White)
	} else {
		ink.FillArea(inset, ink.LightGray)
		thumb := image.Rect(inset.Min.X, inset.Min.Y, inset.Min.X+thumbW, inset.Max.Y)
		ink.FillArea(thumb, ink.White)
		ink.DrawRect(thumb, ink.Black)
	}
}
