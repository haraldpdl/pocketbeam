package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
)

// opdsFeeds is a path -> Atom body table served by newOPDSMock. Any path
// not in the table returns 404, which is how the walkers' error paths
// get exercised.
type opdsFeeds map[string]string

// newOPDSMock serves canned OPDS feeds and counts every request so a
// test can assert which paths a walk touched (and that constructors
// touch none).
func newOPDSMock(t *testing.T, feeds opdsFeeds) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		body, ok := feeds[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func bookEntry(uuid, title string) string {
	return `<entry><id>urn:uuid:` + uuid + `</id><title>` + title + `</title>` +
		`<author><name>A</name></author><updated>2026-01-01T00:00:00Z</updated>` +
		`<link rel="http://opds-spec.org/acquisition" type="application/epub+zip" href="/opds/download/` + uuid + `"/></entry>`
}

func navEntry(title, href string) string {
	return `<entry><id>` + href + `</id><title>` + title + `</title>` +
		`<link rel="subsection" type="application/atom+xml" href="` + href + `"/></entry>`
}

func feedXML(title, entries string) string {
	return `<?xml version="1.0"?><feed ` + opdsNS + `><title>` + title + `</title>` + entries + `</feed>`
}

// cwaFeeds mimics the shape of a Calibre-Web catalog: the root advertises
// the alphabetical letter index and the shelf index, "All books" lives
// under /opds/books/letter/00 and each shelf is a flat feed.
func cwaFeeds() opdsFeeds {
	return opdsFeeds{
		"/opds": feedXML("Calibre-Web", navEntry("Books", "/opds/books/letter/00")+
			navEntry("Shelves", "/opds/shelfindex")+navEntry("Authors", "/opds/author")),
		"/opds/books/letter/00": feedXML("All", bookEntry("a1", "One")+bookEntry("a2", "Two")),
		"/opds/shelf/3":         feedXML("Shelf", bookEntry("a1", "One")+navEntry("Trap", "/opds/author")),
		"/opds/author":          feedXML("Authors", bookEntry("zz", "Should not be walked")),
	}
}

func TestNewSourceDispatch(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cases := []struct {
		name    string
		cfg     *Config
		want    string
		wantErr bool
	}{
		{"opds", &Config{Backend: BackendOPDS, Host: srv.URL, Library: "/lib", StateDB: "/db"}, "*main.OPDSSource", false},
		{"webdav", &Config{Backend: BackendWebDAV, Host: srv.URL, Library: "/lib", StateDB: "/db", Path: "/Books"}, "*main.WebDAVSource", false},
		// Empty backend falls back to OPDS for back-compat with old configs.
		{"legacy", &Config{Host: srv.URL, Library: "/lib", StateDB: "/db"}, "*main.OPDSSource", false},
		{"bogus", &Config{Backend: "bogus", Host: srv.URL, Library: "/", StateDB: "/"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := newSource(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("newSource(%q) = %T, want error", tc.cfg.Backend, src)
				}
				return
			}
			if err != nil {
				t.Fatalf("newSource: %v", err)
			}
			if got := fmt.Sprintf("%T", src); got != tc.want {
				t.Errorf("newSource produced %s, want %s", got, tc.want)
			}
		})
	}
	// Constructing a source must not touch the network: the UI builds one
	// per sync and a stalled DNS lookup here would block before any
	// progress is visible.
	if n := hits.Load(); n != 0 {
		t.Errorf("newSource made %d HTTP requests, want 0", n)
	}
}

func TestOPDSSource_ListDetectsServerThenWalks(t *testing.T) {
	srv, paths := newOPDSMock(t, cwaFeeds())
	src, err := newSource(&Config{Backend: BackendOPDS, Host: srv.URL, Library: "/lib", StateDB: "/db"})
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	books, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(books) != 2 {
		t.Fatalf("got %d books, want 2 from the letter feed: %+v", len(books), books)
	}
	want := []string{"/opds", "/opds/books/letter/00"}
	if !slices.Equal(*paths, want) {
		t.Errorf("requests = %v, want detection then CWA fast path %v", *paths, want)
	}
}

func TestOPDSSource_ListDedupesAcrossFilters(t *testing.T) {
	srv, _ := newOPDSMock(t, cwaFeeds())
	src := &OPDSSource{Client: newOPDSClient(t, srv.URL), FilterHrefs: []string{"/opds/shelf/3", "/opds/books/letter/00"}}
	books, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// a1 appears in both feeds and must be listed once.
	if len(books) != 2 {
		t.Errorf("got %d books, want 2 (a1 deduped): %+v", len(books), books)
	}
}

func TestWalkAll_DispatchesOnServerType(t *testing.T) {
	t.Run("cwa takes the letter feed", func(t *testing.T) {
		srv, paths := newOPDSMock(t, cwaFeeds())
		c := newOPDSClient(t, srv.URL)
		c.IsCWA = true
		books, err := c.WalkAll()
		if err != nil {
			t.Fatalf("WalkAll: %v", err)
		}
		if len(books) != 2 || !slices.Equal(*paths, []string{"/opds/books/letter/00"}) {
			t.Errorf("books=%d requests=%v, want 2 books from the single letter feed", len(books), *paths)
		}
	})
	t.Run("generic recurses from the root", func(t *testing.T) {
		feeds := opdsFeeds{
			"/opds":         feedXML("Generic", navEntry("New", "/opds/new")+navEntry("Loop", "/opds")),
			"/opds/new":     feedXML("New", bookEntry("n1", "One")+navEntry("Deeper", "/opds/new/sub")),
			"/opds/new/sub": feedXML("Sub", bookEntry("n1", "One again")+bookEntry("n2", "Two")),
		}
		srv, paths := newOPDSMock(t, feeds)
		books, err := newOPDSClient(t, srv.URL).WalkAll()
		if err != nil {
			t.Fatalf("WalkAll: %v", err)
		}
		// n1 is reachable twice and must be deduped; the self-link back to
		// /opds must not be followed again.
		if len(books) != 2 {
			t.Errorf("got %d books, want 2: %+v", len(books), books)
		}
		if !slices.Equal(*paths, []string{"/opds", "/opds/new", "/opds/new/sub"}) {
			t.Errorf("requests = %v, want each feed exactly once", *paths)
		}
	})
}

func TestWalkFiltered_ShelfFastPathOnlyOnCWA(t *testing.T) {
	t.Run("cwa shelf is a flat feed", func(t *testing.T) {
		srv, paths := newOPDSMock(t, cwaFeeds())
		c := newOPDSClient(t, srv.URL)
		c.IsCWA = true
		books, err := c.WalkFiltered("/opds/shelf/3")
		if err != nil {
			t.Fatalf("WalkFiltered: %v", err)
		}
		if len(books) != 1 || !slices.Equal(*paths, []string{"/opds/shelf/3"}) {
			t.Errorf("books=%d requests=%v, want 1 book without following the subsection link", len(books), *paths)
		}
	})
	t.Run("generic server recurses into the shelf", func(t *testing.T) {
		srv, paths := newOPDSMock(t, cwaFeeds())
		books, err := newOPDSClient(t, srv.URL).WalkFiltered("/opds/shelf/3")
		if err != nil {
			t.Fatalf("WalkFiltered: %v", err)
		}
		if len(books) != 2 || !slices.Equal(*paths, []string{"/opds/shelf/3", "/opds/author"}) {
			t.Errorf("books=%d requests=%v, want the subsection walked too", len(books), *paths)
		}
	})
	t.Run("empty filter means everything", func(t *testing.T) {
		srv, paths := newOPDSMock(t, cwaFeeds())
		c := newOPDSClient(t, srv.URL)
		c.IsCWA = true
		if _, err := c.WalkFiltered(""); err != nil {
			t.Fatalf("WalkFiltered: %v", err)
		}
		if !slices.Equal(*paths, []string{"/opds/books/letter/00"}) {
			t.Errorf("requests = %v, want WalkAll's letter feed", *paths)
		}
	})
}

func TestWalkShelf_RequiresCWA(t *testing.T) {
	srv, paths := newOPDSMock(t, cwaFeeds())
	c := newOPDSClient(t, srv.URL)
	if _, err := c.WalkShelf(3); err == nil {
		t.Error("WalkShelf on a generic server should error")
	}
	if len(*paths) != 0 {
		t.Errorf("requests = %v, want none before the CWA check", *paths)
	}
	c.IsCWA = true
	books, err := c.WalkShelf(3)
	if err != nil {
		t.Fatalf("WalkShelf: %v", err)
	}
	if len(books) != 1 {
		t.Errorf("got %d books, want 1", len(books))
	}
}

func TestDetectType(t *testing.T) {
	cases := []struct {
		name string
		root string
		want bool
	}{
		{"title names calibre-web", feedXML("Calibre-Web Automated", ""), true},
		{"letter index link", feedXML("Books", navEntry("Books", "/opds/books/letter/00")), true},
		{"shelfindex link", feedXML("Books", navEntry("Shelves", "/opds/shelfindex")), true},
		{"generic catalog", feedXML("Books", navEntry("New", "/opds/new")+bookEntry("x", "X")), false},
		// A non-subsection link to a CWA path is not a signature; only
		// navigation entries count.
		{"letter path on acquisition link", feedXML("Books", `<entry><id>x</id><title>X</title><link rel="http://opds-spec.org/acquisition" type="application/epub+zip" href="/opds/books/letter/00"/></entry>`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newOPDSMock(t, opdsFeeds{"/opds": tc.root})
			c := newOPDSClient(t, srv.URL)
			if err := c.DetectType(); err != nil {
				t.Fatalf("DetectType: %v", err)
			}
			if c.IsCWA != tc.want {
				t.Errorf("IsCWA = %v, want %v", c.IsCWA, tc.want)
			}
		})
	}
	t.Run("http error is reported and leaves IsCWA false", func(t *testing.T) {
		srv, _ := newOPDSMock(t, opdsFeeds{})
		c := newOPDSClient(t, srv.URL)
		if err := c.DetectType(); err == nil || c.IsCWA {
			t.Errorf("err=%v IsCWA=%v, want 404 error and IsCWA=false", err, c.IsCWA)
		}
	})
	t.Run("malformed feed is reported", func(t *testing.T) {
		srv, _ := newOPDSMock(t, opdsFeeds{"/opds": "<feed><title>Calibre-Web</title>"})
		c := newOPDSClient(t, srv.URL)
		if err := c.DetectType(); err == nil || c.IsCWA {
			t.Errorf("err=%v IsCWA=%v, want parse error and IsCWA=false", err, c.IsCWA)
		}
	})
}
