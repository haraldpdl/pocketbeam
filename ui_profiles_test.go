//go:build !arm

package main

import (
	"errors"
	"fmt"
	"image"
	"testing"
)

func drawProfileListOn(v profileListView) *recordCanvas {
	c := newRecordCanvas()
	drawProfileList(c, computeLayout(c.Size()), v)
	return c
}

func drawProfileDetailOn(v profileDetailView) *recordCanvas {
	c := newRecordCanvas()
	drawProfileDetail(c, computeLayout(c.Size()), v)
	return c
}

func profileListTestView() profileListView {
	return profileListView{names: []string{"default", "nextcloud", "friends-opds"}, active: "nextcloud"}
}

func TestDrawProfileListShowsEveryProfile(t *testing.T) {
	c := drawProfileListOn(profileListTestView())
	for _, want := range []string{"Server profiles", "default", "nextcloud", "friends-opds", "Add new server", "Back"} {
		if !c.drew(want) {
			t.Errorf("profile list did not draw %q; drew %q", want, c.texts)
		}
	}
}

// The subtitle is the only marker of which profile the app is on, so it
// belongs to that profile's row and to no other.
func TestDrawProfileListMarksTheActiveProfile(t *testing.T) {
	l := computeLayout(screenshotScreen)
	v := profileListTestView()
	c := newRecordCanvas()
	drawProfileList(c, l, v)

	rects := l.layoutPagedList(l.pickerAreaTop, l.profileListBottom, len(v.names), v.offset)
	if len(rects.rows) != len(v.names) {
		t.Fatalf("laid out %d rows for %d profiles", len(rects.rows), len(v.names))
	}
	var marked []image.Rectangle
	for _, b := range c.boxes {
		if b.s == "Active" {
			marked = append(marked, b.r)
		}
	}
	if len(marked) != 1 {
		t.Fatalf("%d rows are marked Active, want exactly one; drew %q", len(marked), c.texts)
	}
	want := rects.rows[indexOf(v.names, v.active)]
	if !marked[0].In(want) {
		t.Errorf("the Active marker at %v is not in %q's row %v", marked[0], v.active, want)
	}
}

func indexOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}

// A profile file that cannot be read has to say so instead of drawing an
// empty list with an Add button over it.
func TestDrawProfileListReportsAnError(t *testing.T) {
	v := profileListTestView()
	v.err = errors.New("pocketbeam.cfg: permission denied")
	c := drawProfileListOn(v)
	if !c.drew("Could not list profiles:") || !c.drew("permission denied") {
		t.Errorf("error state drew %q", c.texts)
	}
	if c.drew("Add new server") || c.drew("default") {
		t.Errorf("error state drew the list anyway: %q", c.texts)
	}
	if !c.drew("Back") {
		t.Error("error state left no way back")
	}
}

// Every row the draw paints is a tap target the pointer handler computes
// on its own, so the two have to agree on where the rows are.
func TestDrawProfileListRowsLandOnTheirTapTargets(t *testing.T) {
	l := computeLayout(screenshotScreen)
	names := make([]string, 24)
	for i := range names {
		names[i] = fmt.Sprintf("profile-%02d", i)
	}
	v := profileListView{names: names, active: names[0]}
	rects := l.layoutPagedList(l.pickerAreaTop, l.profileListBottom, len(v.names), v.offset)
	if rects.pageSize >= len(names) {
		t.Fatalf("page size %d holds the whole list; the case needs pagination", rects.pageSize)
	}
	c := newImageCanvas(screenshotScreen)
	drawProfileList(c, l, v)
	for i, r := range rects.rows {
		if n := inked(c.img, r); n < 100 {
			t.Errorf("row %d at %v has %d inked pixels, want the drawn profile", i, r, n)
		}
	}
	if rects.next.Empty() {
		t.Fatal("a list of 24 profiles drew no Next button")
	}
	if n := inked(c.img, rects.next); n < 100 {
		t.Errorf("Next button at %v has %d inked pixels", rects.next, n)
	}
	// The rows stop above the Add button, which is hit-tested first: a
	// row drawn under it would be untappable.
	if last := rects.rows[len(rects.rows)-1]; last.Max.Y > l.profileAction.Min.Y {
		t.Errorf("last row ends at y=%d, past the Add button at y=%d", last.Max.Y, l.profileAction.Min.Y)
	}
}

func TestDrawProfileDetailShowsTheProfile(t *testing.T) {
	c := drawProfileDetailOn(profileDetailView{
		name:    "nextcloud",
		backend: BackendWebDAV,
		host:    "https://nc.example.com/remote.php/dav/files/alice",
	})
	for _, want := range []string{"nextcloud", "Profile", BackendWebDAV, "nc.example.com", "Make active", "Delete this profile", "Back"} {
		if !c.drew(want) {
			t.Errorf("profile detail did not draw %q; drew %q", want, c.texts)
		}
	}
}

// The active profile has nothing to switch to, and a profile that could
// not be read has no server details to switch to, so neither offers the
// button - and the pointer handler asks the view the same question.
func TestDrawProfileDetailOffersMakeActiveOnlyWhenItApplies(t *testing.T) {
	cases := map[string]profileDetailView{
		"active":     {name: "default", backend: BackendOPDS, isActive: true},
		"unreadable": {name: "broken", loadErr: errors.New("profile \"broken\" does not exist")},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			if v.canMakeActive() {
				t.Fatal("view offers Make active")
			}
			c := drawProfileDetailOn(v)
			if c.drew("Make active") {
				t.Errorf("drew Make active anyway: %q", c.texts)
			}
			if !c.drew("Delete this profile") {
				t.Errorf("delete is offered on every panel; drew %q", c.texts)
			}
		})
	}
	v := profileDetailView{name: "nextcloud", backend: BackendWebDAV, host: "https://nc.example"}
	if !v.canMakeActive() {
		t.Fatal("an inactive, readable profile does not offer Make active")
	}
}

func TestDrawProfileDetailReportsALoadError(t *testing.T) {
	c := drawProfileDetailOn(profileDetailView{name: "broken", loadErr: errors.New("profile \"broken\" does not exist")})
	if !c.drew("Could not load profile:") || !c.drew("does not exist") {
		t.Errorf("load error drew %q", c.texts)
	}
}

// The armed delete is the only warning the user gets before a profile
// goes, so the label has to name what the next tap does.
func TestProfileDetailDeleteLabel(t *testing.T) {
	cases := []struct {
		name string
		v    profileDetailView
		want string
	}{
		{"idle", profileDetailView{name: "nextcloud"}, "Delete this profile"},
		{"armed", profileDetailView{name: "nextcloud", confirming: true}, `Tap again to delete "nextcloud"`},
		{"armed, last profile", profileDetailView{name: "default", isActive: true, lastProfile: true, confirming: true}, "Tap again to reset pocketbeam"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.deleteLabel(); got != tc.want {
				t.Errorf("deleteLabel = %q, want %q", got, tc.want)
			}
			if c := drawProfileDetailOn(tc.v); !c.drew(tc.want) {
				t.Errorf("button drew %q, want %q", c.texts, tc.want)
			}
		})
	}
}

// Both screens place their lines by hard-coded offsets against the face
// they are drawn in, so a longer value or an extra line silently stacks
// two of them. There is no device in CI, so the geometry is asserted
// directly, as on the main screen.
func TestDrawProfileScreensLinesDoNotOverlap(t *testing.T) {
	long := make([]string, 30)
	for i := range long {
		long[i] = fmt.Sprintf("a-rather-long-profile-name-%02d", i)
	}
	for name, v := range map[string]profileListView{
		"one profile": {names: []string{"default"}, active: "default"},
		"three":       profileListTestView(),
		"paginated":   {names: long, active: long[0]},
		"second page": {names: long, active: long[0], offset: 6},
		"unreadable":  {err: errors.New("pocketbeam.cfg:12: malformed section header")},
	} {
		t.Run("list/"+name, func(t *testing.T) {
			if a, b, ok := drawProfileListOn(v).overlappingText(); ok {
				t.Errorf("%q at %v overlaps %q at %v", a.s, a.r, b.s, b.r)
			}
		})
	}
	for name, v := range map[string]profileDetailView{
		"active": {name: "default", backend: BackendOPDS, host: "https://cwa.lan:8083", isActive: true},
		"inactive": {
			name:    "a-rather-long-profile-name",
			backend: BackendWebDAV,
			host:    "https://nc.example.com/remote.php/dav/files/alice/Books/Fiction",
		},
		"armed":      {name: "nextcloud", backend: BackendWebDAV, host: "https://nc.example", confirming: true},
		"last":       {name: "default", backend: BackendOPDS, host: "https://cwa.lan:8083", isActive: true, lastProfile: true, confirming: true},
		"unreadable": {name: "broken", loadErr: errors.New("profile \"broken\" does not exist in pocketbeam.cfg")},
	} {
		t.Run("detail/"+name, func(t *testing.T) {
			if a, b, ok := drawProfileDetailOn(v).overlappingText(); ok {
				t.Errorf("%q at %v overlaps %q at %v", a.s, a.r, b.s, b.r)
			}
		})
	}
}

// The two action rows are stacked above the Back button on every panel
// tier, and the list's rows stop above them. Overlapping buttons would
// make one of them untappable.
func TestProfileActionRowsStackAboveBack(t *testing.T) {
	for _, sz := range []image.Point{screenshotScreen, {X: 1072, Y: 1448}, {X: 758, Y: 1024}} {
		t.Run(fmt.Sprintf("%dx%d", sz.X, sz.Y), func(t *testing.T) {
			l := computeLayout(sz)
			if l.profileUpperAction.Max.Y >= l.profileAction.Min.Y {
				t.Errorf("Make active ends at y=%d, at or below the row under it at y=%d",
					l.profileUpperAction.Max.Y, l.profileAction.Min.Y)
			}
			if l.profileAction.Max.Y >= l.backButton.Min.Y {
				t.Errorf("action row ends at y=%d, at or below Back at y=%d", l.profileAction.Max.Y, l.backButton.Min.Y)
			}
			if l.profileListBottom > l.profileAction.Min.Y {
				t.Errorf("list area ends at y=%d, past the action row at y=%d", l.profileListBottom, l.profileAction.Min.Y)
			}
		})
	}
}
