package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// Sync reconciles the remote catalog with the local library and store. It
// returns counts of (downloaded, skipped, failed) and the first error
// encountered (subsequent errors are still attempted but only the first is
// returned, so a single bad entry doesn't abort the whole sync).
//
// If filterHref is non-empty, only that OPDS path is synced (a CWA shelf
// path like /opds/shelf/1 or a generic subsection like /opds/books);
// otherwise the full catalog.
func Sync(client *Client, store *Store, library, filterHref string, progress Progress) (downloaded, skipped, failed int, firstErr error) {
	sweepStalePartFiles(library, stalePartAge)
	books, err := client.WalkFiltered(filterHref)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("walk catalog: %w", err)
	}
	total := len(books)
	for i, b := range books {
		if progress != nil {
			progress(i+1, total, b)
		}
		local, oldPath, exists, err := store.LocalEntry(b.UUID)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if exists && !b.Updated.After(local) {
			skipped++
			continue
		}
		path, err := download(client, library, b)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("download %q: %w", b.Title, err)
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
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("store %q: %w", b.Title, err)
			}
			continue
		}
		downloaded++
	}
	return downloaded, skipped, failed, firstErr
}

func download(client *Client, library string, b Book) (string, error) {
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
	body, err := client.Fetch(ctx, b.URL)
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
