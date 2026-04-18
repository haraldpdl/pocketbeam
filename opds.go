package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	return &Client{
		Base: u,
		User: user,
		Pass: pass,
		HTTP: &http.Client{Timeout: 30 * time.Second},
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

// WalkAll fetches the alphabetical "All books" catalog and returns every
// acquirable book as a flat slice. CWA exposes /opds/books/letter/00 as the
// full list; pagination is followed via rel="next" links.
func (c *Client) WalkAll() ([]Book, error) {
	return c.walk("/opds/books/letter/00")
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
