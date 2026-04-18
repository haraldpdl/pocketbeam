package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

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
func Sync(client *Client, store *Store, library string, progress Progress) (downloaded, skipped, failed int, firstErr error) {
	books, err := client.WalkAll()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("walk catalog: %w", err)
	}
	total := len(books)
	for i, b := range books {
		if progress != nil {
			progress(i+1, total, b)
		}
		local, err := store.LocalUpdated(b.UUID)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !local.IsZero() && !b.Updated.After(local) {
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

	body, err := client.Fetch(b.URL)
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
	if out == "" {
		return "_"
	}
	return out
}
