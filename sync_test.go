package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Plain Title", "Plain Title"},
		{"Heller| Peter", "Heller_ Peter"},                   // pipe → underscore
		{"a/b\\c", "a_b_c"},                                  // path separators
		{"weird:title?with*chars", "weird_title_with_chars"}, // FAT-illegal
		{"  leading and trailing  ", "leading and trailing"}, // outer whitespace
		{"runs   of    spaces", "runs of spaces"},            // collapse internal whitespace
		{"trailing dots...", "trailing dots"},                // strip trailing dots
		{"", "_"},                                            // empty input
		{"...", "_"},                                         // becomes empty after strip
		{"control\x01char", "controlchar"},                   // strip control chars
		{"Die Ehefrau – Was hat sie zu verbergen?", "Die Ehefrau – Was hat sie zu verbergen_"}, // unicode preserved
	}
	for _, tc := range cases {
		if got := sanitize(tc.in); got != tc.want {
			t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPickAcquisition_PrefersEpub(t *testing.T) {
	links := []link{
		{Rel: "self", Type: "application/atom+xml"},
		{Rel: AcquisitionRel, Type: "application/x-cbz", Href: "/cbz"},
		{Rel: AcquisitionRel, Type: "application/epub+zip", Href: "/epub"},
		{Rel: AcquisitionRel, Type: "application/pdf", Href: "/pdf"},
	}
	if got := pickAcquisition(links).Href; got != "/epub" {
		t.Errorf("expected EPUB to win, got %q", got)
	}
}

func TestPickAcquisition_FallsThroughToCBZ(t *testing.T) {
	links := []link{
		{Rel: AcquisitionRel, Type: "application/x-cbz", Href: "/cbz"},
		{Rel: AcquisitionRel, Type: "application/pdf", Href: "/pdf"},
	}
	if got := pickAcquisition(links).Href; got != "/cbz" {
		t.Errorf("expected CBZ when no EPUB, got %q", got)
	}
}

func TestPickAcquisition_NoneSupported(t *testing.T) {
	links := []link{
		{Rel: AcquisitionRel, Type: "application/x-mobipocket-ebook", Href: "/mobi"},
	}
	if got := pickAcquisition(links).Href; got != "" {
		t.Errorf("expected empty when no preferred format, got %q", got)
	}
}

func TestSanitize_LengthCap(t *testing.T) {
	long := strings.Repeat("A", maxFilenameBytes+50)
	got := sanitize(long)
	if len(got) > maxFilenameBytes {
		t.Errorf("sanitize did not cap length: got %d bytes, want <= %d", len(got), maxFilenameBytes)
	}
	// Ensure the cap doesn't split a multi-byte rune. 4-byte runes ("𝄞"
	// musical symbol) packed to just over the cap must end on a rune
	// boundary.
	multiByte := strings.Repeat("𝄞", (maxFilenameBytes/4)+5)
	got = sanitize(multiByte)
	if len(got) > maxFilenameBytes {
		t.Errorf("multi-byte sanitize exceeded cap: %d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("sanitize produced invalid UTF-8 at cap boundary")
	}
}

func TestSweepStalePartFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "Author", "Book.epub.part")
	fresh := filepath.Join(dir, "Author", "Fresh.epub.part")
	keep := filepath.Join(dir, "Author", "Book.epub")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{stale, fresh, keep} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	sweepStalePartFiles(dir, time.Hour)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale .part not removed: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh .part removed (should remain): %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("non-.part file removed: %v", err)
	}
}

func TestNextLink(t *testing.T) {
	links := []link{
		{Rel: "self", Href: "/page1"},
		{Rel: "next", Href: "/page2"},
	}
	if got := nextLink(links); got != "/page2" {
		t.Errorf("nextLink = %q, want /page2", got)
	}
	if got := nextLink(nil); got != "" {
		t.Errorf("nextLink(nil) = %q, want empty", got)
	}
}

func TestShortID(t *testing.T) {
	a, b := shortID("webdav:x/Book.epub"), shortID("webdav:y/Book.epub")
	if len(a) != 8 || a == b {
		t.Errorf("shortID not 8 distinct hex chars: %q vs %q", a, b)
	}
	if a != shortID("webdav:x/Book.epub") {
		t.Errorf("shortID not deterministic")
	}
}

func TestUnderDir(t *testing.T) {
	cases := []struct {
		dir, path string
		want      bool
	}{
		{"/mnt/ext1/Books/home", "/mnt/ext1/Books/home/Author/Title.epub", true},
		{"/mnt/ext1/Books/home", "/mnt/ext1/Books/HOME/Author/Title.epub", true}, // FAT folds case
		{"/mnt/ext1/Books/home", "/mnt/ext1/Books/home2/Author/Title.epub", false},
		{"/mnt/ext1/Books/home", "/mnt/ext1/Books/nas/Author/Title.epub", false},
		{"/mnt/ext1/Books/home", "/mnt/ext1/Books/home", false}, // the directory itself is not a book
		{"/mnt/ext1/Books/home", "", false},
		{"", "/mnt/ext1/Books/home/Author/Title.epub", false},
	}
	for _, tc := range cases {
		if got := underDir(tc.dir, tc.path); got != tc.want {
			t.Errorf("underDir(%q, %q) = %v, want %v", tc.dir, tc.path, got, tc.want)
		}
	}
}

// A tracked book whose file is gone from the library has to be fetched
// again. Sync skipped on the store row alone, so a user who deleted books
// from the device saw "Skipped N, Downloaded 0" and an empty folder.
func TestSync_RefetchesBookMissingFromDisk(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	src := &fakeSource{books: []Book{makeBook("uuid-a", "Author", "A", "http://x/a")}}
	if res := Sync(context.Background(), src, store, library, nil, SyncOptions{}); res.Downloaded != 1 {
		t.Fatalf("seed sync: %+v", res)
	}
	e, ok := lookup(t, store, "uuid-a")
	if !ok {
		t.Fatal("book not tracked after seed sync")
	}
	if err := os.Remove(e.LocalPath); err != nil {
		t.Fatalf("remove: %v", err)
	}

	res := Sync(context.Background(), src, store, library, nil, SyncOptions{})
	if res.Downloaded != 1 || res.Skipped != 0 {
		t.Errorf("second sync = %+v, want Downloaded 1 / Skipped 0", res)
	}
	if _, err := os.Stat(e.LocalPath); err != nil {
		t.Errorf("book not back on disk: %v", err)
	}
}

// The state DB is shared by every profile and keyed on UUID alone, so a
// second profile with its own library finds rows pointing into the first
// profile's folder. It must download its own copies and leave the other
// profile's files alone.
func TestSync_SecondLibraryGetsItsOwnCopies(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	first := filepath.Join(dir, "Books", "home")
	second := filepath.Join(dir, "Books", "nas")

	// Two WebDAV servers with the same relative path produce the same
	// synthetic UUID (see webdav.go), which is how the two profiles
	// collide in the shared store.
	src := &fakeSource{books: []Book{makeBook("webdav:/Books/A.epub", "Author", "A", "http://one/a")}}
	if res := Sync(context.Background(), src, store, first, nil, SyncOptions{}); res.Downloaded != 1 {
		t.Fatalf("first profile sync: %+v", res)
	}
	firstCopy := filepath.Join(first, "Author", "A.epub")
	if _, err := os.Stat(firstCopy); err != nil {
		t.Fatalf("first profile copy missing: %v", err)
	}

	res := Sync(context.Background(), src, store, second, nil, SyncOptions{})
	if res.Downloaded != 1 || res.Skipped != 0 {
		t.Errorf("second profile sync = %+v, want Downloaded 1 / Skipped 0", res)
	}
	if _, err := os.Stat(filepath.Join(second, "Author", "A.epub")); err != nil {
		t.Errorf("second profile copy missing: %v", err)
	}
	if _, err := os.Stat(firstCopy); err != nil {
		t.Errorf("the other profile's copy was deleted: %v", err)
	}
}

// A second profile that already downloaded its own copy must reuse it on
// the next sync instead of fetching the book again. The store row points
// at whichever profile synced last, so without this the two profiles
// re-download their whole overlap on every switch, forever.
func TestSync_ReusesCopyAlreadyInThisLibrary(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	first := filepath.Join(dir, "Books", "home")
	second := filepath.Join(dir, "Books", "nas")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{books: []Book{makeBookSized("webdav:/Books/A.epub", "A", 0, t0)}}
	if res := Sync(context.Background(), src, store, first, nil, SyncOptions{}); res.Downloaded != 1 {
		t.Fatalf("home sync: %+v", res)
	}
	if res := Sync(context.Background(), src, store, second, nil, SyncOptions{}); res.Downloaded != 1 {
		t.Fatalf("nas sync: %+v", res)
	}

	// Back to the first profile: its copy is untouched on disk, only the
	// store row moved.
	res := Sync(context.Background(), src, store, first, nil, SyncOptions{})
	if res.Skipped != 1 || res.Downloaded != 0 || res.FirstErr != nil {
		t.Errorf("home re-sync = %+v, want Skipped 1 / Downloaded 0", res)
	}
	homeCopy := filepath.Join(first, "Author", "A.epub")
	if e, _ := lookup(t, store, "webdav:/Books/A.epub"); e.LocalPath != homeCopy {
		t.Errorf("row = %q, want it repointed at %q", e.LocalPath, homeCopy)
	}
	// And the plan for that run agrees with what the run did.
	plan, _, err := Plan(context.Background(), src, store, second, SyncOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Unchanged != 1 || len(plan.NewBooks)+len(plan.UpdatedBooks) != 0 {
		t.Errorf("plan for nas = %+v, want the book unchanged", plan)
	}
}

// A file of a different size at the target path is some other book (or a
// truncated download), not this profile's copy, so it is fetched again.
func TestSync_DoesNotAdoptDifferentlySizedFile(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	first := filepath.Join(dir, "Books", "home")
	second := filepath.Join(dir, "Books", "nas")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{books: []Book{makeBookSized("webdav:/Books/A.epub", "A", 0, t0)}}
	if res := Sync(context.Background(), src, store, first, nil, SyncOptions{}); res.Downloaded != 1 {
		t.Fatalf("home sync: %+v", res)
	}
	stub := filepath.Join(second, "Author", "A.epub")
	if err := os.MkdirAll(filepath.Dir(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stub, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := Sync(context.Background(), src, store, second, nil, SyncOptions{}); res.Downloaded != 1 {
		t.Errorf("nas sync = %+v, want the short file replaced by a download", res)
	}
}

// Delete-missing must only propose books from the library being synced.
// The store is shared and UUID-keyed, so an unscoped diff proposes every
// other profile's books and deletes their files.
func TestSync_DeleteMissing_LeavesOtherLibraryAlone(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	home := filepath.Join(dir, "Books", "home")
	nas := filepath.Join(dir, "Books", "nas")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	homeSrc := &fakeSource{books: []Book{makeBookSized("uuid-home", "Home Book", 0, t0)}}
	if res := Sync(context.Background(), homeSrc, store, home, nil, SyncOptions{Scope: "home"}); res.Downloaded != 1 {
		t.Fatalf("home sync: %+v", res)
	}
	homeCopy := filepath.Join(home, "Author", "Home Book.epub")

	// The nas profile syncs a different server, twice: the first run only
	// records its scope, the second passes the scope guard and reaches the
	// delete step.
	nasSrc := &fakeSource{books: []Book{makeBookSized("uuid-nas", "Nas Book", 0, t0)}}
	var asked []LocalBook
	opts := SyncOptions{
		DeleteMissing: true,
		Scope:         "nas",
		Confirm: func(d []LocalBook) bool {
			asked = append(asked, d...)
			return true
		},
	}
	if res := Sync(context.Background(), nasSrc, store, nas, nil, opts); res.Deleted != 0 {
		t.Fatalf("first nas sync = %+v, want the scope guard to hold", res)
	}
	res := Sync(context.Background(), nasSrc, store, nas, nil, opts)
	if res.Deleted != 0 {
		t.Errorf("Deleted = %d, want 0: the home book is not this profile's to delete", res.Deleted)
	}
	if len(asked) != 0 {
		t.Errorf("Confirm asked about %+v, want nothing", asked)
	}
	if _, err := os.Stat(homeCopy); err != nil {
		t.Errorf("the other profile's file was deleted: %v", err)
	}
	if _, exists := lookup(t, store, "uuid-home"); !exists {
		t.Error("the other profile's store row was deleted")
	}

	// A book that really is missing from this library still goes.
	plan, _, err := Plan(context.Background(), &fakeSource{books: []Book{makeBookSized("uuid-other", "Other", 0, t0)}}, store, nas, opts)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Missing) != 1 || plan.Missing[0].UUID != "uuid-nas" {
		t.Errorf("plan.Missing = %+v, want only the nas book", plan.Missing)
	}
}

// Plan and Sync have to agree on what a run will do, so the same
// containment check applies to the pre-flight estimate.
func TestPlan_CountsBookOutsideLibraryAsDownload(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	first := filepath.Join(dir, "Books", "home")
	second := filepath.Join(dir, "Books", "nas")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	book := makeBookSized("webdav:/Books/A.epub", "A", 100, t0)
	src := &fakeSource{books: []Book{book}}
	if res := Sync(context.Background(), src, store, first, nil, SyncOptions{}); res.Downloaded != 1 {
		t.Fatalf("first profile sync: %+v", res)
	}

	plan, _, err := Plan(context.Background(), src, store, second, SyncOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Unchanged != 0 || len(plan.NewBooks)+len(plan.UpdatedBooks) != 1 {
		t.Errorf("plan = %+v, want the book queued for download", plan)
	}
	if plan.DownloadBytes == 0 {
		t.Error("DownloadBytes = 0, want the book's size")
	}
}
