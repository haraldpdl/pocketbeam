// Drawing for the two picker screens: the OPDS feed picker and the
// WebDAV directory picker. Both are the same screen - a header with the
// path the user is at, a fixed up-row, a paginated list of what is one
// level down, and the action buttons that end the drill-down - so they
// share one view struct and one draw. What each of them puts in that
// view lives in ui_feedpicker.go and ui_dirpicker.go; the fetches and
// the taps live in the matching _arm files.

package main

import (
	"image"
)

// pickerView is a picker screen resolved to plain values, so it renders
// the same on the device and into an image on a build machine.
type pickerView struct {
	title string // screen heading
	crumb string // the path line under it, already fitted to the header
	// upLabel is the row that leaves the current level; "" at the top
	// level, where there is nothing above.
	upLabel string
	loading bool
	// errLead is the first line of the error block, errMsg the server's
	// own message. errMsg empty means the level loaded.
	errLead string
	errMsg  string
	rows    []listRow
	offset  int
	// primary is the top action button and done the one under it. An
	// empty label hides that button; with done empty the screen shows a
	// single full-height primary instead of the stacked pair.
	primary string
	done    string
}

// pickerGeom is where a picker screen's tappable elements sit. Both the
// draw and the pointer handler get it from pickerGeometry, so taps land
// on what was painted without the draw writing rects back into picker
// state.
type pickerGeom struct {
	up      image.Rectangle // leaves the current level; empty at the top
	list    pagedListRects
	primary image.Rectangle
	done    image.Rectangle
	// listTop is the top of the band the list and the status messages
	// share: below the up-row when there is one.
	listTop int
}

// pickerGeometry places everything below the header for the view v.
func (l layout) pickerGeometry(v pickerView) pickerGeom {
	g := pickerGeom{listTop: l.pickerAreaTop}
	if v.upLabel != "" {
		g.up = l.pickerUpRow
		g.listTop += l.rowH()
	}
	g.list = l.layoutPagedList(g.listTop, l.pickerAreaBottom, len(v.rows), v.offset)
	switch {
	case v.done != "":
		g.done = l.pickerDoneRow
		if v.primary != "" {
			g.primary = l.pickerAddRow
		}
	case v.primary != "":
		g.primary = l.pickerSelectRow
	}
	return g
}

// drawPicker paints one picker screen. Like the wizard it reports where
// the busy icon belongs while a level is loading, because raising it is
// the panel's job and has to be tracked by the caller that takes it
// down again.
func drawPicker(c Canvas, l layout, v pickerView) (busyAt image.Point, busy bool) {
	titleFont := l.font(c, 64, true)
	body := l.font(c, 32, false)
	rowTitleFont := l.font(c, 36, true)
	rowSubFont := l.font(c, 28, false)
	smallFont := l.font(c, 26, false)
	btnFont := l.font(c, 44, true)

	g := l.pickerGeometry(v)

	c.SetFont(titleFont, black)
	c.Text(image.Point{X: l.margin, Y: l.sy(140)}, v.title)
	c.SetFont(smallFont, darkGray)
	c.Text(image.Point{X: l.margin, Y: l.sy(210)}, v.crumb)
	l.drawHairline(c, l.margin, l.screen.X-l.margin, l.sy(240))

	// The up-row is drawn on every pass, including while loading and
	// after a failed fetch, because it is the only way back to the
	// parent level: the on-screen Back button and the hardware Back key
	// both leave the picker and throw the whole drill-down away. It also
	// lets the user walk away from a fetch that is still in flight.
	//
	// Styled as a full-width list row with a left-aligned label, so it
	// shares the app-wide row idiom while its "<" tells it apart from
	// the chevron rows below.
	if !g.up.Empty() {
		l.drawHairline(c, g.up.Min.X, g.up.Max.X, g.up.Min.Y)
		c.SetFont(rowTitleFont, black)
		c.Text(image.Point{
			X: g.up.Min.X + l.sx(40),
			Y: g.up.Min.Y + (g.up.Dy()-rowTitleFont.Height())/2,
		}, v.upLabel)
	}

	// Status text shares the band with the list, so it starts below the
	// up-row rather than at a fixed y. The header and the up-row both
	// leave their own face active, so body is activated here.
	msgY := g.listTop + l.sy(20)
	switch {
	case v.loading:
		c.SetFont(body, black)
		c.Text(image.Point{X: l.margin, Y: msgY}, "Loading...")
		busyAt, busy = image.Point{X: l.margin, Y: msgY + l.sy(60)}, true
	case v.errMsg != "":
		c.SetFont(body, black)
		c.Text(image.Point{X: l.margin, Y: msgY}, v.errLead)
		c.Text(image.Point{X: l.margin, Y: msgY + l.sy(50)}, truncate(v.errMsg, 60))
	default:
		l.paintPagedList(c,
			listFonts{rowTitle: rowTitleFont, rowSub: rowSubFont, button: btnFont, label: smallFont},
			g.list, v.rows)
	}

	// The bottom-most button is the screen's primary action and carries
	// the double border; Back is the plain escape every screen has.
	button := func(r image.Rectangle, label string, primary bool) {
		if r.Empty() {
			return
		}
		c.Rect(r, black)
		if primary {
			c.Rect(r.Inset(2), black)
		}
		drawCenteredText(c, btnFont, r, truncate(label, 40))
	}
	button(g.primary, v.primary, v.done == "")
	button(g.done, v.done, true)
	button(l.backButton, "Back", false)

	return busyAt, busy
}
