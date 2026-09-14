//go:build !arm

package main

import (
	"fmt"
	"image"
	"strings"
	"testing"
)

// drawLayout is the reference panel every case here draws against.
func drawLayout() layout { return computeLayout(screenshotScreen) }

func rowFonts(c Canvas, l layout) (title, sub Face) {
	return l.font(c, 36, true), l.font(c, 28, false)
}

func TestDrawListRow(t *testing.T) {
	l := drawLayout()
	c := newRecordCanvas()
	titleF, subF := rowFonts(c, l)
	r := image.Rect(l.margin, 400, l.screen.X-l.margin, 400+l.rowH())
	l.drawListRow(c, titleF, subF, r, "Server", "https://books.example.com", true)

	for _, want := range []string{"Server", "https://books.example.com", ">"} {
		if !c.drew(want) {
			t.Errorf("row did not draw %q; drew %q", want, c.texts)
		}
	}
	// The separator along the top edge is what stacks rows into a list.
	if got := c.img.GrayAt(r.Min.X+10, r.Min.Y).Y; got != lightGray.Y {
		t.Errorf("hairline pixel = %#x, want %#x", got, lightGray.Y)
	}
}

// A row is a fixed-width tap target, so long values are cut rather than
// allowed to run into the chevron.
func TestDrawListRowTruncates(t *testing.T) {
	l := drawLayout()
	c := newRecordCanvas()
	titleF, subF := rowFonts(c, l)
	long := strings.Repeat("x", 80)
	l.drawListRow(c, titleF, subF, image.Rect(l.margin, 0, l.screen.X-l.margin, l.rowH()), long, long, false)
	for _, s := range c.texts {
		if len(s) > 48 {
			t.Errorf("drew %d characters, want the row's cap; %q", len(s), s)
		}
		if !strings.HasSuffix(s, "...") {
			t.Errorf("%q was not marked as truncated", s)
		}
	}
}

// The pill is the whole state of a boolean row: the thumb sits on the
// side that matches the setting.
func TestDrawToggleRowThumbFollowsTheSetting(t *testing.T) {
	l := drawLayout()
	row := l.deleteRow
	// Inside the pill's border: the thumb is what moves, and it is the
	// only solid black in there when the setting is on.
	pill := l.togglePill(row).Inset(l.sx(4))
	left := image.Rect(pill.Min.X, pill.Min.Y, (pill.Min.X+pill.Max.X)/2, pill.Max.Y)
	right := image.Rect((pill.Min.X+pill.Max.X)/2, pill.Min.Y, pill.Max.X, pill.Max.Y)

	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			c := newImageCanvas(screenshotScreen)
			titleF, subF := rowFonts(c, l)
			l.drawToggleRow(c, titleF, subF, row, "Delete missing", "Remove books deleted on server", on)
			darkLeft, darkRight := blackPixels(c.img, left), blackPixels(c.img, right)
			if on && darkRight <= darkLeft {
				t.Errorf("thumb not on the right when on: left=%d right=%d", darkLeft, darkRight)
			}
			if !on && darkLeft <= darkRight {
				t.Errorf("thumb not on the left when off: left=%d right=%d", darkLeft, darkRight)
			}
		})
	}
}

func TestDrawPagedList(t *testing.T) {
	l := drawLayout()
	rows := make([]listRow, 20)
	for i := range rows {
		rows[i] = listRow{title: fmt.Sprintf("Shelf %d", i), subtitle: "subsection"}
	}
	fonts := func(c Canvas) listFonts {
		titleF, subF := rowFonts(c, l)
		return listFonts{rowTitle: titleF, rowSub: subF, button: l.font(c, 44, true), label: l.font(c, 26, false)}
	}

	c := newRecordCanvas()
	first := l.drawPagedList(c, fonts(c), l.pickerAreaTop, l.pickerAreaBottom, rows, 0)
	if first.pageSize < 1 || len(first.rows) != first.pageSize {
		t.Fatalf("first page drew %d rows for a page size of %d", len(first.rows), first.pageSize)
	}
	if !first.prev.Empty() {
		t.Error("first page offers a Prev button")
	}
	if first.next.Empty() {
		t.Error("first page has no Next button for a list that overflows")
	}
	pages := (len(rows) + first.pageSize - 1) / first.pageSize
	if want := fmt.Sprintf("Page 1 of %d  ·  20 items", pages); !c.drew(want) {
		t.Errorf("page indicator missing %q; drew %q", want, c.texts)
	}
	if !c.drew("Shelf 0") || c.drew(fmt.Sprintf("Shelf %d", first.pageSize)) {
		t.Errorf("first page drew the wrong slice: %q", c.texts)
	}

	// The last page keeps its offset even when it is short, and gives up
	// the Next button.
	last := (pages - 1) * first.pageSize
	c = newRecordCanvas()
	end := l.drawPagedList(c, fonts(c), l.pickerAreaTop, l.pickerAreaBottom, rows, last)
	if end.offset != last {
		t.Errorf("last page offset = %d, want %d", end.offset, last)
	}
	if end.prev.Empty() || !end.next.Empty() {
		t.Errorf("last page nav: prev=%v next=%v", end.prev, end.next)
	}
	if !c.drew(fmt.Sprintf("Page %d of %d", pages, pages)) {
		t.Errorf("page indicator on the last page: %q", c.texts)
	}

	// A list that fits needs no navigation at all.
	c = newRecordCanvas()
	short := l.drawPagedList(c, fonts(c), l.pickerAreaTop, l.pickerAreaBottom, rows[:2], 0)
	if !short.prev.Empty() || !short.next.Empty() || c.drew("Page 1 of") {
		t.Errorf("short list drew navigation: prev=%v next=%v texts=%q", short.prev, short.next, c.texts)
	}
}

func TestDrawSectionLabel(t *testing.T) {
	l := drawLayout()
	c := newRecordCanvas()
	l.drawSectionLabel(c, l.font(c, 24, true), "SERVER", l.serverLabelY)
	if !c.drew("SERVER") {
		t.Errorf("section label missing; drew %q", c.texts)
	}
}

// Centred labels are how every button is labelled, and the centring has
// to hold for whatever face the caller activated last.
func TestDrawCenteredTextCentresInTheRect(t *testing.T) {
	l := drawLayout()
	c := newImageCanvas(screenshotScreen)
	// Activate a different face first: the helper has to select its own.
	c.SetFont(l.font(c, 26, false), darkGray)
	btn := l.syncButton
	drawCenteredText(c, l.font(c, 44, true), btn, "Sync Now")

	ink := inkBounds(c.img, btn)
	if ink.Empty() {
		t.Fatal("nothing drawn in the button")
	}
	// The measured width includes the first and last glyph's side
	// bearings, which the inked box does not, so the horizontal centre is
	// only exact to a few pixels.
	const sideBearings = 6
	if dx := center(ink).X - center(btn).X; dx < -sideBearings || dx > sideBearings {
		t.Errorf("label is %d px off centre horizontally (ink %v in %v)", dx, ink, btn)
	}
	// Vertically the glyph box is centred, and the inked pixels sit in
	// its upper part (cap height, no descender in "Sync Now"), so the
	// ink centre is allowed to be up to half a line high.
	if dy := center(ink).Y - center(btn).Y; dy < -l.fpx(44)/2 || dy > l.fpx(44)/2 {
		t.Errorf("label is %d px off centre vertically (ink %v in %v)", dy, ink, btn)
	}
}

func center(r image.Rectangle) image.Point {
	return image.Point{X: (r.Min.X + r.Max.X) / 2, Y: (r.Min.Y + r.Max.Y) / 2}
}

// inkBounds is the bounding box of everything drawn inside r.
func inkBounds(img *image.Gray, r image.Rectangle) image.Rectangle {
	out := image.Rectangle{}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.GrayAt(x, y).Y == 0xff {
				continue
			}
			p := image.Rect(x, y, x+1, y+1)
			if out.Empty() {
				out = p
			} else {
				out = out.Union(p)
			}
		}
	}
	return out
}

// blackPixels counts the fully black pixels in r. The toggle paints its
// two states in different shades, so "black" separates the thumb (black
// when on, a black outline around white when off) from the gray track
// either side of it.
func blackPixels(img *image.Gray, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.GrayAt(x, y).Y == 0x00 {
				n++
			}
		}
	}
	return n
}
