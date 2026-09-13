package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_UpsertAndLocalEntry(t *testing.T) {
	s, _ := openTempStore(t)
	defer s.Close()

	if _, _, _, exists, err := s.LocalEntry("nope"); err != nil || exists {
		t.Fatalf("LocalEntry(unknown) = exists=%v err=%v, want exists=false", exists, err)
	}

	b := makeBook("u1", "Author", "Title", "http://x/1")
	b.Updated = time.Unix(1_700_000_000, 0)
	b.Size = 500
	if err := s.Upsert(b, "/lib/Author/Title.epub", 1234); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	updated, path, size, exists, err := s.LocalEntry("u1")
	if err != nil || !exists {
		t.Fatalf("LocalEntry = exists=%v err=%v", exists, err)
	}
	if !updated.Equal(b.Updated) || path != "/lib/Author/Title.epub" {
		t.Errorf("LocalEntry = (%v, %q), want (%v, %q)", updated, path, b.Updated, "/lib/Author/Title.epub")
	}
	// The measured transfer size wins over the advertised one.
	if size != 1234 {
		t.Errorf("size = %d, want 1234 (actual bytes read)", size)
	}

	// Re-upserting the same UUID updates in place rather than duplicating.
	b.Title = "Renamed"
	b.Updated = b.Updated.Add(time.Hour)
	if err := s.Upsert(b, "/lib/Author/Renamed.epub", 0); err != nil {
		t.Fatalf("Upsert(again): %v", err)
	}
	if n, _ := s.BookCount(); n != 1 {
		t.Errorf("BookCount = %d, want 1 after re-upsert", n)
	}
	updated, path, size, _, _ = s.LocalEntry("u1")
	if !updated.Equal(b.Updated) || path != "/lib/Author/Renamed.epub" {
		t.Errorf("after update: (%v, %q)", updated, path)
	}
	// No measured size falls back to the server-advertised one.
	if size != 500 {
		t.Errorf("size = %d, want 500 (advertised fallback)", size)
	}

	// Neither measured nor advertised: stored as 0 (unknown), never negative.
	b.Size = -7
	if err := s.Upsert(b, path, -1); err != nil {
		t.Fatalf("Upsert(negative): %v", err)
	}
	if _, _, size, _, _ = s.LocalEntry("u1"); size != 0 {
		t.Errorf("size = %d, want 0 for unknown", size)
	}
}

func TestStore_AllEntriesAndDelete(t *testing.T) {
	s, _ := openTempStore(t)
	defer s.Close()

	for _, id := range []string{"a", "b"} {
		if err := s.Upsert(makeBook(id, "Au", "T"+id, ""), "/lib/"+id, 10); err != nil {
			t.Fatalf("Upsert(%s): %v", id, err)
		}
	}
	all, err := s.AllEntries()
	if err != nil || len(all) != 2 {
		t.Fatalf("AllEntries = %d entries, err=%v; want 2", len(all), err)
	}
	for _, e := range all {
		if e.Author != "Au" || e.LocalPath != "/lib/"+e.UUID || e.Size != 10 || e.Title != "T"+e.UUID {
			t.Errorf("entry %+v carries wrong display/path/size fields", e)
		}
	}
	if err := s.Delete("a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, _, exists, _ := s.LocalEntry("a"); exists {
		t.Error("entry a still present after Delete")
	}
	if n, _ := s.BookCount(); n != 1 {
		t.Errorf("BookCount = %d, want 1", n)
	}
	// Deleting an unknown UUID is a no-op, not an error.
	if err := s.Delete("missing"); err != nil {
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

// Installs from before the size column exist in the wild; OpenStore must
// add the column without touching existing rows, and reopening an
// already-migrated database must stay idempotent.
func TestOpenStore_MigratesLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE books (
		uuid TEXT PRIMARY KEY, title TEXT NOT NULL, author TEXT NOT NULL,
		updated INTEGER NOT NULL, local_path TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO books VALUES ('old', 'Old Title', 'Old Author', 42, '/lib/old.epub')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	for pass := 1; pass <= 2; pass++ {
		s, err := OpenStore(path)
		if err != nil {
			t.Fatalf("OpenStore pass %d: %v", pass, err)
		}
		updated, local, size, exists, err := s.LocalEntry("old")
		if err != nil || !exists {
			t.Fatalf("pass %d: LocalEntry = exists=%v err=%v", pass, exists, err)
		}
		if updated.Unix() != 42 || local != "/lib/old.epub" || size != 0 {
			t.Errorf("pass %d: legacy row = (%d, %q, %d), want (42, /lib/old.epub, 0)", pass, updated.Unix(), local, size)
		}
		if _, _, err := s.GetMeta("anything"); err != nil {
			t.Errorf("pass %d: meta table missing after migration: %v", pass, err)
		}
		s.Close()
	}
}

func TestOpenStore_UnwritablePath(t *testing.T) {
	if _, err := OpenStore(filepath.Join(t.TempDir(), "missing-dir", "state.db")); err == nil {
		t.Error("OpenStore into a missing directory should fail")
	}
}

func TestStore_OwnerOf(t *testing.T) {
	s, _ := openTempStore(t)
	defer s.Close()

	if err := s.Upsert(makeBook("u1", "Au", "T", ""), "/lib/Au/T.epub", 1); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ path, want string }{
		{"/lib/Au/T.epub", "u1"},
		{"/lib/Au/t.EPUB", "u1"}, // FAT is case-insensitive
		{"/lib/Au/Other.epub", ""},
	}
	for _, tc := range cases {
		got, err := s.OwnerOf(tc.path)
		if err != nil {
			t.Fatalf("OwnerOf(%q): %v", tc.path, err)
		}
		if got != tc.want {
			t.Errorf("OwnerOf(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
