package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_UpsertAndEntriesByUUID(t *testing.T) {
	s, _ := openTempStore(t)
	defer s.Close()

	if _, exists := lookup(t, s, "/lib", "nope"); exists {
		t.Fatal("lookup(unknown) = exists, want absent")
	}

	b := makeBook("u1", "Author", "Title", "http://x/1")
	b.Updated = time.Unix(1_700_000_000, 0)
	b.Size = 500
	if err := s.Upsert("/lib", "home", b, "/lib/Author/Title.epub", 1234); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	e, exists := lookup(t, s, "/lib", "u1")
	if !exists {
		t.Fatal("u1 missing after Upsert")
	}
	if !e.Updated.Equal(b.Updated) || e.LocalPath != "/lib/Author/Title.epub" {
		t.Errorf("entry = (%v, %q), want (%v, %q)", e.Updated, e.LocalPath, b.Updated, "/lib/Author/Title.epub")
	}
	if e.Title != "Title" || e.Author != "Author" {
		t.Errorf("entry display fields = (%q, %q), want (Title, Author)", e.Title, e.Author)
	}
	// The measured transfer size wins over the advertised one.
	if e.Size != 1234 {
		t.Errorf("size = %d, want 1234 (actual bytes read)", e.Size)
	}

	// Re-upserting the same UUID updates in place rather than duplicating.
	b.Title = "Renamed"
	b.Updated = b.Updated.Add(time.Hour)
	if err := s.Upsert("/lib", "home", b, "/lib/Author/Renamed.epub", 0); err != nil {
		t.Fatalf("Upsert(again): %v", err)
	}
	if n, _ := s.BookCount("/lib"); n != 1 {
		t.Errorf("BookCount = %d, want 1 after re-upsert", n)
	}
	e, _ = lookup(t, s, "/lib", "u1")
	if !e.Updated.Equal(b.Updated) || e.LocalPath != "/lib/Author/Renamed.epub" {
		t.Errorf("after update: (%v, %q)", e.Updated, e.LocalPath)
	}
	// No measured size falls back to the server-advertised one.
	if e.Size != 500 {
		t.Errorf("size = %d, want 500 (advertised fallback)", e.Size)
	}

	// Neither measured nor advertised: stored as 0 (unknown), never negative.
	b.Size = -7
	if err := s.Upsert("/lib", "home", b, e.LocalPath, -1); err != nil {
		t.Fatalf("Upsert(negative): %v", err)
	}
	if e, _ = lookup(t, s, "/lib", "u1"); e.Size != 0 {
		t.Errorf("size = %d, want 0 for unknown", e.Size)
	}
}

func TestStore_AllEntriesAndDelete(t *testing.T) {
	s, _ := openTempStore(t)
	defer s.Close()

	for _, id := range []string{"a", "b"} {
		if err := s.Upsert("/lib", "home", makeBook(id, "Au", "T"+id, ""), "/lib/"+id, 10); err != nil {
			t.Fatalf("Upsert(%s): %v", id, err)
		}
	}
	all, err := s.AllEntries("/lib")
	if err != nil || len(all) != 2 {
		t.Fatalf("AllEntries = %d entries, err=%v; want 2", len(all), err)
	}
	for _, e := range all {
		if e.Author != "Au" || e.LocalPath != "/lib/"+e.UUID || e.Size != 10 || e.Title != "T"+e.UUID {
			t.Errorf("entry %+v carries wrong display/path/size fields", e)
		}
	}
	if err := s.Delete("/lib", "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, exists := lookup(t, s, "/lib", "a"); exists {
		t.Error("entry a still present after Delete")
	}
	if n, _ := s.BookCount("/lib"); n != 1 {
		t.Errorf("BookCount = %d, want 1", n)
	}
	// Deleting an unknown UUID is a no-op, not an error.
	if err := s.Delete("/lib", "missing"); err != nil {
		t.Errorf("Delete(missing) = %v, want nil", err)
	}
}

func TestStore_LastSyncRoundTrip(t *testing.T) {
	s, _ := openTempStore(t)
	defer s.Close()

	if _, ok, err := s.LastSync(); err != nil || ok {
		t.Fatalf("LastSync on fresh store = ok=%v err=%v, want ok=false", ok, err)
	}
	want := SyncSummary{At: time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC), Downloaded: 3, Skipped: 5, Failed: 1}
	if err := s.SetLastSync(want); err != nil {
		t.Fatalf("SetLastSync: %v", err)
	}
	got, ok, err := s.LastSync()
	if err != nil || !ok {
		t.Fatalf("LastSync = ok=%v err=%v", ok, err)
	}
	if !got.At.Equal(want.At) || got.Downloaded != 3 || got.Skipped != 5 || got.Failed != 1 {
		t.Errorf("LastSync = %+v, want %+v", got, want)
	}
	// A second run replaces the first (single meta row, no history).
	want.Downloaded = 0
	if err := s.SetLastSync(want); err != nil {
		t.Fatalf("SetLastSync(again): %v", err)
	}
	if got, _, _ = s.LastSync(); got.Downloaded != 0 {
		t.Errorf("Downloaded = %d after overwrite, want 0", got.Downloaded)
	}
}

func TestStore_LastSyncRejectsCorruptMeta(t *testing.T) {
	s, _ := openTempStore(t)
	defer s.Close()
	if err := s.SetMeta("last_sync", "{not json"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if _, ok, err := s.LastSync(); err == nil || ok {
		t.Errorf("LastSync on corrupt meta = ok=%v err=%v, want an error", ok, err)
	}
}

func TestStore_Meta(t *testing.T) {
	s, _ := openTempStore(t)
	defer s.Close()

	if v, ok, err := s.GetMeta("scope"); err != nil || ok || v != "" {
		t.Fatalf("GetMeta(unset) = (%q, %v, %v), want (\"\", false, nil)", v, ok, err)
	}
	if err := s.SetMeta("scope", "opds:/opds/shelf/1"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := s.SetMeta("scope", "webdav:/Books"); err != nil {
		t.Fatalf("SetMeta(overwrite): %v", err)
	}
	if v, ok, _ := s.GetMeta("scope"); !ok || v != "webdav:/Books" {
		t.Errorf("GetMeta = (%q, %v), want the overwritten value", v, ok)
	}
}

// writeLegacyBooks creates a state DB with the pre-migration schema (no
// size column, UUID as the whole primary key) holding the given rows,
// each {uuid, local_path}.
func writeLegacyBooks(t *testing.T, path string, rows [][2]string) {
	t.Helper()
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE books (
		uuid TEXT PRIMARY KEY, title TEXT NOT NULL, author TEXT NOT NULL,
		updated INTEGER NOT NULL, local_path TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := raw.Exec(`INSERT INTO books VALUES (?, 'Old Title', 'Old Author', 42, ?)`, r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
}

// Installs from before the size column exist in the wild; OpenStore must
// add the column without touching existing rows, and reopening an
// already-migrated database must stay idempotent.
func TestOpenStore_MigratesLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	const local = "/lib/Old Author/Old Title.epub"
	writeLegacyBooks(t, path, [][2]string{{"old", local}})

	for pass := 1; pass <= 2; pass++ {
		s, err := OpenStore(path)
		if err != nil {
			t.Fatalf("OpenStore pass %d: %v", pass, err)
		}
		e, exists := lookup(t, s, "/lib", "old")
		if !exists {
			t.Fatalf("pass %d: legacy row missing", pass)
		}
		if e.Updated.Unix() != 42 || e.LocalPath != local || e.Size != 0 {
			t.Errorf("pass %d: legacy row = (%d, %q, %d), want (42, %q, 0)", pass, e.Updated.Unix(), e.LocalPath, e.Size, local)
		}
		if _, _, err := s.GetMeta("anything"); err != nil {
			t.Errorf("pass %d: meta table missing after migration: %v", pass, err)
		}
		s.Close()
	}
}

// Rows written under the UUID-only key describe whichever library
// downloaded them last, so the migration has to hand each row to the
// library its file sits in.
func TestOpenStore_MigrationKeysRowsByLibrary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	writeLegacyBooks(t, path, [][2]string{
		{"home-only", "/Books/home/Author/A.epub"},
		{"nas-only", "/Books/nas/Author/B.epub"},
		// The shared identity, pointing at whichever library synced last.
		{"webdav:/C.epub", "/Books/nas/Author/C.epub"},
		// Not a <library>/<author>/<file> path: no profile can claim it.
		{"stray", "/loose.epub"},
	})

	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	for library, want := range map[string][]string{
		"/Books/home": {"home-only"},
		"/Books/nas":  {"nas-only", "webdav:/C.epub"},
	} {
		entries, err := s.EntriesByUUID(library)
		if err != nil {
			t.Fatalf("EntriesByUUID(%q): %v", library, err)
		}
		if len(entries) != len(want) {
			t.Errorf("%q tracks %d rows, want %d", library, len(entries), len(want))
		}
		for _, uuid := range want {
			if _, ok := entries[uuid]; !ok {
				t.Errorf("%q lost row %q", library, uuid)
			}
		}
	}
	if n, err := s.BookCount("/Books/home"); err != nil || n != 1 {
		t.Errorf("BookCount(/Books/home) = %d (err %v), want 1", n, err)
	}
}

// The CLI and the UI can run against the same state file at once. A write
// that meets another process's lock must wait for it rather than fail on
// the spot with "database is locked".
func TestOpenStore_WaitsForForeignLock(t *testing.T) {
	s, dir := openTempStore(t)
	defer s.Close()
	if s.db.Stats().MaxOpenConnections != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", s.db.Stats().MaxOpenConnections)
	}

	// A second handle without busy timeout stands in for the other process
	// and holds the write lock via an open IMMEDIATE transaction.
	other, err := sql.Open("sqlite3", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	conn, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Upsert("/lib", "home", makeBook("u1", "Au", "T", ""), "/lib/Au/T.epub", 1) }()

	// Without the busy timeout Upsert returns "database is locked" at once;
	// with it the call is still pending when the lock is released.
	select {
	case err := <-done:
		t.Fatalf("Upsert returned %v while the lock was held, want it to wait", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Upsert after lock release: %v", err)
	}
	if _, exists := lookup(t, s, "/lib", "u1"); !exists {
		t.Error("u1 missing after the waited write")
	}
}

func TestOpenStore_UnwritablePath(t *testing.T) {
	if _, err := OpenStore(filepath.Join(t.TempDir(), "missing-dir", "state.db")); err == nil {
		t.Error("OpenStore into a missing directory should fail")
	}
}

func TestStore_OtherOwner(t *testing.T) {
	s, _ := openTempStore(t)
	defer s.Close()

	if err := s.Upsert("/lib", "home", makeBook("u1", "Au", "T", ""), "/lib/Au/T.epub", 1); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ path, except, want string }{
		{"/lib/Au/T.epub", "", "u1"},
		{"/lib/Au/t.EPUB", "", "u1"}, // FAT is case-insensitive
		{"/lib/Au/T.epub", "u1", ""}, // the asking book is not "another" owner
		{"/lib/Au/Other.epub", "", ""},
	}
	for _, tc := range cases {
		got, err := s.OtherOwner("/lib", tc.path, tc.except)
		if err != nil {
			t.Fatalf("OtherOwner(%q, %q): %v", tc.path, tc.except, err)
		}
		if got != tc.want {
			t.Errorf("OtherOwner(%q, %q) = %q, want %q", tc.path, tc.except, got, tc.want)
		}
	}

	// Stores from before filename disambiguation hold several UUIDs at one
	// path; excluding one must still surface the other.
	if err := s.Upsert("/lib", "home", makeBook("u2", "Au", "T", ""), "/lib/Au/T.epub", 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.OtherOwner("/lib", "/lib/Au/T.epub", "u2"); got != "u1" {
		t.Errorf("OtherOwner with two rows, except u2 = %q, want u1", got)
	}
	if got, _ := s.OtherOwner("/lib", "/lib/Au/T.epub", "u1"); got != "u2" {
		t.Errorf("OtherOwner with two rows, except u1 = %q, want u2", got)
	}
}

// The state DB is shared by every profile, so the main screen's count has
// to be restricted to the library of the profile it is showing.
func TestStore_BookCountScopedToLibrary(t *testing.T) {
	s, dir := openTempStore(t)
	defer s.Close()
	home := filepath.Join(dir, "Books", "home")
	nas := filepath.Join(dir, "Books", "nas")

	seed := []struct {
		library, uuid string
	}{
		{home, "a"},
		{home, "b"},
		{nas, "c"},
	}
	for _, e := range seed {
		path := filepath.Join(e.library, "Author", e.uuid+".epub")
		if err := s.Upsert(e.library, "home", Book{UUID: e.uuid, Title: e.uuid, Author: "Author", Updated: time.Unix(0, 0)}, path, 1); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	for _, tc := range []struct {
		library string
		want    int
	}{
		{home, 2},
		{nas, 1},
		{filepath.Join(dir, "Books", "other"), 0},
		{home + string(filepath.Separator), 2},   // hand-edited trailing slash
		{filepath.Join(dir, "Books", "HOME"), 2}, // one folder on FAT
	} {
		got, err := s.BookCount(tc.library)
		if err != nil {
			t.Fatalf("BookCount(%q): %v", tc.library, err)
		}
		if got != tc.want {
			t.Errorf("BookCount(%q) = %d, want %d", tc.library, got, tc.want)
		}
	}
}

// Rows record the profile that downloaded them so the delete step can
// tell two profiles' books apart inside one library folder. Migrated
// rows have none until a sync claims them, and a claim is one-way.
func TestStore_ProfileOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	const local = "/Books/CWA/Old Author/Old Title.epub"
	writeLegacyBooks(t, path, [][2]string{{"migrated", local}})

	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	if e, ok := lookup(t, s, "/Books/CWA", "migrated"); !ok || e.Profile != "" {
		t.Fatalf("migrated row = (%q, ok=%v), want no profile", e.Profile, ok)
	}
	if err := s.Claim("/Books/CWA", "migrated", "home"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if e, _ := lookup(t, s, "/Books/CWA", "migrated"); e.Profile != "home" {
		t.Errorf("profile after Claim = %q, want home", e.Profile)
	}
	// A second profile syncing the same folder must not take it over.
	if err := s.Claim("/Books/CWA", "migrated", "nas"); err != nil {
		t.Fatalf("Claim(second): %v", err)
	}
	if e, _ := lookup(t, s, "/Books/CWA", "migrated"); e.Profile != "home" {
		t.Errorf("profile after the second Claim = %q, want home to keep it", e.Profile)
	}
	// Downloading over a row hands it to whoever fetched the file.
	if err := s.Upsert("/Books/CWA", "nas", makeBook("migrated", "Au", "T", ""), local, 1); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if e, _ := lookup(t, s, "/Books/CWA", "migrated"); e.Profile != "nas" {
		t.Errorf("profile after Upsert = %q, want nas", e.Profile)
	}
	// Claiming a UUID no row holds is a no-op, not an error.
	if err := s.Claim("/Books/CWA", "absent", "home"); err != nil {
		t.Errorf("Claim(absent) = %v, want nil", err)
	}
}
