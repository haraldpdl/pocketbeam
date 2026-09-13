package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// webdavPropfindResponses maps a request path to the XML body the mock
// server returns. Only the fields pocketbeam actually reads
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

func TestWebDAVSource_CarriesSizeFromPropfind(t *testing.T) {
	const stamp = "Mon, 02 Jan 2006 15:04:05 GMT"
	responses := webdavPropfindResponses{
		"/Books/": propfindResponse([]webdavEntry{
			{href: "/Books/", isDir: true, mtime: stamp},
			{href: "/Books/one.epub", mtime: stamp},
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
	// propfindResponse hardcodes getcontentlength=10 for every file entry.
	if books[0].Size != 10 {
		t.Errorf("Size=%d, want 10 (from getcontentlength)", books[0].Size)
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

func TestProbeWebDAV(t *testing.T) {
	const stamp = "Mon, 02 Jan 2006 15:04:05 GMT"
	listing := propfindResponse([]webdavEntry{{href: "/Books/", isDir: true, mtime: stamp}})

	t.Run("success", func(t *testing.T) {
		srv := newWebDAVMock(t, webdavPropfindResponses{"/Books/": listing})
		defer srv.Close()
		if err := ProbeWebDAV(context.Background(), srv.URL, "u", "p", "Books/"); err != nil {
			t.Errorf("ProbeWebDAV = %v, want nil", err)
		}
	})
	t.Run("root not listable", func(t *testing.T) {
		srv := newWebDAVMock(t, webdavPropfindResponses{})
		defer srv.Close()
		err := ProbeWebDAV(context.Background(), srv.URL, "u", "p", "/Books")
		if err == nil || !strings.Contains(err.Error(), "Could not list /Books") {
			t.Errorf("got %v, want 'Could not list /Books'", err)
		}
	})
	statusCases := []struct {
		status     int
		wantSubstr string
	}{
		{401, "rejected"},
		{403, "no access"},
		{404, "not found"},
		{500, "Could not reach"},
	}
	for _, tc := range statusCases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			err := ProbeWebDAV(context.Background(), srv.URL, "u", "p", "/")
			if err == nil || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("status %d: got %v, want %q", tc.status, err, tc.wantSubstr)
			}
		})
	}
	t.Run("connection refused", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		err := ProbeWebDAV(context.Background(), url, "u", "p", "/")
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Errorf("got %v, want 'refused'", err)
		}
	})
}

func TestClassifyWebDAVError(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Connect /: 401", "rejected"},
		{"Connect /: 403", "no access"},
		{"Connect /: 404", "not found"},
		{"x509: certificate signed by unknown authority", "TLS handshake"},
		{"remote error: tls: handshake failure", "TLS handshake"},
		{"dial tcp: lookup nas.lan: no such host", "resolved"},
		{"dial tcp 10.0.0.2:80: connect: connection refused", "refused"},
		{"something else entirely", "Could not reach"},
	}
	for _, tc := range cases {
		got := classifyWebDAVError(errors.New(tc.in)).Error()
		if !strings.Contains(got, tc.want) {
			t.Errorf("classifyWebDAVError(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
