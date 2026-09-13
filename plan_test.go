package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func makeBookSized(uuid, title string, size int64, updated time.Time) Book {
	return Book{
		UUID:    uuid,
		Title:   title,
		Author:  "Author",
		Updated: updated,
		URL:     "http://x/" + uuid,
		Format:  "application/epub+zip",
		Size:    size,
	}
}

func TestPlan_ClassifiesNewUpdatedUnchanged(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{books: []Book{
		makeBookSized("a", "A", 100, t0),
		makeBookSized("b", "B", 200, t0),
	}}
	if res := Sync(context.Background(), src, store, library, nil, SyncOptions{Scope: "s1"}); res.FirstErr != nil {
		t.Fatalf("seed sync: %v", res.FirstErr)
	}

	t1 := t0.Add(time.Hour)
	src2 := &fakeSource{books: []Book{
		makeBookSized("a", "A", 100, t0),    // unchanged
		makeBookSized("b", "B v2", 250, t1), // updated
		makeBookSized("c", "C", 300, t0),    // new
		makeBookSized("d", "D", 0, t0),      // new, unknown size
	}}
	plan, books, err := Plan(context.Background(), src2, store, library, SyncOptions{Scope: "s1"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(books) != 4 {
		t.Errorf("books len=%d, want 4", len(books))
	}
	if plan.Unchanged != 1 {
		t.Errorf("Unchanged=%d, want 1", plan.Unchanged)
	}
	if len(plan.NewBooks) != 2 {
		t.Errorf("NewBooks len=%d, want 2 (C and D)", len(plan.NewBooks))
	}
	if len(plan.UpdatedBooks) != 1 {
		t.Errorf("UpdatedBooks len=%d, want 1 (B v2)", len(plan.UpdatedBooks))
	}
	// DownloadBytes = 250 (B v2) + 300 (C) = 550. D is unknown.
	if plan.DownloadBytes != 550 {
		t.Errorf("DownloadBytes=%d, want 550", plan.DownloadBytes)
	}
	if plan.UnknownSizes != 1 {
		t.Errorf("UnknownSizes=%d, want 1 (D)", plan.UnknownSizes)
	}
}

func TestPlan_CachedSizeFallback(t *testing.T) {
	// When the server stops advertising length on a second run, the plan
	// falls back to the size cached during the first download (which is
	// overwritten with actual-bytes-on-disk by Upsert).
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{books: []Book{makeBookSized("a", "A", 500, t0)}}
	if res := Sync(context.Background(), src, store, library, nil, SyncOptions{Scope: "s1"}); res.FirstErr != nil {
		t.Fatalf("seed sync: %v", res.FirstErr)
	}

	// Book a is now updated on the server but the server forgot to send
	// its length. Plan should use the cached size from the first run.
	t1 := t0.Add(time.Hour)
	src2 := &fakeSource{books: []Book{makeBookSized("a", "A v2", 0, t1)}}
	plan, _, err := Plan(context.Background(), src2, store, library, SyncOptions{Scope: "s1"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.DownloadBytes == 0 {
		t.Errorf("expected cached size to contribute to DownloadBytes; got 0")
	}
	if plan.UnknownSizes != 0 {
		t.Errorf("UnknownSizes=%d, want 0 (cache covered it)", plan.UnknownSizes)
	}
}

func TestPlan_MissingRespectsGuards(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seed := &fakeSource{books: []Book{
		makeBookSized("a", "A", 100, t0),
		makeBookSized("b", "B", 200, t0),
	}}
	_ = Sync(context.Background(), seed, store, library, nil, SyncOptions{Scope: "s1"})

	// Scope mismatch: Missing must be empty even though b is gone from remote.
	thin := &fakeSource{books: []Book{makeBookSized("a", "A", 100, t0)}}
	plan, _, err := Plan(context.Background(), thin, store, library, SyncOptions{
		DeleteMissing: true, Scope: "s2",
	})
	if err != nil {
		t.Fatalf("Plan (scope mismatch): %v", err)
	}
	if len(plan.Missing) != 0 || plan.ReclaimableBytes != 0 {
		t.Errorf("scope mismatch should zero Missing; got %+v / %d", plan.Missing, plan.ReclaimableBytes)
	}

	// DeleteMissing off: Missing must remain empty.
	plan, _, err = Plan(context.Background(), thin, store, library, SyncOptions{Scope: "s1"})
	if err != nil {
		t.Fatalf("Plan (no delete): %v", err)
	}
	if len(plan.Missing) != 0 || plan.ReclaimableBytes != 0 {
		t.Errorf("DeleteMissing=false should skip Missing; got %+v / %d", plan.Missing, plan.ReclaimableBytes)
	}

	// Empty remote guard.
	empty := &fakeSource{books: nil}
	plan, _, err = Plan(context.Background(), empty, store, library, SyncOptions{
		DeleteMissing: true, Scope: "s1",
	})
	if err != nil {
		t.Fatalf("Plan (empty remote): %v", err)
	}
	if len(plan.Missing) != 0 {
		t.Errorf("empty remote should zero Missing; got %+v", plan.Missing)
	}

	// Same scope, delete-missing on: Missing should carry b with its size.
	plan, _, err = Plan(context.Background(), thin, store, library, SyncOptions{
		DeleteMissing: true, Scope: "s1",
	})
	if err != nil {
		t.Fatalf("Plan (happy): %v", err)
	}
	if len(plan.Missing) != 1 || plan.Missing[0].UUID != "b" {
		t.Errorf("Missing=%+v, want single entry for b", plan.Missing)
	}
	if plan.ReclaimableBytes == 0 {
		t.Errorf("ReclaimableBytes=0, want >0 (b had a size)")
	}
}

func TestPlan_FitsReports(t *testing.T) {
	cases := []struct {
		name                string
		download            int64
		free                int64
		unknown             int
		wantOK, wantCertain bool
	}{
		{"fits_exact", 100, 100, 0, true, true},
		{"fits_loose", 50, 100, 0, true, true},
		{"overflow", 101, 100, 0, false, true},
		{"fits_with_unknown", 50, 100, 2, true, false},
		{"free_unknown", 50, 0, 0, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := SyncPlan{DownloadBytes: tc.download, FreeBytes: tc.free, UnknownSizes: tc.unknown}
			ok, certain := p.Fits()
			if ok != tc.wantOK || certain != tc.wantCertain {
				t.Errorf("Fits()=%v,%v want %v,%v", ok, certain, tc.wantOK, tc.wantCertain)
			}
		})
	}
}

func TestPlan_CachedSizeOverwrittenByDownload(t *testing.T) {
	// After a download, the store should hold the actual bytes-on-disk, not
	// whatever the server's length attribute claimed. Plan's cached-size
	// fallback then works off ground truth.
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Server lies: claims 1_000_000, actual body is 13 bytes ("pretend-ebook").
	src := &fakeSource{books: []Book{makeBookSized("a", "A", 1_000_000, t0)}}
	if res := Sync(context.Background(), src, store, library, nil, SyncOptions{Scope: "s"}); res.FirstErr != nil {
		t.Fatalf("Sync: %v", res.FirstErr)
	}
	_, _, cached, exists, err := store.LocalEntry("a")
	if err != nil || !exists {
		t.Fatalf("LocalEntry: exists=%v err=%v", exists, err)
	}
	if cached != 13 {
		t.Errorf("cached size=%d, want 13 (actual-bytes-on-disk, not the server lie)", cached)
	}
}

// A missing book whose file is still claimed by another tracked book (legacy
// shared path) is listed for deletion but frees no space, so the estimate
// must not count its bytes.
func TestPlan_SharedPathNotReclaimable(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")
	shared := filepath.Join(library, "Author", "Same Title.epub")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := makeBookSized("a", "Same Title", 100, t0)
	b := makeBookSized("b", "Same Title", 100, t0)
	for _, bk := range []Book{a, b} {
		if err := store.Upsert(bk, shared, 100); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetMeta(metaLastScope, "s1"); err != nil {
		t.Fatal(err)
	}

	plan, _, err := Plan(context.Background(), &fakeSource{books: []Book{b}}, store, library, SyncOptions{
		DeleteMissing: true, Scope: "s1",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Missing) != 1 || plan.Missing[0].UUID != "a" {
		t.Errorf("Missing=%+v, want single entry for a", plan.Missing)
	}
	if plan.ReclaimableBytes != 0 {
		t.Errorf("ReclaimableBytes=%d, want 0 (file shared with b)", plan.ReclaimableBytes)
	}
}
