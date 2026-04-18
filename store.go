package main

import (
	"database/sql"
	"encoding/json"
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
		local_path TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
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

// LocalEntry returns the last-recorded updated timestamp and local path for
// a book UUID. exists=false means we have not synced this book before.
func (s *Store) LocalEntry(uuid string) (updated time.Time, path string, exists bool, err error) {
	var ts int64
	err = s.db.QueryRow("SELECT updated, local_path FROM books WHERE uuid = ?", uuid).Scan(&ts, &path)
	if err == sql.ErrNoRows {
		return time.Time{}, "", false, nil
	}
	if err != nil {
		return time.Time{}, "", false, err
	}
	return time.Unix(ts, 0), path, true, nil
}

// Upsert records a successful download.
func (s *Store) Upsert(b Book, localPath string) error {
	_, err := s.db.Exec(`INSERT INTO books (uuid, title, author, updated, local_path)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET
			title = excluded.title,
			author = excluded.author,
			updated = excluded.updated,
			local_path = excluded.local_path`,
		b.UUID, b.Title, b.Author, b.Updated.Unix(), localPath)
	return err
}
