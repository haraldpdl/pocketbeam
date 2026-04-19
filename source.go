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

// OPDSSource adapts a Client into a Source, binding the filter at
// construction so the sync loop does not have to pass it through.
type OPDSSource struct {
	Client     *Client
	FilterHref string
}

func (s *OPDSSource) List(ctx context.Context) ([]Book, error) {
	return s.Client.WalkFiltered(s.FilterHref)
}

func (s *OPDSSource) Fetch(ctx context.Context, b Book) (io.ReadCloser, error) {
	return s.Client.Fetch(ctx, b.URL)
}

// newSource constructs the source implied by the config, dispatching on
// cfg.Backend.
func newSource(cfg *Config) (Source, error) {
	switch cfg.Backend {
	case "", BackendOPDS:
		client, err := NewClient(cfg.Host, cfg.User, cfg.Pass)
		if err != nil {
			return nil, err
		}
		_ = client.DetectType()
		return &OPDSSource{Client: client, FilterHref: cfg.FilterHref}, nil
	case BackendWebDAV:
		return NewWebDAVSource(cfg.Host, cfg.User, cfg.Pass, cfg.Path), nil
	}
	return nil, fmt.Errorf("unknown backend %q", cfg.Backend)
}
