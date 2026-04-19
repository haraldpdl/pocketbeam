package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const opdsNS = `xmlns="http://www.w3.org/2005/Atom"`

// newOPDSClient sets up a gowebdav-free OPDS Client pointed at the given
// mock server URL, suitable for unit tests.
func newOPDSClient(t *testing.T, base string) *Client {
	t.Helper()
	c, err := NewClient(base, "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestFetchLevel_SplitsSubsectionsFromBooks(t *testing.T) {
	const root = `<?xml version="1.0"?><feed ` + opdsNS + `>
        <title>My Library</title>
        <entry>
          <id>cat-authors</id><title>By author</title>
          <link rel="subsection" type="application/atom+xml" href="/opds/authors"/>
        </entry>
        <entry>
          <id>cat-shelves</id><title>Shelves</title>
          <link rel="subsection" type="application/atom+xml" href="/opds/shelfindex"/>
        </entry>
        <entry>
          <id>urn:uuid:abc</id><title>Inline Book</title>
          <author><name>Author</name></author>
          <updated>2026-01-01T00:00:00+00:00</updated>
          <link rel="http://opds-spec.org/acquisition" type="application/epub+zip" href="/opds/download/abc"/>
        </entry>
      </feed>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/opds" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(root))
	}))
	defer srv.Close()

	c := newOPDSClient(t, srv.URL)
	lvl, err := c.FetchLevel("/opds")
	if err != nil {
		t.Fatalf("FetchLevel: %v", err)
	}
	if lvl.FeedTitle != "My Library" {
		t.Errorf("FeedTitle = %q, want %q", lvl.FeedTitle, "My Library")
	}
	if len(lvl.Subsections) != 2 {
		t.Fatalf("got %d subsections, want 2: %+v", len(lvl.Subsections), lvl.Subsections)
	}
	if lvl.Subsections[0].Name != "By author" || lvl.Subsections[0].Href != "/opds/authors" {
		t.Errorf("subsections[0] = %+v", lvl.Subsections[0])
	}
	if lvl.BookCount != 1 {
		t.Errorf("BookCount = %d, want 1", lvl.BookCount)
	}
}

func TestFetchLevel_WalksPagination(t *testing.T) {
	const page1 = `<?xml version="1.0"?><feed ` + opdsNS + `>
        <title>Letter A</title>
        <link rel="next" href="/opds/books/letter/A?page=2"/>
        <entry>
          <id>urn:uuid:p1-1</id><title>Book P1-1</title>
          <link rel="http://opds-spec.org/acquisition" type="application/epub+zip" href="/d/1"/>
        </entry>
        <entry>
          <id>urn:uuid:p1-2</id><title>Book P1-2</title>
          <link rel="http://opds-spec.org/acquisition" type="application/epub+zip" href="/d/2"/>
        </entry>
      </feed>`
	const page2 = `<?xml version="1.0"?><feed ` + opdsNS + `>
        <title>Letter A</title>
        <entry>
          <id>urn:uuid:p2-1</id><title>Book P2-1</title>
          <link rel="http://opds-spec.org/acquisition" type="application/epub+zip" href="/d/3"/>
        </entry>
      </feed>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		switch r.URL.RequestURI() {
		case "/opds/books/letter/A":
			_, _ = w.Write([]byte(page1))
		case "/opds/books/letter/A?page=2":
			_, _ = w.Write([]byte(page2))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newOPDSClient(t, srv.URL)
	lvl, err := c.FetchLevel("/opds/books/letter/A")
	if err != nil {
		t.Fatalf("FetchLevel: %v", err)
	}
	if lvl.BookCount != 3 {
		t.Errorf("BookCount = %d, want 3 (2 + 1 across pagination)", lvl.BookCount)
	}
	if len(lvl.Subsections) != 0 {
		t.Errorf("got %d subsections, want 0", len(lvl.Subsections))
	}
}

func TestFetchLevel_EmptyHrefDefaultsToRoot(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><feed ` + opdsNS + `><title>Root</title></feed>`))
	}))
	defer srv.Close()

	c := newOPDSClient(t, srv.URL)
	if _, err := c.FetchLevel(""); err != nil {
		t.Fatalf("FetchLevel(empty): %v", err)
	}
	if !strings.HasPrefix(gotPath, "/opds") {
		t.Errorf("empty href did not default to /opds; server saw %q", gotPath)
	}
}
