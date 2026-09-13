package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// downloadBodyTimeout caps the wall-clock time for a single book download
// once headers have been received. The transport's ResponseHeaderTimeout
// only covers the handshake, so a server that stalls the body at 1 byte per
// second could otherwise hold the TCP connection indefinitely.
const downloadBodyTimeout = 30 * time.Minute

// stalePartAge is the minimum age of a leftover .part file before the sweep
// deletes it. The grace window avoids clobbering a concurrent run (e.g. the
// user triggers sync from the UI while a CLI run is still in flight).
const stalePartAge = 1 * time.Hour

// maxFilenameBytes caps sanitized filename components below ext4's 255-byte
// NAME_MAX, leaving headroom for an extension. Calibre multi-author strings
// ("A & B & C & D & ...") routinely exceed this on generic OPDS catalogs.
const maxFilenameBytes = 200

// formatExt maps OPDS acquisition mime types to file extensions.
var formatExt = map[string]string{
	"application/epub+zip": ".epub",
	"application/x-cbz":    ".cbz",
	"application/x-cbr":    ".cbr",
	"application/pdf":      ".pdf",
}

// Progress is invoked once per book before its download starts. Index is
// 1-based. Pass nil to disable progress reporting.
type Progress func(index, total int, b Book)

// Confirm asks the caller (UI or CLI) whether to proceed with deleting
// the given local books that are no longer present on the remote. Return
// true to delete, false to skip. Called at most once per sync, and only
// when the deletion set is non-empty.
type Confirm func(deletions []LocalBook) bool

// SyncOptions groups the backend-agnostic inputs for a sync run.
type SyncOptions struct {
	// DeleteMissing enables the post-download reconciliation step that
	// removes local books whose UUID is absent from the fresh remote list.
	DeleteMissing bool

	// Scope is an opaque identity of the remote scope (backend + filter +
	// path). When the stored last-sync scope differs from this one, the
	// delete step is skipped for this run: a scope change is interpreted
	// as the user narrowing or widening what they sync, not a signal to
	// prune the device.
	Scope string

	// Confirm is invoked before any deletion happens. If nil, the delete
	// step is skipped even when DeleteMissing is true.
	Confirm Confirm

	// PrefetchedBooks short-circuits the initial List call when the caller
	// already obtained the remote list (e.g. from a prior Plan). Nil means
	// "List normally". Kept on options rather than as a separate Sync
	// variant so existing callers stay unchanged.
	PrefetchedBooks []Book
}

// SyncResult captures counts for a sync run plus the first non-fatal
// error encountered (subsequent errors are still attempted but only the
// first is returned, so a single bad entry doesn't abort the whole sync).
type SyncResult struct {
	Downloaded int
	Skipped    int
	Failed     int
	Deleted    int
	FirstErr   error
}

// metaLastScope is the key under which Sync persists the Scope of the
// most recent run, used to skip the delete step when scope changes.
const metaLastScope = "last_scope"

// Sync reconciles the remote catalog with the local library and store.
// The source carries any backend-specific scoping (OPDS filter, WebDAV
// root directory) chosen at construction time. The context cancels both
// the List call and any in-flight download; on cancellation the partial
// file is left as a .part for the next sync to clean up.
func Sync(ctx context.Context, src Source, store *Store, library string, progress Progress, opts SyncOptions) SyncResult {
	var res SyncResult
	sweepStalePartFiles(library, stalePartAge)
	books := opts.PrefetchedBooks
	if books == nil {
		var err error
		books, err = src.List(ctx)
		if err != nil {
			res.FirstErr = fmt.Errorf("list remote: %w", err)
			return res
		}
	}
	local, err := store.EntriesByUUID()
	if err != nil {
		res.FirstErr = fmt.Errorf("list local entries: %w", err)
		return res
	}
	total := len(books)
	for i, b := range books {
		if err := ctx.Err(); err != nil {
			res.FirstErr = err
			return res
		}
		if progress != nil {
			progress(i+1, total, b)
		}
		old, exists := local[b.UUID]
		if exists && !b.Updated.After(old.Updated) {
			res.Skipped++
			continue
		}
		path, err := targetPath(store, library, b)
		if err != nil {
			res.Failed++
			if res.FirstErr == nil {
				res.FirstErr = fmt.Errorf("place %q: %w", b.Title, err)
			}
			continue
		}
		actualSize, err := download(ctx, src, path, b)
		if err != nil {
			if ctx.Err() != nil {
				res.FirstErr = ctx.Err()
				return res
			}
			res.Failed++
			if res.FirstErr == nil {
				res.FirstErr = fmt.Errorf("download %q: %w", b.Title, err)
			}
			continue
		}
		// If the book was renamed (title or author changed), the new download
		// lands at a new path; tidy up the old file so we don't accumulate
		// duplicates on the device. Stores written before filenames were
		// disambiguated can map another book to the old path, in which case
		// the file is that book's only copy and must stay.
		if exists && old.LocalPath != "" && old.LocalPath != path {
			if other, _ := store.OtherOwner(old.LocalPath, b.UUID); other == "" {
				if err := os.Remove(old.LocalPath); err == nil {
					// Best-effort: also remove the old author directory if it's now empty.
					_ = os.Remove(filepath.Dir(old.LocalPath))
				}
			}
		}
		if err := store.Upsert(b, path, actualSize); err != nil {
			res.Failed++
			if res.FirstErr == nil {
				res.FirstErr = fmt.Errorf("store %q: %w", b.Title, err)
			}
			continue
		}
		res.Downloaded++
	}

	// Delete-missing reconciliation runs after downloads so a user whose
	// sync is interrupted gets the new books regardless.
	if opts.DeleteMissing && opts.Confirm != nil {
		res.Deleted = reconcileDeletions(store, books, opts, &res)
	}

	// Record this run's scope even when we didn't delete, so the next
	// sync with the same scope can proceed with deletion.
	if opts.Scope != "" {
		_ = store.SetMeta(metaLastScope, opts.Scope)
	}
	return res
}

// computeMissing returns the set of local entries absent from the current
// remote listing, applying the empty-remote and scope-change guards. A nil
// return with nil error means a guard tripped: the caller should treat it
// as "no deletions candidate" rather than surface an error. This is the
// shared source of truth for reconcileDeletions and Plan so the pre-flight
// space estimate agrees with what the delete step will actually do.
func computeMissing(store *Store, remote []Book, opts SyncOptions) ([]LocalBook, error) {
	// Empty-remote guard: a misconfigured feed or wrong credentials often
	// returns zero items. Treat that as "I can't trust this listing" and
	// keep everything.
	if len(remote) == 0 {
		return nil, nil
	}
	// Scope-change guard: if the user narrowed/widened what they sync,
	// the diff against the previous scope's books would mass-delete.
	lastScope, _, _ := store.GetMeta(metaLastScope)
	if lastScope != opts.Scope {
		return nil, nil
	}
	present := make(map[string]struct{}, len(remote))
	for _, b := range remote {
		present[b.UUID] = struct{}{}
	}
	entries, err := store.AllEntries()
	if err != nil {
		return nil, err
	}
	var missing []LocalBook
	for _, e := range entries {
		if _, ok := present[e.UUID]; !ok {
			missing = append(missing, e)
		}
	}
	return missing, nil
}

// reconcileDeletions computes the set of tracked books that are absent
// from the current remote listing, asks the caller for confirmation, and
// deletes the confirmed items (file on disk + store row). Several safety
// guards short-circuit before we call Confirm; a guard tripping is not
// an error, the sync run simply keeps those books.
func reconcileDeletions(store *Store, remote []Book, opts SyncOptions, res *SyncResult) int {
	missing, err := computeMissing(store, remote, opts)
	if err != nil {
		if res.FirstErr == nil {
			res.FirstErr = fmt.Errorf("list local entries: %w", err)
		}
		return 0
	}
	if len(missing) == 0 {
		return 0
	}
	if !opts.Confirm(missing) {
		return 0
	}
	deleted := 0
	for _, m := range missing {
		if m.LocalPath != "" {
			// Stores written before filenames were disambiguated can map
			// another book to this path; then the file is that book's only
			// copy and only the row goes.
			other, err := store.OtherOwner(m.LocalPath, m.UUID)
			if err != nil {
				if res.FirstErr == nil {
					res.FirstErr = fmt.Errorf("delete %q: %w", m.Title, err)
				}
				continue
			}
			if other == "" {
				if err := os.Remove(m.LocalPath); err != nil && !os.IsNotExist(err) {
					if res.FirstErr == nil {
						res.FirstErr = fmt.Errorf("delete %q: %w", m.Title, err)
					}
					continue
				}
				// Prune the author directory if it's now empty; ignore errors.
				_ = os.Remove(filepath.Dir(m.LocalPath))
			}
		}
		if err := store.Delete(m.UUID); err != nil {
			if res.FirstErr == nil {
				res.FirstErr = fmt.Errorf("forget %q: %w", m.Title, err)
			}
			continue
		}
		deleted++
	}
	return deleted
}

// ScopeFor returns the opaque scope string for a config. Change in any
// of the identity-bearing fields (backend, host, filter set, path)
// invalidates the scope, disabling the delete step for that run. The
// filter list is sorted before hashing so two orderings of the same set
// produce the same scope.
func ScopeFor(cfg *Config) string {
	filters := append([]string(nil), cfg.FilterHrefs...)
	sort.Strings(filters)
	return fmt.Sprintf("%s|%s|%s|%s|%s", cfg.Profile, cfg.Backend, cfg.Host, strings.Join(filters, ","), cfg.Path)
}

// SyncPlan is the pre-flight estimate of a sync run: what would be
// downloaded, skipped, and (if delete-missing guards don't trip) removed,
// together with the byte counts needed to decide whether the run fits in
// the device's free space. Size fields are best-effort; a book whose
// Size is unknown contributes 0 and UnknownSizes is incremented.
type SyncPlan struct {
	NewBooks     []Book
	UpdatedBooks []Book
	Unchanged    int
	Missing      []LocalBook

	// DownloadBytes is the peak additional space the sync needs: the sum
	// of New and Updated book sizes. We do not deduct the old size of an
	// updated book because `.part` files are written before the rename,
	// so during the download both copies coexist.
	DownloadBytes int64

	// ReclaimableBytes is the total size of Missing books, available only
	// after the user confirms deletion and after downloads have finished.
	// Informational: does not reduce DownloadBytes.
	ReclaimableBytes int64

	// UnknownSizes counts New/Updated books whose size the source did not
	// advertise (neither the server nor the store cache knew). The
	// DownloadBytes total is therefore a lower bound when this is > 0.
	UnknownSizes int

	// FreeBytes is the bytes currently available on the filesystem that
	// hosts `library`. 0 when the Statfs probe failed, which the caller
	// should treat as "unknown, skip space check" rather than "full".
	FreeBytes int64
}

// Fits reports whether the plan's known download bytes fit in the free
// space. Certain is false when UnknownSizes > 0 (the DownloadBytes total
// is a lower bound, so a true result could still overrun), or when
// FreeBytes is 0 (the free-space probe failed).
func (p SyncPlan) Fits() (ok bool, certain bool) {
	if p.FreeBytes == 0 {
		return true, false
	}
	ok = p.DownloadBytes <= p.FreeBytes
	certain = p.UnknownSizes == 0
	return ok, certain
}

// Plan computes what a Sync call with the same options would do without
// downloading anything. It lists the remote, diffs against the store,
// tallies sizes, and probes free space. The returned plan's NewBooks and
// UpdatedBooks share the same ordering as the remote list, so the caller
// can pass `PrefetchedBooks` back into Sync to avoid a second List.
func Plan(ctx context.Context, src Source, store *Store, library string, opts SyncOptions) (SyncPlan, []Book, error) {
	var plan SyncPlan
	books := opts.PrefetchedBooks
	if books == nil {
		var err error
		books, err = src.List(ctx)
		if err != nil {
			return plan, nil, fmt.Errorf("list remote: %w", err)
		}
	}
	local, err := store.EntriesByUUID()
	if err != nil {
		return plan, books, fmt.Errorf("list local entries: %w", err)
	}
	for _, b := range books {
		old, exists := local[b.UUID]
		if exists && !b.Updated.After(old.Updated) {
			plan.Unchanged++
			continue
		}
		var size int64
		switch {
		case b.Size > 0:
			size = b.Size
		case exists && old.Size > 0:
			// The server didn't advertise length this time but we know
			// from a prior download how big this UUID is. Use the cache
			// so the plan tightens up on every run.
			size = old.Size
		}
		if size > 0 {
			plan.DownloadBytes += size
		} else {
			plan.UnknownSizes++
		}
		if exists {
			plan.UpdatedBooks = append(plan.UpdatedBooks, b)
		} else {
			plan.NewBooks = append(plan.NewBooks, b)
		}
	}
	if opts.DeleteMissing {
		missing, err := computeMissing(store, books, opts)
		if err != nil {
			return plan, books, err
		}
		plan.Missing = missing
		for _, m := range missing {
			// A file shared with another tracked book stays on disk, so
			// its bytes are not reclaimable (see reconcileDeletions).
			if other, err := store.OtherOwner(m.LocalPath, m.UUID); err != nil {
				return plan, books, err
			} else if other == "" {
				plan.ReclaimableBytes += m.Size
			}
		}
	}
	plan.FreeBytes = availableBytes(library)
	return plan, books, nil
}

// availableBytes returns the free space on the filesystem that hosts path.
// MkdirAll guarantees Statfs has a valid target on first run. Returns 0 on
// any error: the caller treats 0 as "unknown, skip the space check".
func availableBytes(path string) int64 {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return 0
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	// Bavail is blocks available to an unprivileged user; Bsize is the
	// fundamental filesystem block size. Multiply in int64 to avoid the
	// overflow that plain int would hit on >2 GB fields on 32-bit ARM.
	return int64(st.Bavail) * int64(st.Bsize)
}

// targetPath decides where b lands in the library:
// <library>/<author>/<title><ext>. Two different books can sanitize to the
// same author and title (editions, reissues, WebDAV files of the same name
// in different sub-directories), and sharing one file means deleting either
// book removes the other's copy. When the store already maps the plain path
// to another UUID, the filename gets a short suffix derived from this book's
// UUID so each book keeps its own file. A book re-downloading over its own
// path is not a collision and keeps the plain name.
func targetPath(store *Store, library string, b Book) (string, error) {
	ext := formatExt[b.Format]
	if ext == "" {
		return "", fmt.Errorf("no extension known for %s", b.Format)
	}
	dir := filepath.Join(library, sanitize(b.Author))
	title := sanitize(b.Title)
	path := filepath.Join(dir, title+ext)
	owner, err := store.OtherOwner(path, b.UUID)
	if err != nil {
		return "", err
	}
	if owner != "" {
		path = filepath.Join(dir, title+" ["+shortID(b.UUID)+"]"+ext)
	}
	return path, nil
}

// shortID returns an 8-hex-char tag for a book UUID, used to disambiguate
// filenames. The UUID is hashed rather than truncated because WebDAV IDs
// are synthetic ("webdav:<relative path>"): they share long prefixes and
// contain path separators, so a raw prefix would neither be unique nor
// filename-safe.
func shortID(uuid string) string {
	sum := sha256.Sum256([]byte(uuid))
	return hex.EncodeToString(sum[:4])
}

// download fetches b into path via an atomic .part rename and returns the
// number of bytes written.
func download(parent context.Context, src Source, path string, b Book) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(parent, downloadBodyTimeout)
	defer cancel()
	body, err := src.Fetch(ctx, b)
	if err != nil {
		return 0, err
	}
	defer body.Close()

	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, body)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return 0, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	return n, nil
}

// sanitize strips characters that are illegal or awkward in filenames on
// FAT-style filesystems (the PocketBook internal storage). Slashes, control
// chars, leading/trailing whitespace and dots are removed; runs of whitespace
// collapse to a single space.
func sanitize(s string) string {
	if s == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < 0x20:
			// drop control chars
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	out = strings.Trim(out, ". ")
	out = truncateUTF8(out, maxFilenameBytes)
	out = strings.TrimRight(out, ". ")
	if out == "" {
		return "_"
	}
	return out
}

// truncateUTF8 returns s truncated to at most maxBytes bytes without
// splitting a multi-byte rune. Input shorter than the limit is returned
// unchanged.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// sweepStalePartFiles removes any *.part files under library older than
// olderThan. These are left behind by crashes, reboots, or SIGKILL during
// a prior sync's atomic-rename staging. Errors are swallowed; this is a
// best-effort housekeeping pass, not a precondition for sync.
func sweepStalePartFiles(library string, olderThan time.Duration) {
	cutoff := time.Now().Add(-olderThan)
	_ = filepath.WalkDir(library, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".part") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
		return nil
	})
}
