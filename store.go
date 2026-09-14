package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
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

// Store tracks which books we've already downloaded, keyed on the library
// the copy lives in plus the book's UUID. The `updated` column lets us
// detect when a remote book has been re-imported (CWA bumps mtime on
// edits) so we re-fetch the new version.
//
// The library is part of the key because one state DB serves every
// profile while each profile has its own library folder, and two servers
// can hand out one identity (WebDAV UUIDs are "webdav:<path>", identical
// for the same relative path anywhere). A row per identity would describe
// whichever library downloaded it last, leaving the other library's copy
// untracked.
//
// The `profile` column records which profile downloaded the copy. It is
// not part of the key: the library folder is per profile for anything
// this version sets up, so the column only matters where it is not.
// Installs created before per-profile folders put every OPDS profile in
// Books/CWA and every WebDAV one in Books/WebDAV, so one library value
// can cover several profiles; the column is what keeps one of them from
// deleting another's books (see computeMissing).
type Store struct {
	db *sql.DB
}

// booksColumns is the books table body, shared by the create and the
// migration rebuild so the two schemas cannot drift apart.
const booksColumns = `(
	library    TEXT NOT NULL,
	uuid       TEXT NOT NULL,
	profile    TEXT NOT NULL DEFAULT '',
	title      TEXT NOT NULL,
	author     TEXT NOT NULL,
	updated    INTEGER NOT NULL,
	local_path TEXT NOT NULL,
	size       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (library, uuid)
)`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", fmt.Sprintf("%s?_busy_timeout=%d", path, busyTimeout.Milliseconds()))
	if err != nil {
		return nil, err
	}
	// One connection per process: every Store call is a single autocommit
	// statement and SQLite serialises writers anyway, so a second pooled
	// connection would only let this process contend with itself.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS books ` + booksColumns); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateBooks(db); err != nil {
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

// migrateBooks brings a books table written by an older install up to the
// current schema: the `size` column (added with the pre-flight space
// estimate), the (library, uuid) key that replaced the UUID-only one, and
// the `profile` column that names the downloader. Every step is a no-op
// on a table this version created. The rebuild already produces the
// current column set, so the ALTER only covers a table that has the key
// but predates the column.
func migrateBooks(db *sql.DB) error {
	cols, err := tableColumns(db, "books")
	if err != nil {
		return err
	}
	if !cols["size"] {
		if _, err := db.Exec(`ALTER TABLE books ADD COLUMN size INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if !cols["library"] {
		return splitBooksPerLibrary(db)
	}
	if !cols["profile"] {
		if _, err := db.Exec(`ALTER TABLE books ADD COLUMN profile TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

// tableColumns returns the column names of table as a set.
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// splitBooksPerLibrary rewrites a UUID-keyed books table into the
// (library, uuid) schema. Changing a primary key means rebuilding the
// table, so the rows are copied into a new one and it takes the old name.
// local_path is the only record a shared row carries of where its file
// is, so the library is derived from it (see libraryOf). The rows carry
// no profile: nothing recorded which one downloaded them, and a sync
// claims them one by one (see Store.Claim).
//
// The copy runs off one prepared statement because it happens on the
// first launch after the upgrade, inside OpenStore, on the InkView event
// loop: re-parsing the INSERT once per book would stall a device holding
// a few thousand of them behind a frozen screen.
func splitBooksPerLibrary(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT uuid, title, author, updated, local_path, size FROM books`)
	if err != nil {
		return err
	}
	type legacyRow struct {
		uuid, title, author, localPath string
		updated, size                  int64
	}
	var legacy []legacyRow
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.uuid, &r.title, &r.author, &r.updated, &r.localPath, &r.size); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	// The pool holds a single connection, so the read has to be finished
	// before the writes below can run on this transaction.
	rows.Close()

	if _, err := tx.Exec(`CREATE TABLE books_migrated ` + booksColumns); err != nil {
		return err
	}
	insert, err := tx.Prepare(`INSERT INTO books_migrated (library, uuid, title, author, updated, local_path, size)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	for _, r := range legacy {
		if _, err := insert.Exec(libraryOf(r.localPath), r.uuid, r.title, r.author, r.updated, r.localPath, r.size); err != nil {
			insert.Close()
			return err
		}
	}
	// The DROP below needs the statement's handle on books_migrated gone.
	if err := insert.Close(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE books`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE books_migrated RENAME TO books`); err != nil {
		return err
	}
	return tx.Commit()
}

// libraryOf derives the library a pre-migration row belongs to from its
// recorded path: targetPath writes <library>/<author>/<title><ext>, so the
// library sits two levels up. A path of another shape yields a key no
// profile uses, which leaves the row unread rather than attached to the
// wrong library; the book is then re-downloaded and tracked again on the
// next sync.
func libraryOf(localPath string) string {
	return libraryKey(filepath.Dir(filepath.Dir(localPath)))
}

// libraryKey normalises a library path into the value stored in the
// library column. The rules are pathKey's (clean, then fold case, because
// the library sits on a FAT volume), so a hand-edited trailing slash or a
// case difference still names one library.
func libraryKey(library string) string {
	return pathKey(library)
}

// BookCount returns the number of books tracked for library. The state DB
// is shared by every profile, so an unfiltered count would tell the main
// screen the sum of all profiles' libraries.
func (s *Store) BookCount(library string) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM books WHERE library = ?`, libraryKey(library)).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
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

// Upsert records a successful download by profile. actualSize is the byte
// count read off the wire during this download; it takes precedence over
// b.Size (which is the server-advertised value and may not match). A
// non-positive actualSize falls back to b.Size so callers can use Upsert
// from contexts that don't measure the transfer.
//
// sharedLibrary makes ownership sticky: in a folder another profile
// downloads into (see SyncOptions.LibraryShared) there is one row for
// the UUID and one file on disk, so re-downloading a book both servers
// list must not move the row to the second profile - it would hand that
// profile the right to delete the first one's only copy as soon as the
// book leaves its shelf. Ownership there follows the same one-way rule
// as Store.Claim: the first profile to own a row keeps it. In a folder
// only this profile writes to there is nothing to protect, so the row
// records whoever last fetched the file and a profile renamed in the
// config file re-adopts its books.
func (s *Store) Upsert(library, profile string, b Book, localPath string, actualSize int64, sharedLibrary bool) error {
	size := actualSize
	if size <= 0 {
		size = b.Size
	}
	if size < 0 {
		size = 0
	}
	_, err := s.db.Exec(`INSERT INTO books (library, uuid, profile, title, author, updated, local_path, size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(library, uuid) DO UPDATE SET
			profile = CASE WHEN ? AND books.profile <> '' THEN books.profile ELSE excluded.profile END,
			title = excluded.title,
			author = excluded.author,
			updated = excluded.updated,
			local_path = excluded.local_path,
			size = excluded.size`,
		libraryKey(library), b.UUID, profile, b.Title, b.Author, b.Updated.Unix(), localPath, size,
		sharedLibrary)
	return err
}

// Claim names profile as the owner of a row that has none. Rows migrated
// from the UUID-keyed schema record no profile, because the schema that
// wrote them had nowhere to put one; a sync whose remote listing still
// carries the row's UUID is evidence the copy is that profile's, so it
// takes ownership. A row that already names a profile is left alone: the
// first profile to claim it keeps it, otherwise two profiles sharing a
// library folder would take turns owning (and so deleting) one file.
func (s *Store) Claim(library, uuid, profile string) error {
	_, err := s.db.Exec(`UPDATE books SET profile = ? WHERE library = ? AND uuid = ? AND profile = ''`,
		profile, libraryKey(library), uuid)
	return err
}

// OtherOwner returns a UUID other than except whose local_path in library
// is path, or "" when no other tracked book lives there. Stores written
// before filenames were disambiguated can hold several UUIDs at one path,
// so the caller names itself to be excluded rather than comparing a single
// returned owner. The match ignores ASCII case because the device library
// sits on a FAT volume, where "Title.epub" and "title.epub" are the same
// file.
func (s *Store) OtherOwner(library, path, except string) (string, error) {
	var uuid string
	err := s.db.QueryRow(`SELECT uuid FROM books WHERE library = ? AND local_path = ? COLLATE NOCASE AND uuid <> ? LIMIT 1`,
		libraryKey(library), path, except).Scan(&uuid)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return uuid, nil
}

// LocalBook is the local view of a previously-synced book: identity, the
// profile that downloaded it ("" for a row migrated from the UUID-keyed
// schema, which recorded none), human display fields, the remote
// timestamp at download time, the path on disk, and the cached byte size
// (0 if unknown: legacy rows from before the size column, or a server
// that never advertised length). Used by the sync diff, the
// delete-missing path, the confirmation prompt, and the pre-flight space
// check.
type LocalBook struct {
	UUID      string
	Profile   string
	Title     string
	Author    string
	Updated   time.Time
	LocalPath string
	Size      int64
}

// AllEntries returns every book tracked for library. Used to compute
// which local entries are missing from the current remote listing.
func (s *Store) AllEntries(library string) ([]LocalBook, error) {
	rows, err := s.db.Query(`SELECT uuid, profile, title, author, updated, local_path, size FROM books WHERE library = ?`, libraryKey(library))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LocalBook
	for rows.Next() {
		var b LocalBook
		var ts int64
		if err := rows.Scan(&b.UUID, &b.Profile, &b.Title, &b.Author, &ts, &b.LocalPath, &b.Size); err != nil {
			return nil, err
		}
		b.Updated = time.Unix(ts, 0)
		out = append(out, b)
	}
	return out, rows.Err()
}

// EntriesByUUID returns library's tracked books keyed on UUID. Plan and
// Sync load it once per run so diffing the remote list costs one query
// rather than one per book.
func (s *Store) EntriesByUUID(library string) (map[string]LocalBook, error) {
	entries, err := s.AllEntries(library)
	if err != nil {
		return nil, err
	}
	out := make(map[string]LocalBook, len(entries))
	for _, e := range entries {
		out[e.UUID] = e
	}
	return out, nil
}

// Delete removes library's tracked entry for a UUID. Another library's
// entry for the same UUID is a different book copy and stays. The file on
// disk is the caller's responsibility.
func (s *Store) Delete(library, uuid string) error {
	_, err := s.db.Exec(`DELETE FROM books WHERE library = ? AND uuid = ?`, libraryKey(library), uuid)
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
