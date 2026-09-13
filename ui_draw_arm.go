// Shared drawing primitives: the list-row / hairline / toggle idiom every
// screen is built from, and the paginated list used by the pickers and
// the profile list.

package main

import (
	"fmt"
	"image"
	"sync"

	ink "github.com/dennwc/inkview"
)

// fontKey identifies one opened face: the family plus the already-scaled
// pixel height.
type fontKey struct {
	family string
	px     int
}

// fontCache holds the faces the screens draw with. ink.OpenFont reads
// and parses the face from the device's filesystem on every call, and
// each screen needs four to six of them, so opening them per draw put
// that cost on every repaint - including the ones a download or sync
// progress tick fires several times a second. A face is immutable once
// opened (SetActive only selects it and a colour for the draw calls that
// follow), so one handle per family and size serves the whole app and is
// closed on exit.
//
// mu guards the map because the goroutines that push partial updates
// (sync progress, update download, the library-refresh spinner) draw
// outside the InkView event loop.
type fontCache struct {
	mu sync.Mutex
	m  map[fontKey]*ink.Font
}

// font returns the shared face for family at base px, scaled for this
// screen. A face InkView refuses to open is cached as nil instead of
// being retried on every draw; ink.Font's methods are nil-safe, so that
// text simply does not appear, exactly as before.
func (a *app) font(family string, base int) *ink.Font {
	k := fontKey{family: family, px: a.layout.fpx(base)}
	a.fonts.mu.Lock()
	defer a.fonts.mu.Unlock()
	if f, ok := a.fonts.m[k]; ok {
		return f
	}
	f := ink.OpenFont(k.family, k.px, true)
	if a.fonts.m == nil {
		a.fonts.m = make(map[fontKey]*ink.Font)
	}
	a.fonts.m[k] = f
	return f
}

// closeFonts releases every cached face. Called from app.Close; the
// relaunch path skips it because exec replaces the process wholesale.
func (a *app) closeFonts() {
	a.fonts.mu.Lock()
	defer a.fonts.mu.Unlock()
	for k, f := range a.fonts.m {
		f.Close()
		delete(a.fonts.m, k)
	}
}

// drawCenteredText writes s centered inside rect using the given font. fontPx
// is the font's pixel height (used for vertical centering since DrawString
// interprets Y as the top-left corner).
//
// InkView measures and draws with whatever face was last made active, so f
// is activated here instead of trusting the caller's most recent
// SetActive: anything drawn in between (a list row, a body paragraph, a
// muted subtitle) would otherwise silently swap the face out and the label
// would render in the wrong size and colour while still being centred for
// fontPx. Every centred label in the app is black.
func drawCenteredText(f *ink.Font, rect image.Rectangle, s string, fontPx int) {
	f.SetActive(ink.Black)
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

// drawToggleRow paints a list row whose value is a boolean: title,
// subtitle, the pill right-aligned inside the row, and the closing
// hairline. Shared by Settings (delete missing) and the update screen
// (automatic checks) so both read and refresh identically.
func (a *app) drawToggleRow(titleF, subF *ink.Font, r image.Rectangle, title, subtitle string, on bool) {
	a.drawListRow(titleF, subF, r, title, subtitle, false)
	a.drawToggle(a.layout.togglePill(r), on)
	a.drawHairline(r.Min.X, r.Max.X, r.Max.Y)
}

// refreshToggleRow repaints one toggle row in place and pushes only that
// strip to the panel. Flipping a toggle changes nothing else on the
// screen, so routing it through Draw would clear and re-flash the whole
// panel for a pill that moved a few millimetres.
func (a *app) refreshToggleRow(r image.Rectangle, title, subtitle string, on bool) {
	ink.FillArea(r, ink.White)
	a.drawToggleRow(a.font(ink.DefaultFontBold, 36), a.font(ink.DefaultFont, 28), r, title, subtitle, on)
	ink.PartialUpdate(r)
}

// listRow is one row for drawPagedList: a bold title plus an optional
// muted subtitle.
type listRow struct {
	title    string
	subtitle string
}

// listFonts are the faces drawPagedList paints with. The caller passes
// its own handles because it has already opened the same faces for the
// screen's header and buttons, and per-row font handles are measurable
// on e-ink.
type listFonts struct {
	rowTitle *ink.Font // bold row title
	rowSub   *ink.Font // muted row subtitle
	button   *ink.Font // Prev / Next page buttons
	label    *ink.Font // muted "Page N of M" indicator
}

// pagedListRects is what a pointer handler needs once a paginated list
// has been drawn: rows[i] is list item offset+i, pageSize is the step
// the Prev/Next buttons move by, and prev/next are empty when that
// direction has nowhere to go.
type pagedListRects struct {
	offset   int
	pageSize int
	rows     []image.Rectangle
	prev     image.Rectangle
	next     image.Rectangle
}

// drawPagedList paints one page of full-width rows between top and
// bottom, followed by a "< Prev | Page N of M · K items | Next >" bar
// when the list overflows a page. Shared by the OPDS feed picker, the
// WebDAV directory picker and the profile list so all three page
// identically.
func (a *app) drawPagedList(f listFonts, top, bottom int, rows []listRow, offset int) pagedListRects {
	rowH := a.layout.rowH()
	navH := a.layout.sy(60)
	gap := a.layout.sy(20)
	p := layoutListPage(top, bottom, rowH, navH, gap, len(rows), offset)

	out := pagedListRects{
		offset:   p.offset,
		pageSize: p.pageSize,
		rows:     make([]image.Rectangle, 0, p.end-p.offset),
	}
	for i := p.offset; i < p.end; i++ {
		y1 := top + (i-p.offset)*rowH
		r := image.Rect(a.layout.margin, y1, a.layout.screen.X-a.layout.margin, y1+rowH)
		out.rows = append(out.rows, r)
		a.drawListRow(f.rowTitle, f.rowSub, r, rows[i].title, rows[i].subtitle, true)
	}
	if n := len(out.rows); n > 0 {
		last := out.rows[n-1]
		a.drawHairline(last.Min.X, last.Max.X, last.Max.Y)
	}
	if !p.navShown {
		return out
	}

	navY2 := p.navY + navH
	navW := (a.layout.screen.X - 2*a.layout.margin) / 4
	if p.offset > 0 {
		out.prev = image.Rect(a.layout.margin, p.navY, a.layout.margin+navW, navY2)
		ink.DrawRect(out.prev, ink.Black)
		drawCenteredText(f.button, out.prev, "< Prev", a.layout.fpx(44))
	}
	if p.end < len(rows) {
		out.next = image.Rect(a.layout.screen.X-a.layout.margin-navW, p.navY, a.layout.screen.X-a.layout.margin, navY2)
		ink.DrawRect(out.next, ink.Black)
		drawCenteredText(f.button, out.next, "Next >", a.layout.fpx(44))
	}
	label := fmt.Sprintf("Page %d of %d  ·  %d items",
		p.offset/p.pageSize+1, (len(rows)+p.pageSize-1)/p.pageSize, len(rows))
	// drawListRow and drawCenteredText both leave their own face active,
	// so the muted label face is activated rather than assumed.
	f.label.SetActive(ink.DarkGray)
	ink.DrawString(image.Point{
		X: (a.layout.screen.X - ink.StringWidth(label)) / 2,
		Y: p.navY + (navH-a.layout.fpx(26))/2,
	}, label)
	return out
}
