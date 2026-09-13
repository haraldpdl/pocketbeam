package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
//
// gowebdav has no context-aware API, so every operation builds a
// short-lived client whose transport injects the caller's context (see
// client). The negotiated auth method lives in the shared Authorizer and
// the connection pool in the shared transport, so per-call clients cost
// nothing extra on the wire.
type WebDAVSource struct {
	host      string
	auth      gowebdav.Authorizer
	transport http.RoundTripper
	Root      string // absolute path on the server, e.g. "/Books/Fiction"
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
// device. The probe is bounded by probeTimeout on top of ctx.
func ProbeWebDAV(ctx context.Context, host, user, pass, rootPath string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	s := NewWebDAVSource(host, user, pass, rootPath)
	if err := s.client(ctx).Connect(); err != nil {
		return classifyWebDAVError(err)
	}
	if _, err := s.ReadDir(ctx, s.Root); err != nil {
		if gowebdav.IsErrNotFound(err) {
			return fmt.Errorf("Could not list %s on the server.", s.Root)
		}
		return classifyWebDAVError(err)
	}
	return nil
}

// classifyWebDAVError maps a gowebdav error to a concise, user-readable
// message. Status failures arrive as a gowebdav.StatusError inside an
// os.PathError; anything else is a transport error and shares the OPDS
// wording.
func classifyWebDAVError(err error) error {
	var se gowebdav.StatusError
	if !errors.As(err, &se) {
		return classifyTransportError(err)
	}
	switch se.Status {
	case http.StatusUnauthorized:
		return errors.New("Username or password rejected by the server.")
	case http.StatusForbidden:
		return errors.New("Credentials accepted but this user has no access.")
	case http.StatusNotFound:
		return errors.New("Server reachable but the WebDAV path was not found.")
	}
	if se.Status >= 500 {
		return fmt.Errorf("Server error (HTTP %d). Try again later.", se.Status)
	}
	return fmt.Errorf("Unexpected response from server (HTTP %d).", se.Status)
}

// NewWebDAVSource builds a source rooted at rootPath on the given WebDAV
// server. Nothing is sent until the first List / Fetch / ReadDir call;
// use ProbeWebDAV up front to validate credentials.
func NewWebDAVSource(host, user, pass, rootPath string) *WebDAVSource {
	return &WebDAVSource{
		host:      host,
		auth:      gowebdav.NewAutoAuth(user, pass),
		transport: newTransport(),
		Root:      normaliseRoot(rootPath),
	}
}

// client returns a gowebdav client whose requests carry ctx. gowebdav
// builds its requests with http.NewRequest, so the context is attached
// at the transport layer instead; a cancelled or expired ctx aborts the
// in-flight request and any body still being read from it.
func (s *WebDAVSource) client(ctx context.Context) *gowebdav.Client {
	c := gowebdav.NewAuthClient(s.host, s.auth)
	c.SetHeader("User-Agent", "pocketbeam/"+version)
	c.SetTransport(ctxTransport{ctx: ctx, base: s.transport})
	return c
}

// ctxTransport rebinds every request to ctx before handing it to base.
type ctxTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t ctxTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req.WithContext(t.ctx))
}

func (s *WebDAVSource) List(ctx context.Context) ([]Book, error) {
	var out []Book
	if err := s.walk(ctx, s.Root, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *WebDAVSource) Fetch(ctx context.Context, b Book) (io.ReadCloser, error) {
	return s.client(ctx).ReadStream(b.URL)
}

// ReadDir lists one directory on the server. The directory picker uses
// it directly; walk uses it recursively.
func (s *WebDAVSource) ReadDir(ctx context.Context, dir string) ([]os.FileInfo, error) {
	return s.client(ctx).ReadDir(dir)
}

// walk recursively lists dir and appends every ebook/comic file to out.
// WebDAV doesn't have a standard recursive PROPFIND (Depth: infinity is
// often disabled server-side), so we iterate per-directory.
func (s *WebDAVSource) walk(ctx context.Context, dir string, out *[]Book) error {
	entries, err := s.ReadDir(ctx, dir)
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}
	for _, e := range entries {
		child := path.Join(dir, e.Name())
		if e.IsDir() {
			if err := s.walk(ctx, child, out); err != nil {
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
