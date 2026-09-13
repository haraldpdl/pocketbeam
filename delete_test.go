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
	_, _, _, exists, err := store.LocalEntry("uuid-b")
	if err != nil {
		t.Fatalf("LocalEntry: %v", err)
	}
	if exists {
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
	_, _, _, exists, _ := store.LocalEntry("uuid-b")
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
	_, pathA, _, _, _ := store.LocalEntry("uuid-a")
	_, pathB, _, _, _ := store.LocalEntry("uuid-b")
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
	if _, p, _, _, _ := store.LocalEntry("uuid-a"); p != pathA {
		t.Errorf("uuid-a moved to %q on re-download", p)
	}
	if _, p, _, _, _ := store.LocalEntry("uuid-b"); p != pathB {
		t.Errorf("uuid-b moved to %q on re-download", p)
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
	_, pathB, _, _, _ := store.LocalEntry("uuid-b")
	if want := filepath.Join(library, "Author", "TITLE ["+shortID("uuid-b")+"].epub"); pathB != want {
		t.Errorf("uuid-b path = %q, want %q", pathB, want)
	}
}
