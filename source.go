package main

import (
	"context"
	"fmt"
	"io"
)

// Source abstracts the remote catalog the sync loop pulls from. OPDS
// (Calibre-Web, generic) and WebDAV (Nextcloud, ownCloud, Synology) are
// both implementations. The sync loop is backend-agnostic: it asks for a
// list of items, diffs against the local store, and fetches what changed.
type Source interface {
	// List returns every remote item that is a candidate for sync. Scope
	// (shelf, directory, filter) is captured at construction time so the
	// sync loop does not have to know about it.
	List(ctx context.Context) ([]Book, error)

	// Fetch streams the bytes of one item. The caller closes the reader.
	// The context bounds the whole transfer (headers + body); cancel it to
	// abort a stalled download.
	Fetch(ctx context.Context, b Book) (io.ReadCloser, error)
}

// OPDSSource adapts a Client into a Source, binding the filter(s) at
// construction so the sync loop does not have to pass them through. An
// empty FilterHrefs slice means "sync everything". Multiple filter
// hrefs are walked in order and deduped by UUID.
type OPDSSource struct {
	Client      *Client
	FilterHrefs []string
}

// List detects the server type right before walking so the walk picks
// the CWA fast path when it applies. Detection lives here rather than in
// newSource so constructing a source never touches the network; a
// failed detection is not fatal, the generic walker still works.
func (s *OPDSSource) List(ctx context.Context) ([]Book, error) {
	_ = s.Client.DetectType()
	if len(s.FilterHrefs) == 0 {
		return s.Client.WalkAll()
	}
	seen := make(map[string]struct{})
	var out []Book
	for _, href := range s.FilterHrefs {
		books, err := s.Client.WalkFiltered(href)
		if err != nil {
			return nil, err
		}
		for _, b := range books {
			if _, ok := seen[b.UUID]; ok {
				continue
			}
			seen[b.UUID] = struct{}{}
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *OPDSSource) Fetch(ctx context.Context, b Book) (io.ReadCloser, error) {
	return s.Client.Fetch(ctx, b.URL)
}

// newSource constructs the source implied by the config, dispatching on
// cfg.Backend. It performs no I/O; the first List call does.
func newSource(cfg *Config) (Source, error) {
	switch cfg.Backend {
	case "", BackendOPDS:
		client, err := NewClient(cfg.Host, cfg.User, cfg.Pass)
		if err != nil {
			return nil, err
		}
		return &OPDSSource{Client: client, FilterHrefs: cfg.FilterHrefs}, nil
	case BackendWebDAV:
		return NewWebDAVSource(cfg.Host, cfg.User, cfg.Pass, cfg.Path), nil
	}
	return nil, fmt.Errorf("unknown backend %q", cfg.Backend)
}
