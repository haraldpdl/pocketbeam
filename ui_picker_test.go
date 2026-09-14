//go:build !arm

package main

import (
	"errors"
	"fmt"
	"image"
	"testing"
)

// feedSubs builds n subsections with known book counts, the shape a
// Calibre-Web catalog level has.
func feedSubs(n int) []FilterOption {
	subs := make([]FilterOption, 0, n)
	for i := range n {
		subs = append(subs, FilterOption{
			Name:       fmt.Sprintf("Shelf %d", i),
			Href:       fmt.Sprintf("/opds/shelf/%d", i),
			Count:      i + 1,
			CountKnown: true,
		})
	}
	return subs
}

// feedSnap is a loaded level two deep in the tree, which the cases vary
// one thing from.
func feedSnap(n int) feedPickerSnapshot {
	return feedPickerSnapshot{
		titles: []string{"Calibre-Web", "Categories"},
		href:   "/opds/category",
		level:  OPDSLevel{Subsections: feedSubs(n)},
	}
}

func drawPickerOn(v pickerView) *recordCanvas {
	c := newRecordCanvas()
	drawPicker(c, computeLayout(c.Size()), v)
	return c
}

func TestFeedPickerViewShowsTheLevel(t *testing.T) {
	c := drawPickerOn(feedPickerViewOf(feedSnap(3)))
	for _, want := range []string{"Select filter", "Calibre-Web / Categories", "< Back to Calibre-Web", "Shelf 0", "1 books", "Back"} {
		if !c.drew(want) {
			t.Errorf("feed picker did not draw %q; drew %q", want, c.texts)
		}
	}
}

// The root has nothing above it, and syncing it is syncing everything.
func TestFeedPickerViewAtRoot(t *testing.T) {
	s := feedPickerSnapshot{titles: []string{"All books"}, level: OPDSLevel{Subsections: feedSubs(2)}}
	v := feedPickerViewOf(s)
	if v.upLabel != "" {
		t.Errorf("root offers an up-row: %q", v.upLabel)
	}
	if v.primary != "Sync everything" {
		t.Errorf("root primary button = %q", v.primary)
	}
	// With a selection going, adding the root would mean the same thing,
	// so only the button that saves the set is offered.
	s.selected = []FilterOption{{Name: "Sci-Fi", Href: "/opds/shelf/2"}}
	v = feedPickerViewOf(s)
	if v.primary != "" {
		t.Errorf("root offers %q next to Done", v.primary)
	}
	if v.done != "Done (1 selected)" {
		t.Errorf("root done button = %q", v.done)
	}
}

func TestFeedPickerViewButtonLabels(t *testing.T) {
	sel := []FilterOption{{Name: "Categories", Href: "/opds/category"}}
	cases := []struct {
		name     string
		snap     func(s feedPickerSnapshot) feedPickerSnapshot
		primary  string
		done     string
		wantRows int
	}{
		{
			"level with a known total",
			func(s feedPickerSnapshot) feedPickerSnapshot {
				s.level.BookCount = 42
				return s
			},
			"Sync this level (42 books)", "", 3,
		},
		{
			"total summed from the subsections is approximate",
			func(s feedPickerSnapshot) feedPickerSnapshot { return s },
			"Sync this level (~6 books)", "", 3,
		},
		{
			"still loading",
			func(s feedPickerSnapshot) feedPickerSnapshot {
				s.loading = true
				return s
			},
			"Sync this level", "", 0,
		},
		{
			"the level is already in the selection",
			func(s feedPickerSnapshot) feedPickerSnapshot {
				s.selected = sel
				return s
			},
			"Remove this level", "Done (1 selected)", 3,
		},
		{
			"another level is in the selection",
			func(s feedPickerSnapshot) feedPickerSnapshot {
				s.selected = []FilterOption{{Name: "Fantasy", Href: "/opds/category/1"}}
				return s
			},
			"Add this level", "Done (1 selected)", 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := feedPickerViewOf(tc.snap(feedSnap(3)))
			if v.primary != tc.primary || v.done != tc.done {
				t.Errorf("buttons = %q / %q, want %q / %q", v.primary, v.done, tc.primary, tc.done)
			}
			if len(v.rows) != tc.wantRows {
				t.Errorf("rows = %d, want %d", len(v.rows), tc.wantRows)
			}
		})
	}
}

// A level that could not be fetched shows why, and keeps the up-row so
// the drill-down is not lost.
func TestFeedPickerViewError(t *testing.T) {
	s := feedSnap(3)
	s.err = errors.New("Server did not respond in time.")
	v := feedPickerViewOf(s)
	if len(v.rows) != 0 {
		t.Errorf("failed level drew %d rows", len(v.rows))
	}
	c := drawPickerOn(v)
	if !c.drew("Could not load feed:") || !c.drew("Server did not respond in time.") {
		t.Errorf("error block missing; drew %q", c.texts)
	}
	if !c.drew("< Back to Calibre-Web") {
		t.Errorf("up-row missing after a failed fetch; drew %q", c.texts)
	}
}

// Empty subsections are hidden, so what the rows index into is the
// visible list rather than the level's own.
func TestFeedPickerViewHidesEmptySubsections(t *testing.T) {
	s := feedSnap(0)
	s.level.Subsections = []FilterOption{
		{Name: "Full", Href: "/a", Count: 3, CountKnown: true},
		{Name: "Empty", Href: "/b", Count: 0, CountKnown: true},
		{Name: "Unknown", Href: "/c"},
	}
	v := feedPickerViewOf(s)
	if len(v.rows) != 2 || v.rows[0].title != "Full" || v.rows[1].title != "Unknown" {
		t.Errorf("rows = %+v, want Full and Unknown", v.rows)
	}
	if v.rows[1].subtitle != "" {
		t.Errorf("subsection with an unknown count drew %q", v.rows[1].subtitle)
	}
}

// A loading picker asks for the busy icon, and asks for it below the
// message it belongs to.
func TestDrawPickerRaisesTheBusyIconWhileLoading(t *testing.T) {
	l := computeLayout(screenshotScreen)
	s := feedSnap(0)
	s.loading = true
	at, busy := drawPicker(newRecordCanvas(), l, feedPickerViewOf(s))
	if !busy {
		t.Fatal("a loading level did not ask for the busy icon")
	}
	if at.Y <= l.pickerAreaTop {
		t.Errorf("busy icon at %v, want it below the list top %d", at, l.pickerAreaTop)
	}
	if _, busy := drawPicker(newRecordCanvas(), l, feedPickerViewOf(feedSnap(3))); busy {
		t.Error("a loaded level asked for the busy icon")
	}
}

func TestDirPickerView(t *testing.T) {
	v := dirPickerViewOf(dirPickerSnapshot{path: "/Books/Fiction", dirs: []string{"Novellas", "Series"}})
	c := drawPickerOn(v)
	for _, want := range []string{"Select folder", "/Books/Fiction", "< Back to Books", "Novellas", "Series", "Sync this folder"} {
		if !c.drew(want) {
			t.Errorf("directory picker did not draw %q; drew %q", want, c.texts)
		}
	}
	if v.done != "" {
		t.Errorf("directory picker offers a second button: %q", v.done)
	}
}

// At the root there is nothing to go up to, and a folder that did not
// list has nothing to sync.
func TestDirPickerViewEdges(t *testing.T) {
	root := dirPickerViewOf(dirPickerSnapshot{path: "/", dirs: []string{"Books"}})
	if root.upLabel != "" {
		t.Errorf("root offers an up-row: %q", root.upLabel)
	}
	if root.primary != "Sync this folder" {
		t.Errorf("root primary button = %q", root.primary)
	}
	failed := dirPickerViewOf(dirPickerSnapshot{path: "/Books", err: errors.New("Login rejected.")})
	if failed.primary != "" {
		t.Errorf("failed listing offers %q", failed.primary)
	}
	c := drawPickerOn(failed)
	if !c.drew("Could not list folder:") || !c.drew("Login rejected.") {
		t.Errorf("error block missing; drew %q", c.texts)
	}
}

// The pointer handlers hit-test against pickerGeometry rather than rects
// a draw published, so the rects it returns have to be the ones the rows
// were painted in.
func TestPickerGeometryMatchesWhatIsDrawn(t *testing.T) {
	l := computeLayout(screenshotScreen)
	v := feedPickerViewOf(feedSnap(20))
	g := l.pickerGeometry(v)
	if len(g.list.rows) == 0 {
		t.Fatal("no rows placed")
	}
	c := newRecordCanvas()
	drawPicker(c, l, v)
	for i, r := range g.list.rows {
		title := v.rows[g.list.offset+i].title
		var found bool
		for _, b := range c.boxes {
			if b.s == title {
				found = true
				if !b.r.In(r) {
					t.Errorf("%q drawn at %v, outside its tap target %v", title, b.r, r)
				}
			}
		}
		if !found {
			t.Errorf("row %d (%q) was placed but not drawn", i, title)
		}
	}
	// Rows off this page must not be drawn at all: they have no tap
	// target, so a user tapping one would drill into the wrong feed.
	for i := g.list.offset + len(g.list.rows); i < len(v.rows); i++ {
		if c.drew(v.rows[i].title) {
			t.Errorf("row %d (%q) is off the page but was drawn", i, v.rows[i].title)
		}
	}
}

// Paging steps by whole pages from the clamped offset, so the last page
// is reachable and an overshoot cannot accumulate.
func TestPickerGeometryPaging(t *testing.T) {
	l := computeLayout(screenshotScreen)
	v := feedPickerViewOf(feedSnap(20))
	first := l.pickerGeometry(v).list
	if !first.prev.Empty() || first.next.Empty() {
		t.Fatalf("first page nav: prev=%v next=%v", first.prev, first.next)
	}
	v.offset = pageStep(first.offset, first.pageSize, +1)
	second := l.pickerGeometry(v).list
	if second.offset != first.pageSize {
		t.Errorf("second page offset = %d, want %d", second.offset, first.pageSize)
	}
	// Past the end: the offset clamps onto the last page start, and
	// stepping on from there stays put.
	v.offset = 999
	last := l.pickerGeometry(v).list
	wantLast := (len(v.rows) - 1) / last.pageSize * last.pageSize
	if last.offset != wantLast {
		t.Errorf("overshot offset clamped to %d, want %d", last.offset, wantLast)
	}
	if !last.next.Empty() {
		t.Error("last page offers a Next button")
	}
	v.offset = pageStep(last.offset, last.pageSize, +1)
	if got := l.pickerGeometry(v).list.offset; got != wantLast {
		t.Errorf("paging past the last page moved to %d, want %d", got, wantLast)
	}
}

// The up-row is fixed above the paginated window, so it stays in the
// same place and the rows below start under it.
func TestPickerGeometryUpRow(t *testing.T) {
	l := computeLayout(screenshotScreen)
	withUp := l.pickerGeometry(feedPickerViewOf(feedSnap(20)))
	atRoot := l.pickerGeometry(feedPickerViewOf(feedPickerSnapshot{
		titles: []string{"All books"},
		level:  OPDSLevel{Subsections: feedSubs(20)},
	}))
	if withUp.up.Empty() {
		t.Fatal("no up-row two levels down")
	}
	if !atRoot.up.Empty() {
		t.Errorf("up-row at the root: %v", atRoot.up)
	}
	if withUp.up.Overlaps(withUp.list.rows[0]) {
		t.Errorf("up-row %v overlaps the first row %v", withUp.up, withUp.list.rows[0])
	}
	if withUp.list.rows[0].Min.Y < withUp.up.Max.Y {
		t.Errorf("first row starts at %d, above the up-row's bottom %d",
			withUp.list.rows[0].Min.Y, withUp.up.Max.Y)
	}
	if len(atRoot.list.rows) <= len(withUp.list.rows) {
		t.Errorf("root page holds %d rows, not more than the %d under an up-row",
			len(atRoot.list.rows), len(withUp.list.rows))
	}
}

// Nothing a picker draws may land on top of anything else it draws, and
// the tap targets must not overlap either: both are invisible to a text
// assertion and there is no device in CI.
func TestDrawPickerElementsDoNotOverlap(t *testing.T) {
	l := computeLayout(screenshotScreen)
	loading := feedSnap(0)
	loading.loading = true
	failed := feedSnap(0)
	failed.err = errors.New("Server did not respond in time.")
	selecting := feedSnap(20)
	selecting.selected = []FilterOption{{Name: "Fantasy", Href: "/opds/category/1"}}

	views := map[string]pickerView{
		"feed loading":    feedPickerViewOf(loading),
		"feed failed":     feedPickerViewOf(failed),
		"feed one page":   feedPickerViewOf(feedSnap(3)),
		"feed paginated":  feedPickerViewOf(feedSnap(20)),
		"feed selecting":  feedPickerViewOf(selecting),
		"feed fixture":    feedPickerFixtureView(),
		"dir root":        dirPickerViewOf(dirPickerSnapshot{path: "/", dirs: []string{"Books", "Documents"}}),
		"dir paginated":   dirPickerViewOf(dirPickerSnapshot{path: "/Books", dirs: dirNames(20)}),
		"dir fixture":     dirPickerFixtureView(),
		"dir long parent": dirPickerViewOf(dirPickerSnapshot{path: "/Books/Science Fiction and Fantasy/Le Guin"}),
	}
	for name, v := range views {
		t.Run(name, func(t *testing.T) {
			if a, b, ok := drawPickerOn(v).overlappingText(); ok {
				t.Errorf("%q at %v overlaps %q at %v", a.s, a.r, b.s, b.r)
			}
			for i, r := range pickerTargets(l.pickerGeometry(v)) {
				for _, other := range pickerTargets(l.pickerGeometry(v))[i+1:] {
					if r.Overlaps(other) {
						t.Errorf("tap targets %v and %v overlap", r, other)
					}
				}
				if !r.In(image.Rect(0, 0, l.screen.X, l.screen.Y)) {
					t.Errorf("tap target %v is off the %v panel", r, l.screen)
				}
			}
		})
	}
}

// pickerTargets is every rect a tap can land on, in one slice.
func pickerTargets(g pickerGeom) []image.Rectangle {
	out := []image.Rectangle{}
	for _, r := range append([]image.Rectangle{g.up, g.primary, g.done, g.list.prev, g.list.next}, g.list.rows...) {
		if !r.Empty() {
			out = append(out, r)
		}
	}
	return out
}

func dirNames(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("Folder %d", i))
	}
	return out
}
