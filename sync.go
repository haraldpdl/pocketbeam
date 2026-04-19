package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
// root directory) chosen at construction time.
func Sync(src Source, store *Store, library string, progress Progress, opts SyncOptions) SyncResult {
	var res SyncResult
	sweepStalePartFiles(library, stalePartAge)
	books, err := src.List(context.Background())
	if err != nil {
		res.FirstErr = fmt.Errorf("list remote: %w", err)
		return res
	}
	total := len(books)
	for i, b := range books {
		if progress != nil {
			progress(i+1, total, b)
		}
		local, oldPath, exists, err := store.LocalEntry(b.UUID)
		if err != nil {
			res.Failed++
			if res.FirstErr == nil {
				res.FirstErr = err
			}
			continue
		}
		if exists && !b.Updated.After(local) {
			res.Skipped++
			continue
		}
		path, err := download(src, library, b)
		if err != nil {
			res.Failed++
			if res.FirstErr == nil {
				res.FirstErr = fmt.Errorf("download %q: %w", b.Title, err)
			}
			continue
		}
		// If the book was renamed (title or author changed), the new download
		// lands at a new path; tidy up the old file so we don't accumulate
		// duplicates on the device.
		if exists && oldPath != "" && oldPath != path {
			if err := os.Remove(oldPath); err == nil {
				// Best-effort: also remove the old author directory if it's now empty.
				_ = os.Remove(filepath.Dir(oldPath))
			}
		}
		if err := store.Upsert(b, path); err != nil {
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

// reconcileDeletions computes the set of tracked books that are absent
// from the current remote listing, asks the caller for confirmation, and
// deletes the confirmed items (file on disk + store row). Several safety
// guards short-circuit before we call Confirm; a guard tripping is not
// an error, the sync run simply keeps those books.
func reconcileDeletions(store *Store, remote []Book, opts SyncOptions, res *SyncResult) int {
	// Empty-remote guard: a misconfigured feed or wrong credentials often
	// returns zero items. Treat that as "I can't trust this listing" and
	// keep everything.
	if len(remote) == 0 {
		return 0
	}
	// Scope-change guard: if the user narrowed/widened what they sync,
	// the diff against the previous scope's books would mass-delete.
	lastScope, _, _ := store.GetMeta(metaLastScope)
	if lastScope != opts.Scope {
		return 0
	}
	present := make(map[string]struct{}, len(remote))
	for _, b := range remote {
		present[b.UUID] = struct{}{}
	}
	entries, err := store.AllEntries()
	if err != nil {
		if res.FirstErr == nil {
			res.FirstErr = fmt.Errorf("list local entries: %w", err)
		}
		return 0
	}
	var missing []LocalBook
	for _, e := range entries {
		if _, ok := present[e.UUID]; !ok {
			missing = append(missing, e)
		}
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
			if err := os.Remove(m.LocalPath); err != nil && !os.IsNotExist(err) {
				if res.FirstErr == nil {
					res.FirstErr = fmt.Errorf("delete %q: %w", m.Title, err)
				}
				continue
			}
			// Prune the author directory if it's now empty; ignore errors.
			_ = os.Remove(filepath.Dir(m.LocalPath))
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

func download(src Source, library string, b Book) (string, error) {
	ext := formatExt[b.Format]
	if ext == "" {
		return "", fmt.Errorf("no extension known for %s", b.Format)
	}
	dir := filepath.Join(library, sanitize(b.Author))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, sanitize(b.Title)+ext)

	ctx, cancel := context.WithTimeout(context.Background(), downloadBodyTimeout)
	defer cancel()
	body, err := src.Fetch(ctx, b)
	if err != nil {
		return "", err
	}
	defer body.Close()

	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
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
