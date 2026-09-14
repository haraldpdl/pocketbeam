// Shared drawing primitives: the list-row / hairline / toggle idiom every
// screen is built from, and the paginated list used by the pickers and
// the profile list. They hang off layout because geometry is all they
// need beyond the canvas they paint on.

package main

import (
	"fmt"
	"image"
)

// font returns the shared face for a base size authored against the
// reference device, scaled for this screen.
func (l layout) font(c Canvas, base int, bold bool) Face {
	return c.Font(l.fpx(base), bold)
}

// drawCenteredText writes s centered inside rect in the given face.
//
// The canvas measures and draws with whatever face was last made active,
// so f is activated here instead of trusting the caller's most recent
// SetFont: anything drawn in between (a list row, a body paragraph, a
// muted subtitle) would otherwise silently swap the face out and the
// label would render in the wrong size and colour while still being
// centred for f's height. Every centred label in the app is black.
func drawCenteredText(c Canvas, f Face, rect image.Rectangle, s string) {
	c.SetFont(f, black)
	w := c.TextWidth(s)
	x := rect.Min.X + (rect.Dx()-w)/2
	y := rect.Min.Y + (rect.Dy()-f.Height())/2
	c.Text(image.Point{X: x, Y: y}, s)
}

// drawSectionLabel paints a small, muted all-caps header above a
// group of rows.
func (l layout) drawSectionLabel(c Canvas, f Face, label string, y int) {
	c.SetFont(f, darkGray)
	c.Text(image.Point{X: l.margin, Y: y}, label)
}

// drawListRow paints one full-width tappable row with a bold title on
// top, an optional muted subtitle underneath, a hairline separator along
// the top edge, and an optional right-edge chevron for drill-downs.
// Used by Settings, Shelf/Dir pickers, Profile list, and the first-run
// backend picker so every list in the app shares one visual language.
//
// A string is positioned by the top-left corner of its glyph box (see
// drawCenteredText), so every position here is expressed as the text's
// top edge, not its baseline.
func (l layout) drawListRow(c Canvas, titleF, subF Face, r image.Rectangle, title, subtitle string, chevron bool) {
	l.drawHairline(c, r.Min.X, r.Max.X, r.Min.Y)

	titleH := titleF.Height()
	subH := subF.Height()
	gap := l.sy(12)

	c.SetFont(titleF, black)
	if subtitle == "" {
		titleY := r.Min.Y + (r.Dy()-titleH)/2
		c.Text(image.Point{X: r.Min.X, Y: titleY}, truncate(title, 48))
	} else {
		totalH := titleH + gap + subH
		titleY := r.Min.Y + (r.Dy()-totalH)/2
		subY := titleY + titleH + gap
		c.Text(image.Point{X: r.Min.X, Y: titleY}, truncate(title, 40))
		c.SetFont(subF, darkGray)
		c.Text(image.Point{X: r.Min.X, Y: subY}, truncate(subtitle, 48))
	}

	if chevron {
		// Reuse titleF (same bold face) rather than opening a fresh
		// font per row; picker screens can call this 10+ times per
		// frame and per-row font handles are measurable on e-ink.
		c.SetFont(titleF, darkGray)
		ch := ">"
		chW := c.TextWidth(ch)
		c.Text(
			image.Point{X: r.Max.X - l.sx(20) - chW, Y: r.Min.Y + (r.Dy()-titleH)/2},
			ch)
	}
}

// drawHairline paints a 1-pixel light-gray horizontal rule. The shared
// separator element for list rows and section dividers across every
// screen.
func (l layout) drawHairline(c Canvas, x1, x2, y int) {
	c.Fill(image.Rect(x1, y, x2, y+1), lightGray)
}

// drawToggle paints a two-position pill indicating a boolean
// setting. On: filled dark with the thumb on the right. Off: filled light
// with the thumb on the left. Sharp-edged because rounded rects are not
// part of the canvas and fake rounding reads worse on e-ink than a
// clean rectangle.
func (l layout) drawToggle(c Canvas, r image.Rectangle, on bool) {
	c.Rect(r, black)
	inset := r.Inset(l.sx(4))
	thumbW := inset.Dy()
	if on {
		c.Fill(inset, darkGray)
		thumb := image.Rect(inset.Max.X-thumbW, inset.Min.Y, inset.Max.X, inset.Max.Y)
		c.Fill(thumb, black)
		c.Rect(thumb, white)
	} else {
		c.Fill(inset, lightGray)
		thumb := image.Rect(inset.Min.X, inset.Min.Y, inset.Min.X+thumbW, inset.Max.Y)
		c.Fill(thumb, white)
		c.Rect(thumb, black)
	}
}

// drawToggleRow paints a list row whose value is a boolean: title,
// subtitle, the pill right-aligned inside the row, and the closing
// hairline. Shared by Settings (delete missing) and the update screen
// (automatic checks) so both read and refresh identically.
func (l layout) drawToggleRow(c Canvas, titleF, subF Face, r image.Rectangle, title, subtitle string, on bool) {
	l.drawListRow(c, titleF, subF, r, title, subtitle, false)
	l.drawToggle(c, l.togglePill(r), on)
	l.drawHairline(c, r.Min.X, r.Max.X, r.Max.Y)
}

// listRow is one row for drawPagedList: a bold title plus an optional
// muted subtitle.
type listRow struct {
	title    string
	subtitle string
}

// listFonts are the faces drawPagedList paints with. The caller passes
// its own handles because it has already opened the same faces for the
// screen's header and buttons.
type listFonts struct {
	rowTitle Face // bold row title
	rowSub   Face // muted row subtitle
	button   Face // Prev / Next page buttons
	label    Face // muted "Page N of M" indicator
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
func (l layout) drawPagedList(c Canvas, f listFonts, top, bottom int, rows []listRow, offset int) pagedListRects {
	rowH := l.rowH()
	navH := l.sy(60)
	gap := l.sy(20)
	p := layoutListPage(top, bottom, rowH, navH, gap, len(rows), offset)

	out := pagedListRects{
		offset:   p.offset,
		pageSize: p.pageSize,
		rows:     make([]image.Rectangle, 0, p.end-p.offset),
	}
	for i := p.offset; i < p.end; i++ {
		y1 := top + (i-p.offset)*rowH
		r := image.Rect(l.margin, y1, l.screen.X-l.margin, y1+rowH)
		out.rows = append(out.rows, r)
		l.drawListRow(c, f.rowTitle, f.rowSub, r, rows[i].title, rows[i].subtitle, true)
	}
	if n := len(out.rows); n > 0 {
		last := out.rows[n-1]
		l.drawHairline(c, last.Min.X, last.Max.X, last.Max.Y)
	}
	if !p.navShown {
		return out
	}

	navY2 := p.navY + navH
	navW := (l.screen.X - 2*l.margin) / 4
	if p.offset > 0 {
		out.prev = image.Rect(l.margin, p.navY, l.margin+navW, navY2)
		c.Rect(out.prev, black)
		drawCenteredText(c, f.button, out.prev, "< Prev")
	}
	if p.end < len(rows) {
		out.next = image.Rect(l.screen.X-l.margin-navW, p.navY, l.screen.X-l.margin, navY2)
		c.Rect(out.next, black)
		drawCenteredText(c, f.button, out.next, "Next >")
	}
	label := fmt.Sprintf("Page %d of %d  ·  %d items",
		p.offset/p.pageSize+1, (len(rows)+p.pageSize-1)/p.pageSize, len(rows))
	// drawListRow and drawCenteredText both leave their own face active,
	// so the muted label face is activated rather than assumed.
	c.SetFont(f.label, darkGray)
	c.Text(image.Point{
		X: (l.screen.X - c.TextWidth(label)) / 2,
		Y: p.navY + (navH-f.label.Height())/2,
	}, label)
	return out
}
