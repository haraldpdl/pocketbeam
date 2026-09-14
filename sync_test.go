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
	e, ok := lookup(t, store, library, "uuid-a")
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

// Two profiles whose servers hand out one identity each track their own
// copy: the second profile downloads into its own library and leaves the
// first profile's file alone.
func TestSync_SecondLibraryGetsItsOwnCopies(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	first := filepath.Join(dir, "Books", "home")
	second := filepath.Join(dir, "Books", "nas")

	// Two WebDAV servers with the same relative path produce the same
	// synthetic UUID (see webdav.go), which is how two profiles end up
	// sharing a book identity in one state DB.
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

// Two profiles that share an identity each keep their own row, so
// switching back and forth skips both copies instead of re-downloading
// the overlap on every switch.
func TestSync_EachLibraryKeepsItsOwnRow(t *testing.T) {
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

	// Back to the first profile: nothing to fetch, and both rows still
	// point at their own library's copy.
	res := Sync(context.Background(), src, store, first, nil, SyncOptions{})
	if res.Skipped != 1 || res.Downloaded != 0 || res.FirstErr != nil {
		t.Errorf("home re-sync = %+v, want Skipped 1 / Downloaded 0", res)
	}
	for _, library := range []string{first, second} {
		want := filepath.Join(library, "Author", "A.epub")
		if e, ok := lookup(t, store, library, "webdav:/Books/A.epub"); !ok || e.LocalPath != want {
			t.Errorf("row for %q = (%q, ok=%v), want %q", library, e.LocalPath, ok, want)
		}
		if n, err := store.BookCount(library); err != nil || n != 1 {
			t.Errorf("BookCount(%q) = %d (err %v), want 1", library, n, err)
		}
	}
	// And the plan for the second profile agrees with what a run would do.
	plan, _, err := Plan(context.Background(), src, store, second, SyncOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Unchanged != 1 || len(plan.NewBooks)+len(plan.UpdatedBooks) != 0 {
		t.Errorf("plan for nas = %+v, want the book unchanged", plan)
	}
}

// A book two profiles share stays deletable from each of them: the
// profile it disappears from prunes its own copy and leaves the other
// profile's file and row alone.
func TestSync_DeleteMissing_SharedIdentityPrunesOnlyThisLibrary(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	home := filepath.Join(dir, "Books", "home")
	nas := filepath.Join(dir, "Books", "nas")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const uuid = "webdav:/Books/A.epub"
	shared := &fakeSource{books: []Book{makeBookSized(uuid, "A", 0, t0)}}
	for _, library := range []string{home, nas, home} {
		if res := Sync(context.Background(), shared, store, library, nil, SyncOptions{Scope: "s", Profile: "p"}); res.FirstErr != nil {
			t.Fatalf("seed sync of %q: %v", library, res.FirstErr)
		}
	}

	// The book is gone from the nas server. Its copy there is the nas
	// profile's to delete.
	var asked []LocalBook
	res := Sync(context.Background(), &fakeSource{books: []Book{makeBookSized("other", "Other", 0, t0)}}, store, nas, nil, SyncOptions{
		DeleteMissing: true,
		Scope:         "s",
		Profile:       "p",
		Confirm: func(d []LocalBook) bool {
			asked = append(asked, d...)
			return true
		},
	})
	if res.Deleted != 1 || len(asked) != 1 || asked[0].UUID != uuid {
		t.Fatalf("nas sync = %+v, asked about %+v, want the shared book deleted", res, asked)
	}
	if _, err := os.Stat(filepath.Join(nas, "Author", "A.epub")); !os.IsNotExist(err) {
		t.Errorf("nas copy still on disk: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "Author", "A.epub")); err != nil {
		t.Errorf("home copy was deleted: %v", err)
	}
	if _, ok := lookup(t, store, home, uuid); !ok {
		t.Error("home row was deleted with the nas one")
	}
}

// Profiles remember their own last scope. Alternating between two of them
// used to overwrite one global memory, so the stored scope differed from
// the current one on nearly every run and the delete step never ran.
func TestSync_DeleteMissing_AlternatingProfilesKeepTheirScope(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	home := filepath.Join(dir, "Books", "home")
	nas := filepath.Join(dir, "Books", "nas")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	homeSrc := &fakeSource{books: []Book{
		makeBookSized("home-a", "A", 0, t0),
		makeBookSized("home-b", "B", 0, t0),
	}}
	nasSrc := &fakeSource{books: []Book{makeBookSized("nas-a", "A", 0, t0)}}
	homeOpts := SyncOptions{DeleteMissing: true, Scope: "home-scope", Profile: "home", Confirm: func([]LocalBook) bool { return true }}
	nasOpts := SyncOptions{DeleteMissing: true, Scope: "nas-scope", Profile: "nas", Confirm: func([]LocalBook) bool { return true }}

	// home records its scope, then nas runs in between.
	if res := Sync(context.Background(), homeSrc, store, home, nil, homeOpts); res.FirstErr != nil {
		t.Fatalf("home seed: %v", res.FirstErr)
	}
	if res := Sync(context.Background(), nasSrc, store, nas, nil, nasOpts); res.FirstErr != nil {
		t.Fatalf("nas seed: %v", res.FirstErr)
	}

	// home again, with one book gone from its server: the scope it stored
	// is still its own, so the delete step runs.
	homeSrc.books = homeSrc.books[:1]
	res := Sync(context.Background(), homeSrc, store, home, nil, homeOpts)
	if res.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 after the other profile synced in between", res.Deleted)
	}
	if _, ok := lookup(t, store, home, "home-b"); ok {
		t.Error("home-b still tracked after the delete step")
	}
}

// Installs upgrading from the single global last-scope key must not lose
// the delete step on their first run under per-profile keys.
func TestSync_DeleteMissing_AdoptsGlobalLastScope(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{books: []Book{
		makeBookSized("a", "A", 0, t0),
		makeBookSized("b", "B", 0, t0),
	}}
	// A version that knew nothing about per-profile keys seeded the store
	// and wrote the global one.
	if res := Sync(context.Background(), src, store, library, nil, SyncOptions{Scope: "s"}); res.FirstErr != nil {
		t.Fatalf("seed: %v", res.FirstErr)
	}
	if v, ok, _ := store.GetMeta(metaLastScope); !ok || v != "s" {
		t.Fatalf("global last-scope key = (%q, %v), want the seed's scope", v, ok)
	}

	src.books = src.books[:1]
	res := Sync(context.Background(), src, store, library, nil, SyncOptions{
		DeleteMissing: true,
		Scope:         "s",
		Profile:       "only",
		Confirm:       func([]LocalBook) bool { return true },
	})
	if res.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 from the global key fallback", res.Deleted)
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
	if _, exists := lookup(t, store, home, "uuid-home"); !exists {
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

// seedFile writes a stand-in book file, creating its directory.
func seedFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("pretend-ebook"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Every released version derived the library folder from the backend, so
// an install upgrading from one has all its OPDS profiles in Books/CWA.
// The migration keys those rows under that one folder and knows no
// profile for any of them, so a diff that only bounds deletion by the
// folder proposes the other profile's whole library. The book has to be
// this profile's before it can be deleted here.
func TestSync_DeleteMissing_SharedLegacyFolderKeepsOtherProfilesBooks(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	library := filepath.Join(dir, "Books", "CWA")

	homeA := filepath.Join(library, "Author", "Home A.epub")
	homeB := filepath.Join(library, "Author", "Home B.epub")
	nasA := filepath.Join(library, "Author", "Nas A.epub")
	writeLegacyBooks(t, dbPath, [][2]string{
		{"home-a", homeA},
		{"home-b", homeB},
		{"nas-a", nasA},
	})
	for _, p := range []string{homeA, homeB, nasA} {
		seedFile(t, p)
	}

	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	// The pre-upgrade version wrote one global last-scope key, and home
	// is the profile that wrote it last, so the scope guard opens on
	// home's very first run under the new schema.
	if err := store.SetMeta(metaLastScope, "home-scope"); err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	homeSrc := &fakeSource{books: []Book{
		makeBookSized("home-a", "Home A", 0, t0),
		makeBookSized("home-b", "Home B", 0, t0),
	}}
	var asked []LocalBook
	homeOpts := SyncOptions{
		DeleteMissing: true,
		Scope:         "home-scope",
		Profile:       "home",
		LibraryShared: true,
		Confirm: func(d []LocalBook) bool {
			asked = append(asked, d...)
			return true
		},
	}

	res := Sync(context.Background(), homeSrc, store, library, nil, homeOpts)
	if res.Deleted != 0 || len(asked) != 0 {
		t.Errorf("home sync = %+v, asked about %+v; the nas book is not home's to delete", res, asked)
	}
	if _, err := os.Stat(nasA); err != nil {
		t.Errorf("the other profile's file was deleted: %v", err)
	}
	if _, ok := lookup(t, store, library, "nas-a"); !ok {
		t.Error("the other profile's row was deleted")
	}

	// The books home's server still lists are home's from that run on, so
	// one going missing is still deleted from the shared folder.
	homeSrc.books = homeSrc.books[:1]
	res = Sync(context.Background(), homeSrc, store, library, nil, homeOpts)
	if res.Deleted != 1 || len(asked) != 1 || asked[0].UUID != "home-b" {
		t.Fatalf("second home sync = %+v, asked about %+v; want only home-b deleted", res, asked)
	}
	if _, err := os.Stat(homeB); !os.IsNotExist(err) {
		t.Errorf("home-b still on disk: err=%v", err)
	}
	if _, err := os.Stat(nasA); err != nil {
		t.Errorf("the other profile's file was deleted on the second run: %v", err)
	}

	// nas claims its own book by syncing it, and home still cannot touch
	// it once it disappears from the nas server.
	nasSrc := &fakeSource{books: []Book{makeBookSized("nas-a", "Nas A", 0, t0)}}
	nasOpts := SyncOptions{Scope: "nas-scope", Profile: "nas", LibraryShared: true}
	if res := Sync(context.Background(), nasSrc, store, library, nil, nasOpts); res.FirstErr != nil {
		t.Fatalf("nas sync: %v", res.FirstErr)
	}
	e, ok := lookup(t, store, library, "nas-a")
	if !ok || e.Profile != "nas" {
		t.Errorf("nas-a row = (%q, ok=%v), want it claimed by nas", e.Profile, ok)
	}
	asked = nil
	if res := Sync(context.Background(), homeSrc, store, library, nil, homeOpts); res.Deleted != 0 || len(asked) != 0 {
		t.Errorf("home sync after the claim = %+v, asked about %+v, want nothing", res, asked)
	}
	if _, err := os.Stat(nasA); err != nil {
		t.Errorf("the claimed file was deleted by the other profile: %v", err)
	}
}

// A renamed book's old file is tidied up after the new one lands, which
// is as irreversible as the delete step and carries the same bound: a
// recorded path outside the library (a hand-edited state DB, a row a
// future tool writes) names a file this run has no claim on.
func TestSync_RenameLeavesFileOutsideLibraryAlone(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "Books", "home")
	outside := filepath.Join(dir, "elsewhere", "Author", "Old.epub")
	seedFile(t, outside)

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.Upsert(library, "home", makeBookSized("u1", "Old", 0, t0), outside, 13); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// A newer edition under a new title: the download lands inside the
	// library and the old path is offered for cleanup.
	src := &fakeSource{books: []Book{makeBookSized("u1", "New", 0, t0.Add(time.Hour))}}
	res := Sync(context.Background(), src, store, library, nil, SyncOptions{Profile: "home"})
	if res.Downloaded != 1 || res.FirstErr != nil {
		t.Fatalf("sync = %+v, want the renamed book downloaded", res)
	}
	if _, err := os.Stat(filepath.Join(library, "Author", "New.epub")); err != nil {
		t.Fatalf("new copy missing: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside the library was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(outside)); err != nil {
		t.Errorf("directory outside the library was removed: %v", err)
	}
}
