//go:build !arm

package main

import (
	"errors"
	"fmt"
	"image"
	"testing"
)

func drawUpdateOn(v updateView) *recordCanvas {
	c := newRecordCanvas()
	drawUpdate(c, computeLayout(c.Size()), v)
	return c
}

func updateTestRelease() Release {
	return Release{Version: "v0.6.0", BinarySize: 7_300_000, SHA256: "9f2c4a1b7d3e5086c41f9ab2d7e6035481cbb9de7a2f4c10d8e63b5a90271fe4"}
}

func TestDrawUpdateStates(t *testing.T) {
	rel := updateTestRelease()
	cases := []struct {
		name string
		snap updateSnapshot
		want []string
		miss string
	}{
		{
			name: "up to date",
			want: []string{"Updates", "Installed version v0.5.0", "You are up to date", "Check for updates"},
			miss: "Install now",
		},
		{
			name: "available",
			snap: updateSnapshot{available: true, rel: rel},
			want: []string{"v0.6.0 is available", "7 MB", "Tap Install now to update", "sha256 9f2c4a1b7d3e", "Install now"},
			miss: "Check for updates",
		},
		{
			name: "checking",
			snap: updateSnapshot{checking: true},
			want: []string{"Checking for updates…"},
			// The check is already running, so there is nothing to offer.
			miss: "Check for updates",
		},
		{
			name: "check failed",
			snap: updateSnapshot{checkErr: errors.New("no Wi-Fi: not connected")},
			want: []string{"Could not check", "no Wi-Fi: not connected", "Check for updates"},
		},
		{
			name: "downloading",
			snap: updateSnapshot{available: true, rel: rel, downloading: true, downloaded: 2_920_000, total: rel.BinarySize},
			want: []string{"Downloading v0.6.0", "40%", "2851 / 7128 KB"},
			miss: "Install now",
		},
		{
			name: "downloading without a length",
			snap: updateSnapshot{available: true, rel: rel, downloading: true, downloaded: 1_024_000, total: -1},
			want: []string{"1000 KB received"},
			miss: "%",
		},
		{
			name: "installed",
			snap: updateSnapshot{rel: rel, installed: true},
			want: []string{"Installed v0.6.0", "Relaunching…"},
			miss: "Install now",
		},
		{
			name: "install failed",
			snap: updateSnapshot{available: true, rel: rel, installErr: errors.New("download: sha256 mismatch")},
			want: []string{"Install failed", "sha256 mismatch", "Install now"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := drawUpdateOn(updateViewOf(tc.snap, true, "v0.5.0"))
			for _, want := range tc.want {
				if !c.drew(want) {
					t.Errorf("update screen did not draw %q; drew %q", want, c.texts)
				}
			}
			if tc.miss != "" && c.drew(tc.miss) {
				t.Errorf("update screen drew %q, which it should not; drew %q", tc.miss, c.texts)
			}
		})
	}
}

// The primary button is one rect carrying one of two actions, so what it
// starts has to follow what it was labelled with; the pointer handler
// asks the same question the draw did.
func TestUpdatePrimaryAction(t *testing.T) {
	rel := updateTestRelease()
	cases := []struct {
		name string
		snap updateSnapshot
		want updateAction
	}{
		{"idle", updateSnapshot{}, updateActionCheck},
		{"check failed", updateSnapshot{checkErr: errors.New("boom")}, updateActionCheck},
		{"available", updateSnapshot{available: true, rel: rel}, updateActionInstall},
		{"checking", updateSnapshot{checking: true}, updateActionNone},
		{"downloading", updateSnapshot{available: true, rel: rel, downloading: true, total: -1}, updateActionNone},
		{"installed", updateSnapshot{rel: rel, installed: true}, updateActionNone},
		{"install failed", updateSnapshot{available: true, rel: rel, installErr: errors.New("boom")}, updateActionInstall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := updateViewOf(tc.snap, false, "v0.5.0")
			if got := v.primaryAction(); got != tc.want {
				t.Fatalf("primaryAction = %d, want %d", got, tc.want)
			}
			c := newImageCanvas(screenshotScreen)
			l := computeLayout(screenshotScreen)
			drawUpdate(c, l, v)
			n := inked(c.img, l.updatePrimaryButton)
			if tc.want == updateActionNone && n != 0 {
				t.Errorf("no action offered, but the button slot has %d inked pixels", n)
			}
			if tc.want != updateActionNone && n < 100 {
				t.Errorf("action %d offered, but the button slot has %d inked pixels", tc.want, n)
			}
		})
	}
}

// The toggle row is what the config's automatic-check setting looks
// like, and tapping it is the documented way to switch the weekly check
// off.
func TestDrawUpdateToggleFollowsTheSetting(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			c := drawUpdateOn(updateViewOf(updateSnapshot{}, on, "v0.5.0"))
			if !c.drew(autoCheckTitle) || !c.drew(autoCheckSubtitle(on)) {
				t.Errorf("toggle row with the setting %v drew %q", on, c.texts)
			}
		})
	}
}

func TestDownloadLine(t *testing.T) {
	cases := []struct {
		downloaded, total int64
		want              string
	}{
		{0, 1000 * 1024, "0%  ·  0 / 1000 KB"},
		{512 * 1024, 1024 * 1024, "50%  ·  512 / 1024 KB"},
		// A server that sends more than it announced cannot report past 100%.
		{2048 * 1024, 1024 * 1024, "100%  ·  2048 / 1024 KB"},
		// No Content-Length: bytes received, no percentage to compute.
		{700 * 1024, -1, "700 KB received"},
		{700 * 1024, 0, "700 KB received"},
	}
	for _, tc := range cases {
		if got := downloadLine(tc.downloaded, tc.total); got != tc.want {
			t.Errorf("downloadLine(%d, %d) = %q, want %q", tc.downloaded, tc.total, got, tc.want)
		}
	}
}

// The bar is the one geometry the status strip computes itself, so it is
// checked in pixels rather than through the recorder.
func TestDrawUpdateBarFillsWithProgress(t *testing.T) {
	l := computeLayout(screenshotScreen)
	rel := updateTestRelease()
	filled := func(done int64) int {
		c := newImageCanvas(screenshotScreen)
		drawUpdateStatus(c, l, updateSnapshot{available: true, rel: rel, downloading: true, downloaded: done, total: rel.BinarySize})
		return inked(c.img, l.updateStatusArea)
	}
	start, quarter, half := filled(0), filled(rel.BinarySize/4), filled(rel.BinarySize/2)
	if quarter <= start || half <= quarter {
		t.Errorf("fill does not grow with progress: 0=%d 1/4=%d 1/2=%d", start, quarter, half)
	}
	// The fill stays inside the strip the goroutine repaints on its own,
	// or a progress tick leaves the overflow behind on the panel.
	over := newImageCanvas(screenshotScreen)
	drawUpdateStatus(over, l, updateSnapshot{available: true, rel: rel, downloading: true, downloaded: 2 * rel.BinarySize, total: rel.BinarySize})
	if n := inked(over.img, image.Rect(0, l.updateStatusArea.Max.Y, l.screen.X, l.screen.Y)); n != 0 {
		t.Errorf("%d pixels drawn below the status strip", n)
	}
}

// Every line is placed by a hard-coded offset against the face it is
// drawn in, so a state that fills in one line too many silently stacks
// two of them.
func TestDrawUpdateLinesDoNotOverlap(t *testing.T) {
	rel := updateTestRelease()
	for name, snap := range map[string]updateSnapshot{
		"up to date":  {},
		"available":   {available: true, rel: rel},
		"checking":    {checking: true},
		"check error": {checkErr: errors.New("no Wi-Fi: wifi is not connected and could not be started")},
		"downloading": {available: true, rel: rel, downloading: true, downloaded: 2_920_000, total: rel.BinarySize},
		"installed":   {rel: rel, installed: true},
		"install error": {available: true, rel: rel, installErr: errors.New(
			"auto-relaunch failed: exec format error. Quit pocketbeam and reopen it from the Applications menu to finish the update.")},
	} {
		t.Run(name, func(t *testing.T) {
			if a, b, ok := drawUpdateOn(updateViewOf(snap, true, "v0.5.0")).overlappingText(); ok {
				t.Errorf("%q at %v overlaps %q at %v", a.s, a.r, b.s, b.r)
			}
		})
	}
}

// The status strip, the primary button, the toggle row and Back are
// stacked down the screen on every panel tier; two of them sharing
// pixels would make one untappable or leave a repainted strip on top of
// another row.
func TestUpdateRowsStackAboveBack(t *testing.T) {
	for _, sz := range []image.Point{screenshotScreen, {X: 1072, Y: 1448}, {X: 758, Y: 1024}} {
		t.Run(fmt.Sprintf("%dx%d", sz.X, sz.Y), func(t *testing.T) {
			l := computeLayout(sz)
			for _, tc := range []struct {
				name   string
				above  image.Rectangle
				below  image.Rectangle
				belowN string
			}{
				{"status strip", l.updateStatusArea, l.updatePrimaryButton, "primary button"},
				{"primary button", l.updatePrimaryButton, l.updateToggleRow, "toggle row"},
				{"toggle row", l.updateToggleRow, l.backButton, "Back"},
			} {
				if tc.above.Max.Y > tc.below.Min.Y {
					t.Errorf("%s ends at y=%d, past the %s at y=%d", tc.name, tc.above.Max.Y, tc.belowN, tc.below.Min.Y)
				}
			}
		})
	}
}
