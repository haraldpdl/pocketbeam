package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
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
	lvl, err := c.FetchLevel(context.Background(), "/opds")
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
	lvl, err := c.FetchLevel(context.Background(), "/opds/books/letter/A")
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

func TestFetchLevel_ParsesOPDSCount(t *testing.T) {
	const root = `<?xml version="1.0"?><feed ` + opdsNS + ` xmlns:opds="http://opds-spec.org/2010/catalog">
        <title>My Library</title>
        <entry>
          <id>full</id><title>Fantasy</title>
          <link rel="subsection" type="application/atom+xml" href="/opds/tag/3" opds:count="42"/>
        </entry>
        <entry>
          <id>empty</id><title>Unused Tag</title>
          <link rel="subsection" type="application/atom+xml" href="/opds/tag/4" opds:count="0"/>
        </entry>
        <entry>
          <id>unknown</id><title>Legacy</title>
          <link rel="subsection" type="application/atom+xml" href="/opds/legacy"/>
        </entry>
      </feed>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(root))
	}))
	defer srv.Close()

	c := newOPDSClient(t, srv.URL)
	lvl, err := c.FetchLevel(context.Background(), "/opds")
	if err != nil {
		t.Fatalf("FetchLevel: %v", err)
	}
	if len(lvl.Subsections) != 3 {
		t.Fatalf("got %d subsections, want 3: %+v", len(lvl.Subsections), lvl.Subsections)
	}
	full := lvl.Subsections[0]
	if !full.CountKnown || full.Count != 42 {
		t.Errorf("Fantasy: got CountKnown=%v Count=%d, want true/42", full.CountKnown, full.Count)
	}
	empty := lvl.Subsections[1]
	if !empty.CountKnown || empty.Count != 0 {
		t.Errorf("Unused Tag: got CountKnown=%v Count=%d, want true/0", empty.CountKnown, empty.Count)
	}
	unknown := lvl.Subsections[2]
	if unknown.CountKnown {
		t.Errorf("Legacy: got CountKnown=true, want false when attribute is absent")
	}
}

func TestLevelBookCount(t *testing.T) {
	cases := []struct {
		name  string
		lvl   OPDSLevel
		wantN int
		wantA bool
	}{
		{
			"leaf_with_exact_books",
			OPDSLevel{BookCount: 12},
			12, false,
		},
		{
			"shelfindex_style_all_known",
			OPDSLevel{
				BookCount: 0,
				Subsections: []FilterOption{
					{Name: "to-pocketbook", Count: 2, CountKnown: true},
					{Name: "Fantasy", Count: 40, CountKnown: true},
				},
			},
			42, true,
		},
		{
			"any_unknown_falls_back",
			OPDSLevel{
				Subsections: []FilterOption{
					{Name: "Authors", Count: 0, CountKnown: false},
					{Name: "Tags", Count: 10, CountKnown: true},
				},
			},
			0, false,
		},
		{
			"empty_level",
			OPDSLevel{},
			0, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, a := levelBookCount(tc.lvl)
			if n != tc.wantN || a != tc.wantA {
				t.Errorf("levelBookCount = (%d, %v), want (%d, %v)", n, a, tc.wantN, tc.wantA)
			}
		})
	}
}

func TestBookFromEntry_ParsesLength(t *testing.T) {
	const feedXML = `<?xml version="1.0"?><feed ` + opdsNS + `>
        <entry>
          <id>urn:uuid:with-size</id><title>Sized</title>
          <author><name>A</name></author>
          <updated>2026-01-01T00:00:00+00:00</updated>
          <link rel="http://opds-spec.org/acquisition" type="application/epub+zip"
                href="/d/1" length="3147045"/>
        </entry>
        <entry>
          <id>urn:uuid:no-size</id><title>Unknown</title>
          <author><name>A</name></author>
          <updated>2026-01-01T00:00:00+00:00</updated>
          <link rel="http://opds-spec.org/acquisition" type="application/epub+zip" href="/d/2"/>
        </entry>
        <entry>
          <id>urn:uuid:bad-size</id><title>Garbage</title>
          <author><name>A</name></author>
          <updated>2026-01-01T00:00:00+00:00</updated>
          <link rel="http://opds-spec.org/acquisition" type="application/epub+zip"
                href="/d/3" length="not-a-number"/>
        </entry>
      </feed>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(feedXML))
	}))
	defer srv.Close()

	c := newOPDSClient(t, srv.URL)
	books, err := c.walk(context.Background(), "/")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(books) != 3 {
		t.Fatalf("got %d books, want 3", len(books))
	}
	byUUID := map[string]Book{}
	for _, b := range books {
		byUUID[b.UUID] = b
	}
	if byUUID["with-size"].Size != 3147045 {
		t.Errorf("with-size: Size=%d, want 3147045", byUUID["with-size"].Size)
	}
	if byUUID["no-size"].Size != 0 {
		t.Errorf("no-size: Size=%d, want 0", byUUID["no-size"].Size)
	}
	if byUUID["bad-size"].Size != 0 {
		t.Errorf("bad-size: Size=%d, want 0 for unparseable length", byUUID["bad-size"].Size)
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
	if _, err := c.FetchLevel(context.Background(), ""); err != nil {
		t.Fatalf("FetchLevel(empty): %v", err)
	}
	if !strings.HasPrefix(gotPath, "/opds") {
		t.Errorf("empty href did not default to /opds; server saw %q", gotPath)
	}
}

func TestRejectSchemeDowngrade(t *testing.T) {
	mk := func(raw string) *http.Request {
		r, err := http.NewRequest("GET", raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	via := func(n int, raw string) []*http.Request {
		out := make([]*http.Request, n)
		for i := range out {
			out[i] = mk(raw)
		}
		return out
	}
	cases := []struct {
		name    string
		next    string
		via     []*http.Request
		wantErr string
	}{
		{"first request has no history", "http://cwa/opds", nil, ""},
		{"https to https", "https://cwa/opds/", via(1, "https://cwa/opds"), ""},
		{"http to http", "http://cwa/opds/", via(1, "http://cwa/opds"), ""},
		{"http upgraded to https", "https://cwa/opds", via(1, "http://cwa/opds"), ""},
		{"https downgraded to http", "http://cwa/opds", via(1, "https://cwa/opds"), "refusing redirect"},
		// The downgrade check looks at the origin, not the last hop.
		{"downgrade after an https hop", "http://cwa/x", via(2, "https://cwa/opds"), "refusing redirect"},
		{"nine hops still allowed", "https://cwa/x", via(9, "https://cwa/opds"), ""},
		{"ten hops is a loop", "https://cwa/x", via(10, "https://cwa/opds"), "too many redirects"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectSchemeDowngrade(mk(tc.next), tc.via)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("got %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("got %v, want %q", err, tc.wantErr)
			}
		})
	}
	// Credentials pasted into the URL must not surface in the error.
	err := rejectSchemeDowngrade(mk("http://alice:hunter2@cwa/opds"), via(1, "https://cwa/opds"))
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("got %v, want a downgrade error without the password", err)
	}
}

// The redirect policy is wired into the client's http.Client, so a
// cleartext hop out of an https origin fails the request itself.
func TestClient_FollowsRedirectsWithinScheme(t *testing.T) {
	const feed = `<?xml version="1.0"?><feed ` + opdsNS + `><title>Moved</title></feed>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/opds":
			http.Redirect(w, r, "/opds/", http.StatusMovedPermanently)
		case "/opds/":
			_, _ = w.Write([]byte(feed))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	lvl, err := newOPDSClient(t, srv.URL).FetchLevel(context.Background(), "/opds")
	if err != nil {
		t.Fatalf("FetchLevel through redirect: %v", err)
	}
	if lvl.FeedTitle != "Moved" {
		t.Errorf("FeedTitle = %q, want Moved", lvl.FeedTitle)
	}
}

// blockingOPDSServer serves feeds like newOPDSMock but stalls every request
// matching stall until the client gives up, sending the stalled path on
// arrived first so a test can cancel deterministically once the client is
// waiting on the server.
func blockingOPDSServer(t *testing.T, feeds opdsFeeds, stall func(*http.Request) bool) (*httptest.Server, <-chan string, func() []string) {
	t.Helper()
	arrived := make(chan string, 16)
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if stall(r) {
			arrived <- r.URL.RequestURI()
			<-r.Context().Done()
			return
		}
		body, ok := feeds[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, arrived, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paths)
	}
}

func TestFetchLevel_CancelAbortsStalledRequest(t *testing.T) {
	srv, arrived, _ := blockingOPDSServer(t, nil, func(*http.Request) bool { return true })
	c := newOPDSClient(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.FetchLevel(ctx, "/opds")
		done <- err
	}()

	<-arrived
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FetchLevel returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FetchLevel did not return after cancel; request ignores context")
	}
}

func TestFetchLevel_DeadlineCoversPagination(t *testing.T) {
	// Page one answers, page two stalls: the deadline bounds the whole
	// level walk, not just the first request.
	feeds := opdsFeeds{"/opds": `<?xml version="1.0"?><feed ` + opdsNS + `><title>Root</title>` +
		`<link rel="next" href="/opds?page=2"/></feed>`}
	srv, arrived, _ := blockingOPDSServer(t, feeds, func(r *http.Request) bool { return r.URL.RawQuery == "page=2" })
	c := newOPDSClient(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := c.FetchLevel(ctx, "/opds")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("FetchLevel returned %v, want context.DeadlineExceeded", err)
	}
	select {
	case uri := <-arrived:
		if uri != "/opds?page=2" {
			t.Errorf("stalled request was %q, want the second page", uri)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second page never reached the server; page one did not paginate")
	}
}

func TestWalkGeneric_SubFeedErrorAbortsListing(t *testing.T) {
	// /opds/b is not served (404). The walk must fail rather than return
	// the books from /opds/a alone: a partial listing would make
	// delete-missing propose every book under /opds/b for deletion.
	feeds := opdsFeeds{
		"/opds": `<?xml version="1.0"?><feed ` + opdsNS + `><title>Root</title>
		  <entry><id>a</id><title>A</title><link rel="subsection" type="application/atom+xml" href="/opds/a"/></entry>
		  <entry><id>b</id><title>B</title><link rel="subsection" type="application/atom+xml" href="/opds/b"/></entry>
		  <entry><id>c</id><title>C</title><link rel="subsection" type="application/atom+xml" href="/opds/c"/></entry>
		</feed>`,
		"/opds/a": `<?xml version="1.0"?><feed ` + opdsNS + `><title>A</title>
		  <entry><id>urn:uuid:book-a</id><title>Book A</title>
		    <link rel="http://opds-spec.org/acquisition" type="application/epub+zip" href="/dl/a"/>
		  </entry>
		</feed>`,
		"/opds/c": `<?xml version="1.0"?><feed ` + opdsNS + `><title>C</title></feed>`,
	}
	srv, _, paths := blockingOPDSServer(t, feeds, func(*http.Request) bool { return false })
	c := newOPDSClient(t, srv.URL)

	books, err := c.WalkAll(context.Background())
	if err == nil {
		t.Fatalf("WalkAll returned %d books and no error, want an error for the failed sub-feed", len(books))
	}
	if !strings.Contains(err.Error(), "/opds/b") {
		t.Errorf("error %q does not name the failed sub-feed", err)
	}
	if books != nil {
		t.Errorf("WalkAll returned partial books %+v alongside the error", books)
	}
	if slices.Contains(paths(), "/opds/c") {
		t.Errorf("walk continued to /opds/c after /opds/b failed; requests: %v", paths())
	}
}
