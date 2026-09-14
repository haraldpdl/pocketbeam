//go:build !arm

package main

import (
	"errors"
	"testing"
	"time"
)

// mainTestView is the baseline the cases below vary one thing from.
func mainTestView() mainView {
	return mainView{
		subtitle:    "https://books.example.com  ·  All books",
		stats:       mainStats{hasLastSync: true, bookCount: 12},
		lastSyncAgo: "2 hours ago",
	}
}

func drawMainOn(v mainView) *recordCanvas {
	c := newRecordCanvas()
	drawMain(c, computeLayout(c.Size()), v)
	return c
}

func TestDrawMainShowsLibraryState(t *testing.T) {
	c := drawMainOn(mainTestView())
	for _, want := range []string{"pocketbeam", "https://books.example.com", "Last synced 2 hours ago", "12 books in library", "Sync Now", "Network", "Settings", "Quit"} {
		if !c.drew(want) {
			t.Errorf("main screen did not draw %q; drew %q", want, c.texts)
		}
	}
}

func TestDrawMainBeforeFirstSync(t *testing.T) {
	v := mainTestView()
	v.stats = mainStats{}
	c := drawMainOn(v)
	if !c.drew("Not yet synced") || !c.drew("Tap Sync Now to begin") {
		t.Errorf("first-run main screen drew %q", c.texts)
	}
	if c.drew("books in library") {
		t.Error("book count shown before the first sync")
	}
}

func TestDrawMainReportsFailures(t *testing.T) {
	v := mainTestView()
	v.stats.lastSync.Failed = 3
	c := drawMainOn(v)
	if !c.drew("3 failed") {
		t.Errorf("failure count missing; drew %q", c.texts)
	}
}

// The primary button doubles as Cancel while a sync runs, which is the
// only way to stop one.
func TestDrawMainPrimaryButtonBecomesStop(t *testing.T) {
	v := mainTestView()
	v.sync = syncSnapshot{active: true, index: 1, total: 4}
	c := drawMainOn(v)
	if !c.drew("Stop") || c.drew("Sync Now") {
		t.Errorf("syncing main screen drew %q", c.texts)
	}
}

func TestDrawMainUpdateBadge(t *testing.T) {
	c := drawMainOn(mainTestView())
	if c.drew("available in Settings") {
		t.Error("update badge drawn with no update waiting")
	}
	v := mainTestView()
	v.updateVer = "v9.9.9"
	if c := drawMainOn(v); !c.drew("Update v9.9.9 available in Settings") {
		t.Errorf("update badge missing; drew %q", c.texts)
	}
}

// Every line on the screen is placed by a hard-coded offset, and a
// string occupies a glyph box as tall as the face it is drawn in, so a
// gap smaller than the line above it silently stacks two lines on top of
// each other. That is not visible in a text assertion and there is no
// device in CI, so the geometry is asserted directly.
func TestDrawMainLinesDoNotOverlap(t *testing.T) {
	full := mainTestView()
	full.updateVer = "v9.9.9"
	full.stats.lastSync.Failed = 3
	syncing := mainTestView()
	syncing.sync = syncSnapshot{active: true, index: 7, total: 24, author: "Le Guin", title: "The Dispossessed", elapsed: 95 * time.Second}
	for name, v := range map[string]mainView{
		"idle": mainTestView(),
		"first run": func() mainView {
			v := mainTestView()
			v.stats = mainStats{}
			return v
		}(),
		"failures and an update": full,
		"syncing":                syncing,
	} {
		t.Run(name, func(t *testing.T) {
			if a, b, ok := drawMainOn(v).overlappingText(); ok {
				t.Errorf("%q at %v overlaps %q at %v", a.s, a.r, b.s, b.r)
			}
		})
	}
}

func TestDrawMainProgressStates(t *testing.T) {
	cases := []struct {
		name string
		sync syncSnapshot
		want string
		miss string
	}{
		{"planning", syncSnapshot{planning: true}, "Checking remote catalog", ""},
		{"counter", syncSnapshot{active: true, index: 7, total: 24}, "7 / 24", "("},
		{"elapsed", syncSnapshot{active: true, index: 7, total: 24, elapsed: 95 * time.Second}, "7 / 24  (1:35)", ""},
		{"book", syncSnapshot{active: true, index: 1, total: 2, author: "Le Guin", title: "The Dispossessed"}, "Le Guin: The Dispossessed", ""},
		{"error", syncSnapshot{err: errors.New("server unreachable")}, "server unreachable", ""},
		{"unknown sizes", syncSnapshot{unknownSizes: 2}, "2 book(s) had unknown size", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newRecordCanvas()
			drawMainProgress(c, computeLayout(c.Size()), tc.sync)
			if !c.drew(tc.want) {
				t.Errorf("progress strip did not draw %q; drew %q", tc.want, c.texts)
			}
			if tc.miss != "" && c.drew(tc.miss) {
				t.Errorf("progress strip drew %q, which it should not; drew %q", tc.miss, c.texts)
			}
		})
	}
}

// An error is only the strip's message while no sync is running: a
// retry has to show its own progress, not the previous failure.
func TestDrawMainProgressActiveBeatsLastError(t *testing.T) {
	c := newRecordCanvas()
	drawMainProgress(c, computeLayout(c.Size()), syncSnapshot{active: true, index: 2, total: 3, err: errors.New("boom")})
	if c.drew("Last error") {
		t.Errorf("stale error shown during a sync; drew %q", c.texts)
	}
}

func TestMainSubtitle(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"no profile", nil, ""},
		{"opds unfiltered", &Config{Host: "https://cwa.example"}, "https://cwa.example  ·  All books"},
		{
			"opds filtered",
			&Config{Host: "https://cwa.example", FilterHrefs: []string{"/s/1"}, FilterNames: []string{"Sci-Fi"}},
			"https://cwa.example  ·  Sci-Fi",
		},
		{"webdav root", &Config{Backend: BackendWebDAV, Host: "https://nc.example"}, "https://nc.example  ·  /"},
		{"webdav path", &Config{Backend: BackendWebDAV, Host: "https://nc.example", Path: "/Books"}, "https://nc.example  ·  /Books"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mainSubtitle(tc.cfg); got != tc.want {
				t.Errorf("mainSubtitle = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatElapsed(t *testing.T) {
	cases := map[time.Duration]string{
		0:                               "0:00",
		9 * time.Second:                 "0:09",
		95 * time.Second:                "1:35",
		59*time.Minute + 59*time.Second: "59:59",
		time.Hour:                       "1:00:00",
		2*time.Hour + 3*time.Minute + 4*time.Second: "2:03:04",
	}
	for d, want := range cases {
		if got := formatElapsed(d); got != want {
			t.Errorf("formatElapsed(%s) = %q, want %q", d, got, want)
		}
	}
}

// The progress bar is the one geometry the strip computes itself, so it
// is checked in pixels rather than through the recorder.
func TestDrawMainProgressBarFillsWithProgress(t *testing.T) {
	l := computeLayout(screenshotScreen)
	filled := func(idx, total int) int {
		c := newImageCanvas(screenshotScreen)
		drawMainProgress(c, l, syncSnapshot{active: true, index: idx, total: total})
		return inked(c.img, l.progressBar.Inset(4))
	}
	quarter, half := filled(1, 4), filled(2, 4)
	if quarter == 0 {
		t.Fatal("progress bar drew no fill at 1/4")
	}
	if half <= quarter {
		t.Errorf("fill at 2/4 (%d px) is not wider than at 1/4 (%d px)", half, quarter)
	}
	if got := filled(0, 0); got != 0 {
		t.Errorf("fill with an unknown total = %d px, want 0", got)
	}
}
