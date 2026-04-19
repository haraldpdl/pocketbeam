package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// webdavPropfindResponses maps a request path to the XML body the mock
// server returns. Only the fields bookbeam actually reads
// (href, collection bit, getlastmodified, getcontentlength) are populated.
type webdavPropfindResponses map[string]string

// webdavFileContent is served verbatim for GET requests.
const webdavFileContent = "pretend epub bytes"

func newWebDAVMock(t *testing.T, responses webdavPropfindResponses) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "OPTIONS":
			w.Header().Set("DAV", "1, 2")
			w.Header().Set("Allow", "OPTIONS, GET, PROPFIND")
			w.WriteHeader(http.StatusOK)
		case "PROPFIND":
			body, ok := responses[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, body)
		case "GET":
			w.Header().Set("Content-Type", "application/epub+zip")
			_, _ = io.WriteString(w, webdavFileContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
}

// propfindResponse renders a PROPFIND response body from a flat description
// of entries. The first entry is treated as the directory itself.
func propfindResponse(entries []webdavEntry) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<D:multistatus xmlns:D="DAV:">`)
	for _, e := range entries {
		b.WriteString(`<D:response>`)
		b.WriteString(`<D:href>` + e.href + `</D:href>`)
		b.WriteString(`<D:propstat><D:prop>`)
		if e.isDir {
			b.WriteString(`<D:resourcetype><D:collection/></D:resourcetype>`)
		} else {
			b.WriteString(`<D:resourcetype/>`)
			b.WriteString(`<D:getcontentlength>10</D:getcontentlength>`)
		}
		b.WriteString(`<D:getlastmodified>` + e.mtime + `</D:getlastmodified>`)
		b.WriteString(`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`)
		b.WriteString(`</D:response>`)
	}
	b.WriteString(`</D:multistatus>`)
	return b.String()
}

type webdavEntry struct {
	href  string
	isDir bool
	mtime string
}

func TestWebDAVSource_ListWalksDirectory(t *testing.T) {
	const stamp = "Mon, 02 Jan 2006 15:04:05 GMT"
	responses := webdavPropfindResponses{
		"/Books/": propfindResponse([]webdavEntry{
			{href: "/Books/", isDir: true, mtime: stamp},
			{href: "/Books/Authorless.epub", mtime: stamp},
			{href: "/Books/Tolkien/", isDir: true, mtime: stamp},
			{href: "/Books/ignore.txt", mtime: stamp},
		}),
		"/Books/Tolkien/": propfindResponse([]webdavEntry{
			{href: "/Books/Tolkien/", isDir: true, mtime: stamp},
			{href: "/Books/Tolkien/The%20Hobbit.epub", mtime: stamp},
			{href: "/Books/Tolkien/Silmarillion.pdf", mtime: stamp},
		}),
	}
	srv := newWebDAVMock(t, responses)
	defer srv.Close()

	src := NewWebDAVSource(srv.URL, "", "", "/Books")
	books, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Order isn't guaranteed by the backend; sort by title for the assertion.
	sort.Slice(books, func(i, j int) bool { return books[i].Title < books[j].Title })
	want := []struct {
		title, author, format string
	}{
		{"Authorless", "", "application/epub+zip"},
		{"Silmarillion", "Tolkien", "application/pdf"},
		{"The Hobbit", "Tolkien", "application/epub+zip"},
	}
	if len(books) != len(want) {
		t.Fatalf("got %d books, want %d: %+v", len(books), len(want), books)
	}
	for i, w := range want {
		if books[i].Title != w.title || books[i].Author != w.author || books[i].Format != w.format {
			t.Errorf("book[%d] = %+v, want title=%q author=%q format=%q", i, books[i], w.title, w.author, w.format)
		}
		if !strings.HasPrefix(books[i].UUID, "webdav:") {
			t.Errorf("book[%d].UUID = %q, want webdav: prefix", i, books[i].UUID)
		}
	}
}

func TestWebDAVSource_FetchStreamsBody(t *testing.T) {
	const stamp = "Mon, 02 Jan 2006 15:04:05 GMT"
	responses := webdavPropfindResponses{
		"/Books/": propfindResponse([]webdavEntry{
			{href: "/Books/", isDir: true, mtime: stamp},
			{href: "/Books/only.epub", mtime: stamp},
		}),
	}
	srv := newWebDAVMock(t, responses)
	defer srv.Close()

	src := NewWebDAVSource(srv.URL, "", "", "/Books")
	books, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1", len(books))
	}
	body, err := src.Fetch(context.Background(), books[0])
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != webdavFileContent {
		t.Errorf("body = %q, want %q", got, webdavFileContent)
	}
}

func TestNewSourceDispatch(t *testing.T) {
	opdsCfg := &Config{
		Backend: BackendOPDS,
		Host:    "http://example:8083",
		Library: "/lib",
		StateDB: "/db",
	}
	src, err := newSource(opdsCfg)
	if err != nil {
		t.Fatalf("newSource(opds): %v", err)
	}
	if _, ok := src.(*OPDSSource); !ok {
		t.Errorf("OPDS backend produced %T, want *OPDSSource", src)
	}
	webdavCfg := &Config{
		Backend: BackendWebDAV,
		Host:    "https://example/dav",
		Library: "/lib",
		StateDB: "/db",
		Path:    "/Books",
	}
	src, err = newSource(webdavCfg)
	if err != nil {
		t.Fatalf("newSource(webdav): %v", err)
	}
	if _, ok := src.(*WebDAVSource); !ok {
		t.Errorf("WebDAV backend produced %T, want *WebDAVSource", src)
	}
	if _, err := newSource(&Config{Backend: "bogus", Host: "x", Library: "/", StateDB: "/"}); err == nil {
		t.Errorf("unknown backend should error")
	}
	// Empty backend falls back to OPDS for back-compat with old configs.
	legacyCfg := &Config{Host: "http://example:8083", Library: "/lib", StateDB: "/db"}
	src, err = newSource(legacyCfg)
	if err != nil {
		t.Fatalf("newSource(legacy): %v", err)
	}
	if _, ok := src.(*OPDSSource); !ok {
		t.Errorf("empty backend produced %T, want *OPDSSource", src)
	}
}

func TestNormaliseRoot(t *testing.T) {
	cases := map[string]string{
		"":            "/",
		"/":           "/",
		"Books":       "/Books",
		"/Books":      "/Books",
		"/Books/":     "/Books",
		"Books/":      "/Books",
		"/Books/sub/": "/Books/sub",
	}
	for in, want := range cases {
		if got := normaliseRoot(in); got != want {
			t.Errorf("normaliseRoot(%q) = %q, want %q", in, got, want)
		}
	}
}
