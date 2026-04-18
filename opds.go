package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net"
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
}

// Book is one acquirable item flattened out of the OPDS catalog.
type Book struct {
	UUID    string // from <id>, urn:uuid: prefix stripped
	Title   string
	Author  string
	Updated time.Time // from <updated>
	URL     string    // absolute acquisition URL
	Format  string    // mime type
}

// Client fetches and walks an OPDS catalog.
type Client struct {
	Base *url.URL
	User string
	Pass string
	HTTP *http.Client
}

func NewClient(base, user, pass string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	// Split the timeout into per-stage budgets so big book bodies can take as
	// long as they need while hung connections still fail fast. A single
	// Client.Timeout is too blunt: a 500 MB CBR over slow Wi-Fi needs more
	// than 30 s just to stream the body.
	//
	// ResponseHeaderTimeout is generous (5 min) because CWA runs calibredb
	// export with metadata embedding before it sends the first byte, which
	// can take a minute or more for large files. Once the headers arrive,
	// the body read has no deadline so transfer size is unlimited.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
	}
	return &Client{
		Base: u,
		User: user,
		Pass: pass,
		HTTP: &http.Client{Transport: transport},
	}, nil
}

func (c *Client) get(href string) ([]byte, error) {
	abs, err := c.Base.Parse(href)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("GET", abs.String(), nil)
	if err != nil {
		return nil, err
	}
	if c.User != "" || c.Pass != "" {
		req.SetBasicAuth(c.User, c.Pass)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", abs, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// Fetch downloads a single URL, used for EPUB acquisition. The caller streams
// the body to disk; we return the body reader and a close func.
func (c *Client) Fetch(rawurl string) (io.ReadCloser, error) {
	abs, err := c.Base.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("GET", abs.String(), nil)
	if err != nil {
		return nil, err
	}
	if c.User != "" || c.Pass != "" {
		req.SetBasicAuth(c.User, c.Pass)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", abs, resp.Status)
	}
	return resp.Body, nil
}

// Shelf is a user-curated collection of books in CWA.
type Shelf struct {
	ID   int
	Name string
}

// WalkAll fetches the alphabetical "All books" catalog and returns every
// acquirable book as a flat slice. CWA exposes /opds/books/letter/00 as the
// full list; pagination is followed via rel="next" links.
func (c *Client) WalkAll() ([]Book, error) {
	return c.walk("/opds/books/letter/00")
}

// WalkShelf returns every acquirable book in the given CWA shelf.
func (c *Client) WalkShelf(id int) ([]Book, error) {
	return c.walk(fmt.Sprintf("/opds/shelf/%d", id))
}

// ListShelves returns the user's shelves as they appear in CWA's shelfindex
// OPDS feed. Sorted by the feed's natural order (typically recency).
func (c *Client) ListShelves() ([]Shelf, error) {
	body, err := c.get("/opds/shelfindex")
	if err != nil {
		return nil, err
	}
	var f feed
	if err := xml.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("parse shelfindex: %w", err)
	}
	out := make([]Shelf, 0, len(f.Entries))
	for _, e := range f.Entries {
		if id, ok := shelfIDFromEntry(e); ok {
			out = append(out, Shelf{ID: id, Name: strings.TrimSpace(e.Title)})
		}
	}
	return out, nil
}

// shelfIDFromEntry parses "/opds/shelf/<N>" from a shelfindex entry's <id>.
func shelfIDFromEntry(e entry) (int, bool) {
	const prefix = "/opds/shelf/"
	if !strings.HasPrefix(e.ID, prefix) {
		return 0, false
	}
	id, err := strconv.Atoi(strings.TrimPrefix(e.ID, prefix))
	if err != nil {
		return 0, false
	}
	return id, true
}

func (c *Client) walk(start string) ([]Book, error) {
	var out []Book
	href := start
	for href != "" {
		body, err := c.get(href)
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
	return Book{
		UUID:    strings.TrimPrefix(e.ID, "urn:uuid:"),
		Title:   e.Title,
		Author:  e.Author.Name,
		Updated: updated,
		URL:     abs.String(),
		Format:  acq.Type,
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
