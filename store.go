package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
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

// busyTimeout is how long a statement waits for another process's lock
// before failing with "database is locked". The CLI and the UI can run at
// the same time against one state file (see stalePartAge); their writes
// are short, so waiting beats failing the whole book. mattn/go-sqlite3
// happens to default to the same value; it is set explicitly so the wait
// is visible here and survives a driver default change.
const busyTimeout = 5 * time.Second

// Store tracks which books we've already downloaded, keyed on the OPDS UUID.
// The `updated` column lets us detect when a remote book has been re-imported
// (CWA bumps mtime on edits) so we re-fetch the new version.
type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", fmt.Sprintf("%s?_busy_timeout=%d", path, busyTimeout.Milliseconds()))
	if err != nil {
		return nil, err
	}
	// One connection per process: every Store call is a single autocommit
	// statement and SQLite serialises writers anyway, so a second pooled
	// connection would only let this process contend with itself.
	db.SetMaxOpenConns(1)
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

// BookCount returns the number of tracked books whose file lives under
// library. The state DB is shared by every profile, so an unfiltered
// count would tell the main screen the sum of all profiles' libraries.
// Two of its callers run on the InkView event loop (app start, profile
// switch), so the filtering stays in SQL rather than materialising every
// row. The sync diff case-folds with strings.ToLower (Unicode) while LIKE
// folds ASCII only, so the two containment rules agree on ASCII paths and
// can disagree on a library path with non-ASCII cased letters; recorded
// paths come from filepath.Join and are therefore already clean.
func (s *Store) BookCount(library string) (int, error) {
	prefix := filepath.Clean(library) + string(filepath.Separator)
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM books WHERE local_path LIKE ? ESCAPE '\'`, likePrefix(prefix)).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// likePrefix turns a literal path prefix into a LIKE pattern matching
// everything below it. The prefix is escaped because sanitize puts "_"
// (a single-character wildcard) into folder and file names.
func likePrefix(prefix string) string {
	return likeEscape.Replace(prefix) + "%"
}

var likeEscape = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

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

// OtherOwner returns a UUID other than except whose local_path is path, or
// "" when no other tracked book lives there. Stores written before filenames
// were disambiguated can hold several UUIDs at one path, so the caller names
// itself to be excluded rather than comparing a single returned owner. The
// match ignores ASCII case because the device library sits on a FAT volume,
// where "Title.epub" and "title.epub" are the same file.
func (s *Store) OtherOwner(path, except string) (string, error) {
	var uuid string
	err := s.db.QueryRow(`SELECT uuid FROM books WHERE local_path = ? COLLATE NOCASE AND uuid <> ? LIMIT 1`, path, except).Scan(&uuid)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return uuid, nil
}

// LocalBook is the local view of a previously-synced book: identity,
// human display fields, the remote timestamp at download time, the path
// on disk, and the cached byte size (0 if unknown: legacy rows from before
// the size column, or a server that never advertised length). Used by the
// sync diff, the delete-missing path, the confirmation prompt, and the
// pre-flight space check.
type LocalBook struct {
	UUID      string
	Title     string
	Author    string
	Updated   time.Time
	LocalPath string
	Size      int64
}

// AllEntries returns every tracked book. Used to compute which local
// entries are missing from the current remote listing.
func (s *Store) AllEntries() ([]LocalBook, error) {
	rows, err := s.db.Query(`SELECT uuid, title, author, updated, local_path, size FROM books`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LocalBook
	for rows.Next() {
		var b LocalBook
		var ts int64
		if err := rows.Scan(&b.UUID, &b.Title, &b.Author, &ts, &b.LocalPath, &b.Size); err != nil {
			return nil, err
		}
		b.Updated = time.Unix(ts, 0)
		out = append(out, b)
	}
	return out, rows.Err()
}

// EntriesByUUID returns every tracked book keyed on UUID. Plan and Sync
// load it once per run so diffing the remote list costs one query rather
// than one per book.
func (s *Store) EntriesByUUID() (map[string]LocalBook, error) {
	entries, err := s.AllEntries()
	if err != nil {
		return nil, err
	}
	out := make(map[string]LocalBook, len(entries))
	for _, e := range entries {
		out[e.UUID] = e
	}
	return out, nil
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
