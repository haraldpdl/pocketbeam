package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSource is a Source that returns a canned list of books; Fetch
// streams a synthetic byte payload so Sync's download loop succeeds.
type fakeSource struct {
	books []Book
}

func (f *fakeSource) List(ctx context.Context) ([]Book, error) {
	return f.books, nil
}
func (f *fakeSource) Fetch(ctx context.Context, b Book) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("pretend-ebook")), nil
}

func makeBook(uuid, author, title string, url string) Book {
	return Book{
		UUID:    uuid,
		Title:   title,
		Author:  author,
		Updated: time.Now(),
		URL:     url,
		Format:  "application/epub+zip",
	}
}

// openTempStore is a quick helper around OpenStore that uses a temp file.
func openTempStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	s, err := OpenStore(db)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return s, dir
}

// lookup returns library's tracked entry for uuid via the same per-run
// map Plan and Sync consult; ok is false when the store does not know
// uuid in that library.
func lookup(t *testing.T, s *Store, library, uuid string) (LocalBook, bool) {
	t.Helper()
	entries, err := s.EntriesByUUID(library)
	if err != nil {
		t.Fatalf("EntriesByUUID: %v", err)
	}
	e, ok := entries[uuid]
	return e, ok
}

// cancellingSource pretends to have many books and deliberately blocks
// in Fetch so the context can be cancelled mid-download. Used to verify
// Sync exits promptly once the caller cancels.
type cancellingSource struct {
	books   []Book
	entered chan struct{}
	release chan struct{}
}

func (c *cancellingSource) List(ctx context.Context) ([]Book, error) {
	return c.books, nil
}
func (c *cancellingSource) Fetch(ctx context.Context, b Book) (io.ReadCloser, error) {
	// Signal that the first fetch has started, then block until either
	// the context is cancelled or the test releases us.
	select {
	case c.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return io.NopCloser(strings.NewReader("late")), nil
	}
}

func TestSync_CancellationReturnsQuickly(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	src := &cancellingSource{
		books: []Book{
			makeBook("uuid-a", "Author", "A", "http://x/a"),
			makeBook("uuid-b", "Author", "B", "http://x/b"),
		},
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan SyncResult, 1)
	go func() {
		done <- Sync(ctx, src, store, library, nil, SyncOptions{})
	}()

	select {
	case <-src.entered:
	case <-time.After(time.Second):
		t.Fatal("Sync never called Fetch")
	}
	cancel()

	select {
	case res := <-done:
		if res.FirstErr == nil {
			t.Errorf("expected cancellation error, got %+v", res)
		}
		if res.Downloaded != 0 {
			t.Errorf("Downloaded = %d, want 0", res.Downloaded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Sync did not return within 2s of cancel")
	}
}

func TestSync_DeleteMissing_ConfirmsAndRemoves(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	// First run: two books on the remote.
	srcFull := &fakeSource{
		books: []Book{
			makeBook("uuid-a", "Author", "Keep Me", "http://x/a"),
			makeBook("uuid-b", "Author", "Remove Me", "http://x/b"),
		},
	}
	opts := SyncOptions{Scope: "opds|x|||"}
	res := Sync(context.Background(), srcFull, store, library, nil, opts)
	if res.Downloaded != 2 || res.FirstErr != nil {
		t.Fatalf("first sync: got %+v", res)
	}

	// Second run: book-b gone from the remote. DeleteMissing on, confirm yes.
	srcPartial := &fakeSource{
		books: []Book{
			makeBook("uuid-a", "Author", "Keep Me", "http://x/a"),
		},
	}
	var askedWith []LocalBook
	opts2 := SyncOptions{
		DeleteMissing: true,
		Scope:         "opds|x|||",
		Confirm: func(d []LocalBook) bool {
			askedWith = d
			return true
		},
	}
	res = Sync(context.Background(), srcPartial, store, library, nil, opts2)
	if res.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", res.Deleted)
	}
	if len(askedWith) != 1 || askedWith[0].UUID != "uuid-b" {
		t.Errorf("Confirm called with %+v, want one entry for uuid-b", askedWith)
	}
	// The store should no longer know about uuid-b.
	if _, exists := lookup(t, store, library, "uuid-b"); exists {
		t.Errorf("uuid-b still present in store after delete")
	}
}

func TestSync_DeleteMissing_ScopeChangeSkips(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	srcFull := &fakeSource{
		books: []Book{
			makeBook("uuid-a", "Author", "A", "http://x/a"),
			makeBook("uuid-b", "Author", "B", "http://x/b"),
		},
	}
	res := Sync(context.Background(), srcFull, store, library, nil, SyncOptions{Scope: "scope-one"})
	if res.Downloaded != 2 {
		t.Fatalf("first sync: %+v", res)
	}

	// Scope changes (user switched shelf). Remote now has only one book.
	confirmed := false
	opts := SyncOptions{
		DeleteMissing: true,
		Scope:         "scope-two",
		Confirm: func(d []LocalBook) bool {
			confirmed = true
			return true
		},
	}
	srcOther := &fakeSource{
		books: []Book{makeBook("uuid-c", "Author", "C", "http://x/c")},
	}
	res = Sync(context.Background(), srcOther, store, library, nil, opts)
	if confirmed {
		t.Errorf("Confirm was called despite scope change")
	}
	if res.Deleted != 0 {
		t.Errorf("Deleted = %d, want 0 on scope change", res.Deleted)
	}
	// Next run with the same scope should now proceed with deletion.
	res = Sync(context.Background(), srcOther, store, library, nil, opts)
	if !confirmed {
		t.Errorf("Confirm should have fired once scope stabilised")
	}
	if res.Deleted == 0 {
		t.Errorf("Expected deletions on second stable-scope run")
	}
}

func TestSync_DeleteMissing_EmptyRemoteSkips(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	// Seed one book.
	srcFull := &fakeSource{books: []Book{makeBook("uuid-a", "Author", "A", "http://x/a")}}
	_ = Sync(context.Background(), srcFull, store, library, nil, SyncOptions{Scope: "s1"})

	empty := &fakeSource{books: nil}
	called := false
	res := Sync(context.Background(), empty, store, library, nil, SyncOptions{
		DeleteMissing: true,
		Scope:         "s1",
		Confirm: func(d []LocalBook) bool {
			called = true
			return true
		},
	})
	if called {
		t.Errorf("Confirm should not fire for empty remote")
	}
	if res.Deleted != 0 {
		t.Errorf("Deleted = %d, want 0 on empty remote", res.Deleted)
	}
}

func TestSync_DeleteMissing_ConfirmDecline(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	srcFull := &fakeSource{
		books: []Book{
			makeBook("uuid-a", "Author", "A", "http://x/a"),
			makeBook("uuid-b", "Author", "B", "http://x/b"),
		},
	}
	_ = Sync(context.Background(), srcFull, store, library, nil, SyncOptions{Scope: "s"})

	srcPartial := &fakeSource{books: []Book{makeBook("uuid-a", "Author", "A", "http://x/a")}}
	res := Sync(context.Background(), srcPartial, store, library, nil, SyncOptions{
		DeleteMissing: true,
		Scope:         "s",
		Confirm:       func(d []LocalBook) bool { return false },
	})
	if res.Deleted != 0 {
		t.Errorf("Deleted = %d, want 0 when declined", res.Deleted)
	}
	_, exists := lookup(t, store, library, "uuid-b")
	if !exists {
		t.Errorf("uuid-b should still be present after decline")
	}
}

func TestSync_DeleteMissing_RemovesFileOnDisk(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	srcFull := &fakeSource{
		books: []Book{
			makeBook("uuid-a", "Author", "Keeper", "http://x/a"),
			makeBook("uuid-b", "Author", "Goner", "http://x/b"),
		},
	}
	_ = Sync(context.Background(), srcFull, store, library, nil, SyncOptions{Scope: "s"})
	goner := filepath.Join(library, "Author", "Goner.epub")
	if _, err := os.Stat(goner); err != nil {
		t.Fatalf("expected file at %s: %v", goner, err)
	}

	srcPartial := &fakeSource{books: []Book{makeBook("uuid-a", "Author", "Keeper", "http://x/a")}}
	_ = Sync(context.Background(), srcPartial, store, library, nil, SyncOptions{
		DeleteMissing: true,
		Scope:         "s",
		Confirm:       func(d []LocalBook) bool { return true },
	})
	if _, err := os.Stat(goner); !os.IsNotExist(err) {
		t.Errorf("file %s not deleted: err=%v", goner, err)
	}
	// Keeper stays.
	keeper := filepath.Join(library, "Author", "Keeper.epub")
	if _, err := os.Stat(keeper); err != nil {
		t.Errorf("keeper missing: %v", err)
	}
}

// Two different books that sanitize to the same author and title must not
// share one file: the second gets a suffixed filename, and deleting either
// leaves the other's file untouched.
func TestSync_SameTitleDifferentBooks_KeepSeparateFiles(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	src := &fakeSource{
		books: []Book{
			makeBook("uuid-a", "Author", "Same Title", "http://x/a"),
			makeBook("uuid-b", "Author", "Same Title", "http://x/b"),
		},
	}
	res := Sync(context.Background(), src, store, library, nil, SyncOptions{Scope: "s"})
	if res.Downloaded != 2 || res.FirstErr != nil {
		t.Fatalf("first sync: %+v", res)
	}
	entryA, _ := lookup(t, store, library, "uuid-a")
	entryB, _ := lookup(t, store, library, "uuid-b")
	pathA, pathB := entryA.LocalPath, entryB.LocalPath
	if pathA != filepath.Join(library, "Author", "Same Title.epub") {
		t.Errorf("first book path = %q, want plain name", pathA)
	}
	wantB := filepath.Join(library, "Author", "Same Title ["+shortID("uuid-b")+"].epub")
	if pathB != wantB {
		t.Errorf("second book path = %q, want %q", pathB, wantB)
	}
	for _, p := range []string{pathA, pathB} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing file %s: %v", p, err)
		}
	}

	// Re-downloading a book over its own path is not a collision: the
	// plain name stays plain and the suffixed name stays stable.
	for i := range src.books {
		src.books[i].Updated = src.books[i].Updated.Add(time.Hour)
	}
	res = Sync(context.Background(), src, store, library, nil, SyncOptions{Scope: "s"})
	if res.Downloaded != 2 || res.FirstErr != nil {
		t.Fatalf("second sync: %+v", res)
	}
	if e, _ := lookup(t, store, library, "uuid-a"); e.LocalPath != pathA {
		t.Errorf("uuid-a moved to %q on re-download", e.LocalPath)
	}
	if e, _ := lookup(t, store, library, "uuid-b"); e.LocalPath != pathB {
		t.Errorf("uuid-b moved to %q on re-download", e.LocalPath)
	}

	// Removing the first book from the remote deletes only its file.
	src.books = src.books[1:]
	res = Sync(context.Background(), src, store, library, nil, SyncOptions{
		DeleteMissing: true,
		Scope:         "s",
		Confirm:       func(d []LocalBook) bool { return true },
	})
	if res.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", res.Deleted)
	}
	if _, err := os.Stat(pathA); !os.IsNotExist(err) {
		t.Errorf("deleted book's file still present: err=%v", err)
	}
	if _, err := os.Stat(pathB); err != nil {
		t.Errorf("surviving book's file was removed: %v", err)
	}
}

// The library lives on a FAT volume, so titles differing only in case
// would still land on one file; the store's ownership check must catch that.
func TestSync_CaseOnlyTitleDifference_IsACollision(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")

	src := &fakeSource{
		books: []Book{
			makeBook("uuid-a", "Author", "Title", "http://x/a"),
			makeBook("uuid-b", "Author", "TITLE", "http://x/b"),
		},
	}
	res := Sync(context.Background(), src, store, library, nil, SyncOptions{})
	if res.Downloaded != 2 || res.FirstErr != nil {
		t.Fatalf("sync: %+v", res)
	}
	entryB, _ := lookup(t, store, library, "uuid-b")
	pathB := entryB.LocalPath
	if want := filepath.Join(library, "Author", "TITLE ["+shortID("uuid-b")+"].epub"); pathB != want {
		t.Errorf("uuid-b path = %q, want %q", pathB, want)
	}
}

// Stores written before filename disambiguation already hold two UUIDs at
// one path. When only one of those books updates, it must move to a tagged
// path while the other book's file (the shared one) stays in place.
func TestSync_LegacySharedPath_UpdateKeepsOtherBooksFile(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")
	shared := filepath.Join(library, "Author", "Same Title.epub")

	src := &fakeSource{
		books: []Book{
			makeBook("uuid-a", "Author", "Same Title", "http://x/a"),
			makeBook("uuid-b", "Author", "Same Title", "http://x/b"),
		},
	}
	if err := os.MkdirAll(filepath.Dir(shared), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The store keeps whole seconds; seed with the same precision so only
	// the deliberate bump below reads as an update.
	for i, b := range src.books {
		src.books[i].Updated = b.Updated.Truncate(time.Second)
		if err := store.Upsert(library, "", src.books[i], shared, 6, false); err != nil {
			t.Fatal(err)
		}
	}

	src.books[1].Updated = src.books[1].Updated.Add(time.Hour)
	res := Sync(context.Background(), src, store, library, nil, SyncOptions{Scope: "s"})
	if res.Downloaded != 1 || res.Skipped != 1 || res.FirstErr != nil {
		t.Fatalf("sync: %+v", res)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Errorf("unchanged book's file was removed: %v", err)
	}
	if e, _ := lookup(t, store, library, "uuid-a"); e.LocalPath != shared {
		t.Errorf("uuid-a path = %q, want %q", e.LocalPath, shared)
	}
	wantB := filepath.Join(library, "Author", "Same Title ["+shortID("uuid-b")+"].epub")
	if e, _ := lookup(t, store, library, "uuid-b"); e.LocalPath != wantB {
		t.Errorf("uuid-b path = %q, want %q", e.LocalPath, wantB)
	}
	if _, err := os.Stat(wantB); err != nil {
		t.Errorf("updated book's file missing: %v", err)
	}
}

// The same legacy layout under delete-missing: dropping one of the two
// books from the remote must forget its row but leave the shared file in
// place, since it is the surviving book's only copy.
func TestSync_LegacySharedPath_DeleteKeepsOtherBooksFile(t *testing.T) {
	store, dir := openTempStore(t)
	defer store.Close()
	library := filepath.Join(dir, "lib")
	shared := filepath.Join(library, "Author", "Same Title.epub")

	books := []Book{
		makeBook("uuid-a", "Author", "Same Title", "http://x/a"),
		makeBook("uuid-b", "Author", "Same Title", "http://x/b"),
	}
	if err := os.MkdirAll(filepath.Dir(shared), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, b := range books {
		books[i].Updated = b.Updated.Truncate(time.Second)
		if err := store.Upsert(library, "", books[i], shared, 6, false); err != nil {
			t.Fatal(err)
		}
	}
	const scope = "s"
	if err := store.SetMeta(lastScopeKey(""), scope); err != nil {
		t.Fatal(err)
	}

	src := &fakeSource{books: books[1:]}
	res := Sync(context.Background(), src, store, library, nil, SyncOptions{
		DeleteMissing: true,
		Scope:         scope,
		Confirm:       func([]LocalBook) bool { return true },
	})
	if res.Deleted != 1 || res.Skipped != 1 || res.FirstErr != nil {
		t.Fatalf("sync: %+v", res)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Errorf("surviving book uuid-b lost its only file: %v", err)
	}
	if _, exists := lookup(t, store, library, "uuid-a"); exists {
		t.Errorf("uuid-a still present in store after delete")
	}
	if e, exists := lookup(t, store, library, "uuid-b"); !exists || e.LocalPath != shared {
		t.Errorf("uuid-b entry = (%q, exists=%v), want (%q, true)", e.LocalPath, exists, shared)
	}
}
