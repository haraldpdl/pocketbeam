package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AcquisitionRel is the OPDS link rel for downloadable content.
const AcquisitionRel = "http://opds-spec.org/acquisition"

// preferredFormats are acquisition mime types we'll download, in priority order.
// The PocketBook Era Color reads all of these natively.
var preferredFormats = []string{
	"application/epub+zip",
	"application/x-cbz",
	"application/x-cbr",
	"application/pdf",
}

type feed struct {
	XMLName xml.Name `xml:"feed"`
	Title   string   `xml:"title"`
	Entries []entry  `xml:"entry"`
	Links   []link   `xml:"link"`
}

type entry struct {
	ID      string `xml:"id"`
	Title   string `xml:"title"`
	Updated string `xml:"updated"`
	Author  struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Links []link `xml:"link"`
}

type link struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
	// Count is the OPDS:count attribute Calibre-Web attaches to
	// navigation links ("how many items behind this link"). Namespace-
	// agnostic match: any `count="N"` on the link parses here. Empty
	// when the server didn't attach one.
	Count string `xml:"count,attr"`
	// Length is the byte size of the acquisition resource, from the
	// optional `length` attribute on acquisition links. CWA/Calibre-Web
	// emits it; generic OPDS may not. Empty when absent.
	Length string `xml:"length,attr"`
}

// Book is one acquirable item flattened out of the OPDS catalog.
type Book struct {
	UUID    string // from <id>, urn:uuid: prefix stripped
	Title   string
	Author  string
	Updated time.Time // from <updated>
	URL     string    // absolute acquisition URL
	Format  string    // mime type
	// Size is the resource length in bytes when the source advertised it
	// (OPDS `length` attribute, WebDAV `getcontentlength`). 0 means
	// unknown; callers treat unknown as "don't count in the space
	// estimate" rather than zero.
	Size int64
}

// Client fetches and walks an OPDS catalog.
type Client struct {
	Base *url.URL
	User string
	Pass string
	HTTP *http.Client

	// IsCWA is true when the server exposes Calibre-Web / Calibre-Web
	// Automated-specific navigation (shelfindex, alphabetical letter list).
	// Set by DetectType. When false the generic recursive walker is used.
	IsCWA bool
}

func NewClient(base, user, pass string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	return &Client{
		Base: u,
		User: user,
		Pass: pass,
		HTTP: &http.Client{
			Transport:     newTransport(),
			CheckRedirect: rejectSchemeDowngrade,
		},
	}, nil
}

// rejectSchemeDowngrade blocks redirects from https:// to http://. A
// compromised or misconfigured proxy could otherwise strip TLS and expose
// Basic auth credentials in cleartext.
func rejectSchemeDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	origin := via[0].URL
	if origin.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("refusing redirect from https to http (%s)", redactURL(req.URL))
	}
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	return nil
}

// redactURL returns a URL string with any embedded userinfo stripped. OPDS
// base URLs normally carry auth via headers, but a user pasting
// `https://user:pass@host/` would otherwise leak credentials into error
// messages and logs.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	if u.User != nil {
		cp := *u
		cp.User = nil
		return cp.String()
	}
	return u.String()
}

// get fetches one feed page. The context bounds the request so a caller
// (sync cancel, picker timeout) can abort a listing that would otherwise
// sit on the 5-minute response-header timeout.
func (c *Client) get(ctx context.Context, href string) ([]byte, error) {
	abs, err := c.Base.Parse(href)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", abs.String(), nil)
	if err != nil {
		return nil, err
	}
	c.prepare(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", redactURL(abs), resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// Fetch downloads a single URL, used for book acquisition. The caller streams
// the body to disk; the returned ReadCloser must be closed. The context
// bounds the whole transfer (headers + body); cancel it to abort a stalled
// download, and set a deadline to cap wall-clock transfer time.
func (c *Client) Fetch(ctx context.Context, rawurl string) (io.ReadCloser, error) {
	abs, err := c.Base.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", abs.String(), nil)
	if err != nil {
		return nil, err
	}
	c.prepare(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", redactURL(abs), resp.Status)
	}
	return resp.Body, nil
}

// prepare applies auth and request headers common to every OPDS request.
// Accept-Encoding: identity avoids servers (or middleware) returning gzipped
// bodies the client then has to decode inline. Some Calibre-Web fronting
// proxies have been observed mangling compressed OPDS streams.
func (c *Client) prepare(req *http.Request) {
	if c.User != "" || c.Pass != "" {
		req.SetBasicAuth(c.User, c.Pass)
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "pocketbeam/"+version)
}

// catalogPath joins one of the client's own well-known OPDS paths ("/opds",
// "/opds/shelfindex", ...) onto the base URL's path prefix. url.URL.Parse
// treats an absolute-path reference as a replacement, so a base of
// https://host/calibre would otherwise resolve "/opds" to https://host/opds.
// Server-supplied hrefs do not go through here: they already carry
// whatever prefix the server is mounted under.
func (c *Client) catalogPath(p string) string {
	return strings.TrimRight(c.Base.Path, "/") + p
}

// Shelf is a user-curated collection of books, exposed by CWA under
// /opds/shelfindex. Generic OPDS servers do not share this concept.
type Shelf struct {
	ID   int
	Name string
}

// FilterOption is one row in the filter picker. Works for both CWA shelves
// (Href is /opds/shelf/<N>) and generic OPDS subsections (Href is whatever
// the server exposes from its root navigation feed). Count and CountKnown
// come from the opds:count attribute when the server advertises it; when
// the attribute is absent (generic OPDS servers, older CWA), CountKnown
// is false and the picker shows the row as-is.
type FilterOption struct {
	Name       string
	Href       string
	Count      int
	CountKnown bool
}

// OPDSLevel is one step of nested navigation: a list of subsections the
// user can drill into, the feed's title (for breadcrumbs), and whether
// the feed contains acquirable books at this level. Used by the picker.
type OPDSLevel struct {
	FeedTitle   string
	Subsections []FilterOption
	BookCount   int // total acquisition entries seen across all pages
}

// levelBookCount returns a best-effort book total for a level. When the
// feed itself has acquisition entries, the exact BookCount is used.
// Otherwise, if every visible subsection advertises an opds:count, the
// sum of those counts is returned flagged as approximate (it can
// double-count books shared across children, e.g. a book in two
// shelves). When neither is available the caller should fall back to
// the plain "Sync this level" label.
func levelBookCount(lvl OPDSLevel) (n int, approximate bool) {
	if lvl.BookCount > 0 {
		return lvl.BookCount, false
	}
	if len(lvl.Subsections) == 0 {
		return 0, false
	}
	sum := 0
	for _, sub := range lvl.Subsections {
		if !sub.CountKnown {
			return 0, false
		}
		sum += sub.Count
	}
	return sum, true
}

// FetchLevel retrieves the feed at href (walking pagination) and splits
// its entries into drill-in subsections and acquisition entries. An empty
// href starts at the catalog root ("/opds" under the base URL's prefix).
func (c *Client) FetchLevel(ctx context.Context, href string) (OPDSLevel, error) {
	if href == "" {
		href = c.catalogPath("/opds")
	}
	var lvl OPDSLevel
	first := true
	for href != "" {
		body, err := c.get(ctx, href)
		if err != nil {
			return OPDSLevel{}, err
		}
		var f feed
		if err := xml.Unmarshal(body, &f); err != nil {
			return OPDSLevel{}, fmt.Errorf("parse %s: %w", href, err)
		}
		if first {
			lvl.FeedTitle = strings.TrimSpace(f.Title)
			first = false
		}
		for _, e := range f.Entries {
			if _, ok := bookFromEntry(c.Base, e); ok {
				lvl.BookCount++
				continue
			}
			if nav, ok := navigationLink(e.Links); ok {
				lvl.Subsections = append(lvl.Subsections, filterOptionFromLink(nav, e.Title))
			}
		}
		href = nextLink(f.Links)
	}
	return lvl, nil
}

// DetectType fetches the root /opds feed once and sets c.IsCWA based on
// subsections it finds. CWA-signatures we accept: a subsection link pointing
// at /opds/books/letter/ or /opds/shelfindex, or a title containing
// Calibre-Web. Generic OPDS servers pass through with IsCWA=false and the
// recursive walker takes over.
func (c *Client) DetectType(ctx context.Context) error {
	body, err := c.get(ctx, c.catalogPath("/opds"))
	if err != nil {
		return err
	}
	var f feed
	if err := xml.Unmarshal(body, &f); err != nil {
		return fmt.Errorf("parse opds root: %w", err)
	}
	if strings.Contains(strings.ToLower(f.Title), "calibre-web") {
		c.IsCWA = true
		return nil
	}
	for _, e := range f.Entries {
		for _, l := range e.Links {
			if l.Rel == "subsection" &&
				(strings.Contains(l.Href, "/opds/books/letter/") ||
					strings.Contains(l.Href, "/opds/shelfindex")) {
				c.IsCWA = true
				return nil
			}
		}
	}
	return nil
}

// WalkAll returns every acquirable book exposed by the server. On CWA it
// takes the fast path (/opds/books/letter/00 is a single paginated feed).
// On any other OPDS server it falls through to a recursive walker that
// follows subsection links from the root.
func (c *Client) WalkAll(ctx context.Context) ([]Book, error) {
	if c.IsCWA {
		return c.walk(ctx, c.catalogPath("/opds/books/letter/00"))
	}
	return c.walkGeneric(ctx, c.catalogPath("/opds"))
}

// WalkFiltered returns books under a specific OPDS path (CWA shelf path,
// generic subsection, or any other OPDS feed URL). Empty filterHref means
// "sync all books" and falls through to WalkAll. The CWA shelf fast path
// (single paginated feed, no recursion) kicks in when filterHref looks like
// "/opds/shelf/<N>" and the client is in CWA mode.
func (c *Client) WalkFiltered(ctx context.Context, filterHref string) ([]Book, error) {
	if filterHref == "" {
		return c.WalkAll(ctx)
	}
	if c.IsCWA && strings.HasPrefix(filterHref, c.catalogPath("/opds/shelf/")) {
		return c.walk(ctx, filterHref)
	}
	return c.walkGeneric(ctx, filterHref)
}

// WalkShelf returns every acquirable book in the given CWA shelf. Only
// meaningful on CWA; an error is returned if the client is not in CWA mode.
func (c *Client) WalkShelf(ctx context.Context, id int) ([]Book, error) {
	if !c.IsCWA {
		return nil, fmt.Errorf("shelves are a Calibre-Web feature; server did not advertise them")
	}
	return c.walk(ctx, c.catalogPath(fmt.Sprintf("/opds/shelf/%d", id)))
}

// ListShelves returns the user's shelves as they appear in CWA's shelfindex
// OPDS feed. Returns nil on non-CWA servers (not an error; just no shelves).
func (c *Client) ListShelves(ctx context.Context) ([]Shelf, error) {
	if !c.IsCWA {
		return nil, nil
	}
	body, err := c.get(ctx, c.catalogPath("/opds/shelfindex"))
	if err != nil {
		return nil, err
	}
	var f feed
	if err := xml.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("parse shelfindex: %w", err)
	}
	shelfPrefix := c.catalogPath("/opds/shelf/")
	out := make([]Shelf, 0, len(f.Entries))
	for _, e := range f.Entries {
		if id, ok := shelfIDFromEntry(e, shelfPrefix); ok {
			out = append(out, Shelf{ID: id, Name: strings.TrimSpace(e.Title)})
		}
	}
	return out, nil
}

// walkGeneric recursively walks an OPDS catalog from start, following
// navigation links and collecting every acquisition entry as a Book.
// Visited URLs are deduplicated to prevent loops; walking is capped at
// maxWalkDepth levels deep to bound work on pathological catalogs. Books
// are deduplicated by UUID at the end since generic catalogs often expose
// the same book under multiple navigation sections (e.g. by-author and
// by-series). Any sub-feed that fails to load or parse fails the walk:
// the result is either the complete catalog or an error, never a subset.
const maxWalkDepth = 5

func (c *Client) walkGeneric(ctx context.Context, start string) ([]Book, error) {
	visited := make(map[string]bool)
	books, err := c.walkGenericRec(ctx, start, visited, 0)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(books))
	unique := books[:0]
	for _, b := range books {
		if b.UUID != "" && seen[b.UUID] {
			continue
		}
		if b.UUID != "" {
			seen[b.UUID] = true
		}
		unique = append(unique, b)
	}
	return unique, nil
}

func (c *Client) walkGenericRec(ctx context.Context, path string, visited map[string]bool, depth int) ([]Book, error) {
	if depth > maxWalkDepth {
		return nil, nil
	}
	abs, err := c.Base.Parse(path)
	if err != nil {
		return nil, err
	}
	key := abs.String()
	if visited[key] {
		return nil, nil
	}
	visited[key] = true

	var out []Book
	href := path
	for href != "" {
		body, err := c.get(ctx, href)
		if err != nil {
			return nil, err
		}
		var f feed
		if err := xml.Unmarshal(body, &f); err != nil {
			return nil, fmt.Errorf("parse %s: %w", href, err)
		}
		for _, e := range f.Entries {
			if b, ok := bookFromEntry(c.Base, e); ok {
				out = append(out, b)
				continue
			}
			if sub := navigationHref(e.Links); sub != "" {
				// A failed sub-feed aborts the whole listing. Silently
				// skipping it would return a partial catalog that
				// delete-missing then reads as "these books are gone".
				child, err := c.walkGenericRec(ctx, sub, visited, depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, child...)
			}
		}
		href = nextLink(f.Links)
	}
	return out, nil
}

// shelfIDFromEntry parses <N> from a shelfindex entry's <id>, which CWA
// emits as its shelf path (prefix + "<N>", prefix being "/opds/shelf/"
// under the server's mount point).
func shelfIDFromEntry(e entry, prefix string) (int, bool) {
	if !strings.HasPrefix(e.ID, prefix) {
		return 0, false
	}
	id, err := strconv.Atoi(strings.TrimPrefix(e.ID, prefix))
	if err != nil {
		return 0, false
	}
	return id, true
}

// navigationLink returns the first link on entry that points at another
// OPDS feed. Prefers explicit rel="subsection"; falls back to any
// atom+xml link since not all OPDS servers set rel on navigation entries
// (CWA's root feed is an example).
func navigationLink(links []link) (link, bool) {
	for _, l := range links {
		if l.Rel == "subsection" {
			return l, true
		}
	}
	for _, l := range links {
		if strings.Contains(l.Type, "atom+xml") {
			return l, true
		}
	}
	return link{}, false
}

// navigationHref is a convenience wrapper returning only the Href of the
// selected navigation link.
func navigationHref(links []link) string {
	l, ok := navigationLink(links)
	if !ok {
		return ""
	}
	return l.Href
}

// filterOptionFromLink builds a FilterOption from a navigation link plus
// the entry's display title, parsing opds:count when the server attached
// one so the picker can hide empty categories.
func filterOptionFromLink(l link, title string) FilterOption {
	opt := FilterOption{
		Name: strings.TrimSpace(title),
		Href: l.Href,
	}
	if opt.Name == "" {
		opt.Name = l.Href
	}
	if l.Count != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(l.Count)); err == nil {
			opt.Count = n
			opt.CountKnown = true
		}
	}
	return opt
}

func (c *Client) walk(ctx context.Context, start string) ([]Book, error) {
	var out []Book
	href := start
	for href != "" {
		body, err := c.get(ctx, href)
		if err != nil {
			return nil, err
		}
		var f feed
		if err := xml.Unmarshal(body, &f); err != nil {
			return nil, fmt.Errorf("parse %s: %w", href, err)
		}
		for _, e := range f.Entries {
			if b, ok := bookFromEntry(c.Base, e); ok {
				out = append(out, b)
			}
		}
		href = nextLink(f.Links)
	}
	return out, nil
}

func bookFromEntry(base *url.URL, e entry) (Book, bool) {
	acq := pickAcquisition(e.Links)
	if acq.Href == "" {
		return Book{}, false
	}
	abs, err := base.Parse(acq.Href)
	if err != nil {
		return Book{}, false
	}
	updated, _ := time.Parse(time.RFC3339, e.Updated)
	var size int64
	if acq.Length != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(acq.Length), 10, 64); err == nil && n > 0 {
			size = n
		}
	}
	return Book{
		UUID:    strings.TrimPrefix(e.ID, "urn:uuid:"),
		Title:   e.Title,
		Author:  e.Author.Name,
		Updated: updated,
		URL:     abs.String(),
		Format:  acq.Type,
		Size:    size,
	}, true
}

// pickAcquisition picks the highest-priority format we know how to handle.
func pickAcquisition(links []link) link {
	for _, want := range preferredFormats {
		for _, l := range links {
			if l.Rel == AcquisitionRel && l.Type == want {
				return l
			}
		}
	}
	return link{}
}

func nextLink(links []link) string {
	for _, l := range links {
		if l.Rel == "next" {
			return l.Href
		}
	}
	return ""
}
