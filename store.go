package main

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

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
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// LocalUpdated returns the last `updated` timestamp we have stored for the
// given UUID. Zero time means we don't have it yet.
func (s *Store) LocalUpdated(uuid string) (time.Time, error) {
	var ts int64
	err := s.db.QueryRow("SELECT updated FROM books WHERE uuid = ?", uuid).Scan(&ts)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(ts, 0), nil
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
