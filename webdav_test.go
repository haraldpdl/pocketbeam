package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/studio-b12/gowebdav"
)

// webdavPropfindResponses maps a request path to the XML body the mock
// server returns. Only the fields pocketbeam actually reads
// (href, collection bit, getlastmodified, getcontentlength) are populated.
type webdavPropfindResponses map[string]string

// webdavFileContent is served verbatim for GET requests.
const webdavFileContent = "pretend epub bytes"

func newWebDAVMock(t *testing.T, responses webdavPropfindResponses) *httptest.Server {
	t.Helper()
	return httptest.NewServer(webdavMockHandler(responses))
}

func webdavMockHandler(responses webdavPropfindResponses) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})
}

// newStallingServer answers every request by holding the connection open
// until the test ends. With headersFirst the handler sends a 200 before
// stalling, which models a download whose body never arrives. Handlers
// are released from Cleanup rather than on the request context: the
// server only notices a client hang-up once it reads the body, which the
// stalled PROPFIND never does.
func newStallingServer(t *testing.T, headersFirst bool) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if headersFirst {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
		}
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv
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

func TestWebDAVSource_FetchStopsOnCancel(t *testing.T) {
	srv := newStallingServer(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := NewWebDAVSource(srv.URL, "", "", "/")
	body, err := src.Fetch(ctx, Book{URL: "/stalled.epub"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer body.Close()

	time.AfterFunc(50*time.Millisecond, cancel)
	_, err = io.ReadAll(body)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadAll after cancel = %v, want context.Canceled", err)
	}
}

func TestWebDAVSource_ListStopsOnDeadline(t *testing.T) {
	srv := newStallingServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := NewWebDAVSource(srv.URL, "", "", "/Books").List(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("List = %v, want context.DeadlineExceeded", err)
	}
}

// TestWebDAVSource_SharesNegotiatedAuth pins the property the per-call
// clients rely on: the auth method negotiated by the first request is
// reused by later requests instead of being re-challenged each time.
func TestWebDAVSource_SharesNegotiatedAuth(t *testing.T) {
	const stamp = "Mon, 02 Jan 2006 15:04:05 GMT"
	inner := webdavMockHandler(webdavPropfindResponses{
		"/Books/": propfindResponse([]webdavEntry{
			{href: "/Books/", isDir: true, mtime: stamp},
			{href: "/Books/only.epub", mtime: stamp},
		}),
	})
	var challenges atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "alice" || p != "hunter2" {
			challenges.Add(1)
			w.Header().Set("WWW-Authenticate", `Basic realm="dav"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()

	src := NewWebDAVSource(srv.URL, "alice", "hunter2", "/Books")
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
	body.Close()
	if got := challenges.Load(); got != 1 {
		t.Errorf("server challenged %d times, want 1 (auth negotiated once, then reused)", got)
	}
}

// TestWebDAVSource_SharesConnectionPool pins that ProbeWebDAV and a
// separately built sync source share one connection pool. Each caller
// builds its own WebDAVSource, so the pool has to live at package level
// (webdavTransport) or every probe and picker level would dial afresh
// and leave an idle connection behind in its own transport.
//
// The bound is 2, not 1: a fresh gowebdav Authorizer probes the server
// with an X-Gowebdav-Inhibit-Redirect request whose body it closes
// unread, which discards that connection, and the real request dials
// again. With the shared pool the sync source's negotiation reuses the
// probe's connection (2 in total); with a per-source transport it dials
// twice on its own (3).
func TestWebDAVSource_SharesConnectionPool(t *testing.T) {
	const stamp = "Mon, 02 Jan 2006 15:04:05 GMT"
	srv := httptest.NewUnstartedServer(webdavMockHandler(webdavPropfindResponses{
		"/Books/": propfindResponse([]webdavEntry{
			{href: "/Books/", isDir: true, mtime: stamp},
			{href: "/Books/only.epub", mtime: stamp},
		}),
	}))
	var conns atomic.Int32
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	if err := ProbeWebDAV(context.Background(), srv.URL, "", "", "/Books"); err != nil {
		t.Fatalf("ProbeWebDAV: %v", err)
	}
	books, err := NewWebDAVSource(srv.URL, "", "", "/Books").List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1", len(books))
	}
	if got := conns.Load(); got > 2 {
		t.Errorf("server saw %d connections, want at most 2 (probe and sync share the pool)", got)
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
		{405, "Unexpected response"},
		{500, "Server error (HTTP 500)"},
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
	t.Run("stalled server honours the caller's deadline", func(t *testing.T) {
		srv := newStallingServer(t, false)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := ProbeWebDAV(ctx, srv.URL, "u", "p", "/")
		if err == nil || !strings.Contains(err.Error(), "did not respond in time") {
			t.Errorf("got %v, want 'did not respond in time'", err)
		}
		if elapsed := time.Since(start); elapsed > probeTimeout/2 {
			t.Errorf("probe took %v, want it to stop at the caller's deadline", elapsed)
		}
	})
}

func TestClassifyWebDAVError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want string
	}{
		{"401", gowebdav.NewPathError("Connect", "/", 401), "rejected"},
		{"403", gowebdav.NewPathError("Connect", "/", 403), "no access"},
		{"404", gowebdav.NewPathError("Connect", "/", 404), "not found"},
		{"405", gowebdav.NewPathError("PROPFIND", "/", 405), "Unexpected response from server (HTTP 405)"},
		{"502", gowebdav.NewPathError("PROPFIND", "/", 502), "Server error (HTTP 502)"},
		{"deadline", &url.Error{Op: "Get", URL: "http://nas.lan/", Err: context.DeadlineExceeded}, "did not respond in time"},
		{"dns", &url.Error{Op: "Get", URL: "http://nas.lan/", Err: &net.DNSError{Err: "no such host", Name: "nas.lan", IsNotFound: true}}, "resolved"},
		{"x509", errors.New("x509: certificate signed by unknown authority"), "TLS handshake"},
		{"tls", errors.New("remote error: tls: handshake failure"), "TLS handshake"},
		{"refused", errors.New("dial tcp 10.0.0.2:80: connect: connection refused"), "refused"},
		// A port that happens to contain "401" must not read as an auth
		// failure; the classifier keys on the typed status, not the text.
		{"refused on port 4010", errors.New(`Get "http://nas.lan:4010/": dial tcp: connection refused`), "refused"},
		{"other", errors.New("something else entirely"), "Could not reach"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyWebDAVError(tc.in).Error()
			if !strings.Contains(got, tc.want) {
				t.Errorf("classifyWebDAVError(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
