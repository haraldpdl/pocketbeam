package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/studio-b12/gowebdav"
)

// WebDAVSource syncs from a directory on a WebDAV server (Nextcloud,
// Synology, ownCloud, Apache/nginx dav). The sync scope is the root
// directory configured up front; listing is recursive from there. Item
// identity is the path relative to that root, prefixed with "webdav:" so
// it cannot collide with OPDS UUIDs in the same state database.
type WebDAVSource struct {
	Client *gowebdav.Client
	Root   string // absolute path on the server, e.g. "/Books/Fiction"
}

// extToFormat maps supported ebook/comic extensions to the OPDS-style mime
// types the rest of pocketbeam keys on (see formatExt in sync.go).
var extToFormat = map[string]string{
	".epub": "application/epub+zip",
	".cbz":  "application/x-cbz",
	".cbr":  "application/x-cbr",
	".pdf":  "application/pdf",
}

// ProbeWebDAV verifies that host is a reachable WebDAV endpoint that
// accepts the credentials and exposes rootPath. Returns nil on success; on
// failure the error message is short and suitable for display on the
// device.
func ProbeWebDAV(ctx context.Context, host, user, pass, rootPath string) error {
	c := gowebdav.NewClient(host, user, pass)
	c.SetHeader("User-Agent", "pocketbeam/"+version)
	if err := c.Connect(); err != nil {
		return classifyWebDAVError(err)
	}
	root := normaliseRoot(rootPath)
	if _, err := c.ReadDir(root); err != nil {
		return fmt.Errorf("Could not list %s on the server.", root)
	}
	return nil
}

// classifyWebDAVError maps a gowebdav error to a concise, user-readable
// message. gowebdav wraps everything in errors.New() with context, so we
// pattern-match on substrings rather than typed errors.
func classifyWebDAVError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401"):
		return fmt.Errorf("Username or password rejected by the server.")
	case strings.Contains(msg, "403"):
		return fmt.Errorf("Credentials accepted but this user has no access.")
	case strings.Contains(msg, "404"):
		return fmt.Errorf("Server reachable but the WebDAV path was not found.")
	case strings.Contains(msg, "x509"), strings.Contains(msg, "tls"):
		return fmt.Errorf("TLS handshake failed. Is the server's certificate valid?")
	case strings.Contains(msg, "no such host"):
		return fmt.Errorf("Server name could not be resolved. Check the URL.")
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("Server refused the connection. Is the service running?")
	}
	return fmt.Errorf("Could not reach the WebDAV server.")
}

// NewWebDAVSource builds a source rooted at rootPath on the given WebDAV
// server. The client is not connected until the first List / Fetch call;
// use ProbeWebDAV up front to validate credentials.
func NewWebDAVSource(host, user, pass, rootPath string) *WebDAVSource {
	c := gowebdav.NewClient(host, user, pass)
	c.SetHeader("User-Agent", "pocketbeam/"+version)
	return &WebDAVSource{
		Client: c,
		Root:   normaliseRoot(rootPath),
	}
}

func (s *WebDAVSource) List(ctx context.Context) ([]Book, error) {
	var out []Book
	if err := s.walk(s.Root, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *WebDAVSource) Fetch(ctx context.Context, b Book) (io.ReadCloser, error) {
	return s.Client.ReadStream(b.URL)
}

// walk recursively lists dir and appends every ebook/comic file to out.
// WebDAV doesn't have a standard recursive PROPFIND (Depth: infinity is
// often disabled server-side), so we iterate per-directory.
func (s *WebDAVSource) walk(dir string, out *[]Book) error {
	entries, err := s.Client.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}
	for _, e := range entries {
		child := path.Join(dir, e.Name())
		if e.IsDir() {
			if err := s.walk(child, out); err != nil {
				return err
			}
			continue
		}
		if b, ok := bookFromFileInfo(s.Root, child, e); ok {
			*out = append(*out, b)
		}
	}
	return nil
}

// bookFromFileInfo maps a WebDAV file entry to a Book, returning false if
// the extension is not one pocketbeam knows how to place.
func bookFromFileInfo(root, full string, fi os.FileInfo) (Book, bool) {
	ext := strings.ToLower(path.Ext(full))
	format, ok := extToFormat[ext]
	if !ok {
		return Book{}, false
	}
	rel := strings.TrimPrefix(full, root)
	rel = strings.TrimPrefix(rel, "/")
	title := strings.TrimSuffix(path.Base(full), path.Ext(full))
	// Author comes from the immediate parent directory below the root,
	// which matches how most WebDAV ebook libraries are organised
	// (/Author/Title.epub). Books living directly in the root get no
	// author, matching pocketbeam's existing sanitize("") = "_" directory.
	author := ""
	if parent := path.Dir(rel); parent != "." && parent != "/" {
		author = path.Base(parent)
	}
	var size int64
	if s := fi.Size(); s > 0 {
		size = s
	}
	return Book{
		UUID:    "webdav:" + rel,
		Title:   title,
		Author:  author,
		Updated: fi.ModTime(),
		URL:     full,
		Format:  format,
		Size:    size,
	}, true
}

// normaliseRoot trims trailing slashes and ensures a leading slash. "" and
// "/" both mean "sync from the server root".
func normaliseRoot(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
