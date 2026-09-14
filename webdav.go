package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
// client). The negotiated auth method lives in the source's Authorizer
// and the connection pool in webdavTransport, so per-call clients cost
// nothing extra on the wire.
type WebDAVSource struct {
	host string
	// secure is true when host is an https:// URL, which makes every
	// plaintext request from this source a downgrade (see guardedTransport).
	secure bool
	auth   gowebdav.Authorizer
	Root   string // absolute path on the server, e.g. "/Books/Fiction"
}

// webdavTransport is the one connection pool for every WebDAV client.
// ProbeWebDAV, the directory picker and the sync source each build their
// own WebDAVSource; sharing the pool lets the listing reuse the probe's
// keep-alive connection instead of paying a fresh TCP+TLS handshake per
// source and leaving an idle connection behind for each one.
var webdavTransport = newTransport()

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
		host:   host,
		secure: isHTTPSURL(host),
		auth:   gowebdav.NewAutoAuth(user, pass),
		Root:   normaliseRoot(rootPath),
	}
}

// client returns a gowebdav client whose requests carry ctx. gowebdav
// builds its requests with http.NewRequest, so the context is attached
// at the transport layer instead; a cancelled or expired ctx aborts the
// in-flight request and any body still being read from it.
func (s *WebDAVSource) client(ctx context.Context) *gowebdav.Client {
	c := gowebdav.NewAuthClient(s.host, s.auth)
	c.SetHeader("User-Agent", "pocketbeam/"+version)
	c.SetTransport(guardedTransport{ctx: ctx, base: webdavTransport, secure: s.secure})
	return c
}

// guardedTransport rebinds every request to ctx before handing it to
// base, and refuses to send one in the clear when the source is https.
//
// The clear-text check is the transport's job here because gowebdav
// builds its own http.Client and exposes no hook for its CheckRedirect,
// which is where newHTTPClient puts rejectSchemeDowngrade for OPDS and
// the updater. It needs no redirect history: every request a client
// makes starts at the source host, so an http:// request from an https
// source is by definition a downgraded hop, and Go replays the
// Authorization header across a same-host redirect, which would put the
// WebDAV password on the wire in clear.
type guardedTransport struct {
	ctx    context.Context
	base   http.RoundTripper
	secure bool
}

func (t guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.secure && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("%w (%s)", errSchemeDowngrade, redactURL(req.URL))
	}
	return t.base.RoundTrip(req.WithContext(t.ctx))
}

// isHTTPSURL reports whether raw is an https:// URL. An unparsable or
// schemeless value is not https, and gowebdav rejects it later with its
// own error.
func isHTTPSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https"
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
