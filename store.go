package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// SyncSummary captures the outcome of a sync run, used to show the main screen
// the last-sync info after an app restart.
type SyncSummary struct {
	At         time.Time `json:"at"`
	Downloaded int       `json:"downloaded"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
}

// Store tracks which books we've already downloaded, keyed on the OPDS UUID.
// The `updated` column lets us detect when a remote book has been re-imported
// (CWA bumps mtime on edits) so we re-fetch the new version.
type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS books (
		uuid       TEXT PRIMARY KEY,
		title      TEXT NOT NULL,
		author     TEXT NOT NULL,
		updated    INTEGER NOT NULL,
		local_path TEXT NOT NULL,
		size       INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		db.Close()
		return nil, err
	}
	// Migration: older installs created `books` without the size column.
	// ALTER TABLE ADD COLUMN is idempotent only if we swallow the "duplicate
	// column" error; PRAGMA table_info is the clean alternative but this is
	// cheaper and covers the single-column case we care about.
	if _, err := db.Exec(`ALTER TABLE books ADD COLUMN size INTEGER NOT NULL DEFAULT 0`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// BookCount returns the number of books tracked locally.
func (s *Store) BookCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&n)
	return n, err
}

// SetLastSync persists the outcome of the most recent sync run.
func (s *Store) SetLastSync(sum SyncSummary) error {
	b, err := json.Marshal(sum)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO meta (key, value) VALUES ('last_sync', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b))
	return err
}

// LastSync returns the last-sync summary. The bool is false if no sync has
// run yet.
func (s *Store) LastSync() (SyncSummary, bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'last_sync'`).Scan(&raw)
	if err == sql.ErrNoRows {
		return SyncSummary{}, false, nil
	}
	if err != nil {
		return SyncSummary{}, false, err
	}
	var sum SyncSummary
	if err := json.Unmarshal([]byte(raw), &sum); err != nil {
		return SyncSummary{}, false, err
	}
	return sum, true, nil
}

func (s *Store) Close() error { return s.db.Close() }

// LocalEntry returns the last-recorded updated timestamp, local path, and
// cached byte size for a book UUID. Size is 0 when unknown (legacy rows
// from before the size column, or a server that never advertised length).
// exists=false means we have not synced this book before.
func (s *Store) LocalEntry(uuid string) (updated time.Time, path string, size int64, exists bool, err error) {
	var ts int64
	err = s.db.QueryRow("SELECT updated, local_path, size FROM books WHERE uuid = ?", uuid).Scan(&ts, &path, &size)
	if err == sql.ErrNoRows {
		return time.Time{}, "", 0, false, nil
	}
	if err != nil {
		return time.Time{}, "", 0, false, err
	}
	return time.Unix(ts, 0), path, size, true, nil
}

// Upsert records a successful download. actualSize is the byte count read
// off the wire during this download; it takes precedence over b.Size
// (which is the server-advertised value and may not match). A non-positive
// actualSize falls back to b.Size so callers can use Upsert from contexts
// that don't measure the transfer.
func (s *Store) Upsert(b Book, localPath string, actualSize int64) error {
	size := actualSize
	if size <= 0 {
		size = b.Size
	}
	if size < 0 {
		size = 0
	}
	_, err := s.db.Exec(`INSERT INTO books (uuid, title, author, updated, local_path, size)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET
			title = excluded.title,
			author = excluded.author,
			updated = excluded.updated,
			local_path = excluded.local_path,
			size = excluded.size`,
		b.UUID, b.Title, b.Author, b.Updated.Unix(), localPath, size)
	return err
}

// LocalBook is the local view of a previously-synced book: identity,
// human display fields, the path on disk, and the cached byte size
// (0 if unknown). Used by the delete-missing path, the confirmation
// prompt, and the pre-flight space check.
type LocalBook struct {
	UUID      string
	Title     string
	Author    string
	LocalPath string
	Size      int64
}

// AllEntries returns every tracked book. Used to compute which local
// entries are missing from the current remote listing.
func (s *Store) AllEntries() ([]LocalBook, error) {
	rows, err := s.db.Query(`SELECT uuid, title, author, local_path, size FROM books`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LocalBook
	for rows.Next() {
		var b LocalBook
		if err := rows.Scan(&b.UUID, &b.Title, &b.Author, &b.LocalPath, &b.Size); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Delete removes the tracked entry for a UUID. The file on disk is the
// caller's responsibility.
func (s *Store) Delete(uuid string) error {
	_, err := s.db.Exec(`DELETE FROM books WHERE uuid = ?`, uuid)
	return err
}

// SetMeta stores a small string under key in the meta table. Used for
// per-install bookkeeping that doesn't warrant its own column (e.g. the
// scope of the most recent sync).
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// GetMeta returns the value previously stored via SetMeta. The bool is
// false when the key has never been set.
func (s *Store) GetMeta(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}
