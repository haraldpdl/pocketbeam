//go:build !arm

package main

import (
	"fmt"
	"image"
	"testing"
)

func drawDeleteConfirmOn(v deleteConfirmView) *recordCanvas {
	c := newRecordCanvas()
	drawDeleteConfirm(c, computeLayout(c.Size()), v)
	return c
}

func drawSpaceWarnOn(v spaceWarnView) *recordCanvas {
	c := newRecordCanvas()
	drawSpaceWarn(c, computeLayout(c.Size()), v)
	return c
}

// The prompt names a few of the books so the user can recognise what is
// about to go, and counts the rest rather than running off the screen.
func TestDeleteConfirmViewOfPreviewsTheFirstFew(t *testing.T) {
	books := make([]LocalBook, 0, 9)
	for i := range cap(books) {
		books = append(books, LocalBook{Author: fmt.Sprintf("Author %d", i), Title: fmt.Sprintf("Title %d", i)})
	}
	v := deleteConfirmViewOf(books)
	if v.total != len(books) {
		t.Errorf("total = %d, want %d", v.total, len(books))
	}
	if len(v.preview) != deletePreview {
		t.Fatalf("previewed %d books, want %d", len(v.preview), deletePreview)
	}
	if want := "Author 0 — Title 0"; v.preview[0] != want {
		t.Errorf("first preview line = %q, want %q", v.preview[0], want)
	}

	short := deleteConfirmViewOf(books[:2])
	if len(short.preview) != 2 || short.total != 2 {
		t.Errorf("a set shorter than the preview = %d of %d", len(short.preview), short.total)
	}
	if v := deleteConfirmViewOf(nil); v.total != 0 || len(v.preview) != 0 {
		t.Errorf("empty set = %d of %d", len(v.preview), v.total)
	}
}

func TestDrawDeleteConfirmListsWhatGoes(t *testing.T) {
	c := drawDeleteConfirmOn(deleteConfirmFixtureView())
	for _, want := range []string{
		"Confirm deletion", "8 books are no longer on the server.",
		"Delete them from this device?", "Ursula K. Le Guin — The Dispossessed",
		"… and 3 more", "Keep", "Delete",
	} {
		if !c.drew(want) {
			t.Errorf("delete prompt did not draw %q; drew %q", want, c.texts)
		}
	}

	// A set that fits in the preview has nothing left to count.
	short := drawDeleteConfirmOn(deleteConfirmViewOf([]LocalBook{{Author: "Lem", Title: "Solaris"}}))
	if !short.drew("Lem — Solaris") || short.drew("and 0 more") || short.drew("… and") {
		t.Errorf("fully listed set drew %q", short.texts)
	}
}

func TestSpaceWarnViewOfCountsTheDownload(t *testing.T) {
	v := spaceWarnViewOf(SyncPlan{
		NewBooks:      make([]Book, 4),
		UpdatedBooks:  make([]Book, 2),
		Unchanged:     30,
		DownloadBytes: 900,
		FreeBytes:     100,
	})
	// Unchanged books are neither downloaded nor counted.
	if v.books != 6 {
		t.Errorf("books = %d, want the 4 new plus the 2 updated", v.books)
	}
	if v.needBytes != 900 || v.freeBytes != 100 {
		t.Errorf("need/free = %d/%d, want 900/100", v.needBytes, v.freeBytes)
	}
}

func TestDrawSpaceWarnShowsTheShortfall(t *testing.T) {
	c := drawSpaceWarnOn(spaceWarnFixtureView())
	for _, want := range []string{
		"Not enough space", "145 books · 1.8 GB needed", "612 MB free on device",
		"4 books had unknown size", "Up to 240 MB freed after delete-missing.",
		"Download anyway?", "Download", "Cancel",
	} {
		if !c.drew(want) {
			t.Errorf("space warning did not draw %q; drew %q", want, c.texts)
		}
	}

	// Both notes are conditional: an exact estimate with nothing to
	// reclaim says neither.
	plain := drawSpaceWarnOn(spaceWarnView{books: 2, needBytes: 500_000, freeBytes: 100_000})
	if plain.drew("unknown size") || plain.drew("delete-missing") {
		t.Errorf("plain shortfall drew a conditional note: %q", plain.texts)
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{
		-1:            "0 B",
		0:             "0 B",
		999:           "999 B",
		1000:          "1 KB",
		1_500_000:     "2 MB",
		1_840_000_000: "1.8 GB",
	}
	for b, want := range cases {
		if got := formatBytes(b); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", b, got, want)
		}
	}
}

func TestDrawLibraryRefreshAnimatesOneLine(t *testing.T) {
	l := computeLayout(screenshotScreen)
	for _, dots := range []int{0, 1, 2, 3} {
		c := newRecordCanvas()
		drawLibraryRefresh(c, l, libraryRefreshView{dots: dots})
		want := "Indexing new books on the device"
		for range dots {
			want += "."
		}
		if !c.drew(want) {
			t.Errorf("%d dots drew %q", dots, c.texts)
		}
		if !c.drew("Refreshing library") || !c.drew("This closes on its own.") {
			t.Errorf("dialog is missing a fixed line; drew %q", c.texts)
		}
	}
}

// The spinner repaints the animated line alone by filling the strip the
// layout reserves and drawing into it, so the line has to land inside
// that strip or the dots accumulate on screen.
func TestLibraryRefreshLineStaysInItsStrip(t *testing.T) {
	for _, sz := range []image.Point{screenshotScreen, {X: 1072, Y: 1448}, {X: 758, Y: 1024}} {
		t.Run(fmt.Sprintf("%dx%d", sz.X, sz.Y), func(t *testing.T) {
			l := computeLayout(sz)
			c := newImageCanvas(sz)
			drawLibraryRefreshLine(c, l, libraryRefreshView{dots: 3})
			if n := inked(c.img, l.libRefreshLine); n < 100 {
				t.Errorf("strip %v has %d inked pixels, want the drawn line", l.libRefreshLine, n)
			}
			if n := inked(c.img, image.Rectangle{Max: sz}) - inked(c.img, l.libRefreshLine); n != 0 {
				t.Errorf("%d pixels of the line fall outside the strip %v", n, l.libRefreshLine)
			}
		})
	}
}

// Both confirmations are answered by tapping a button, and their pointer
// handlers read the two rects straight off the layout, so the labels
// have to be drawn exactly where the layout puts them.
func TestConfirmButtonsMatchTheTapTargets(t *testing.T) {
	l := computeLayout(screenshotScreen)
	for name, draw := range map[string]func(c Canvas){
		"delete": func(c Canvas) { drawDeleteConfirm(c, l, deleteConfirmFixtureView()) },
		"space":  func(c Canvas) { drawSpaceWarn(c, l, spaceWarnFixtureView()) },
	} {
		t.Run(name, func(t *testing.T) {
			c := newImageCanvas(screenshotScreen)
			draw(c)
			for _, btn := range []image.Rectangle{l.confirmLeftButton, l.confirmRightButton} {
				if n := inked(c.img, btn); n < 100 {
					t.Errorf("button %v has %d inked pixels, want the drawn answer", btn, n)
				}
			}
		})
	}
}

// Every line is placed by a hard-coded offset against the face it is
// drawn in, and the two confirmations grow by a line each when an
// optional note applies, so a full prompt silently stacks two lines
// without any text assertion noticing. There is no device in CI, so the
// geometry is asserted directly, as on the main screen.
func TestDialogLinesDoNotOverlap(t *testing.T) {
	l := computeLayout(screenshotScreen)
	long := make([]LocalBook, 12)
	for i := range long {
		long[i] = LocalBook{
			Author: "A Rather Long Author Name Indeed",
			Title:  fmt.Sprintf("An Equally Long Book Title, Volume %d", i),
		}
	}
	for name, draw := range map[string]func(c Canvas){
		"delete prompt": func(c Canvas) { drawDeleteConfirm(c, l, deleteConfirmViewOf(long)) },
		"delete prompt, one book": func(c Canvas) {
			drawDeleteConfirm(c, l, deleteConfirmViewOf(long[:1]))
		},
		"space warning": func(c Canvas) { drawSpaceWarn(c, l, spaceWarnFixtureView()) },
		"space warning without notes": func(c Canvas) {
			drawSpaceWarn(c, l, spaceWarnView{books: 9, needBytes: 4_000_000, freeBytes: 1_000_000})
		},
		"library refresh": func(c Canvas) { drawLibraryRefresh(c, l, libraryRefreshView{dots: 3}) },
	} {
		t.Run(name, func(t *testing.T) {
			c := newRecordCanvas()
			draw(c)
			if a, b, ok := c.overlappingText(); ok {
				t.Errorf("%q at %v overlaps %q at %v", a.s, a.r, b.s, b.r)
			}
		})
	}
}
