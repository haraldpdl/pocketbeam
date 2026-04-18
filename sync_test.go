package main

import "testing"

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Plain Title", "Plain Title"},
		{"Heller| Peter", "Heller_ Peter"},                   // pipe → underscore
		{"a/b\\c", "a_b_c"},                                  // path separators
		{"weird:title?with*chars", "weird_title_with_chars"}, // FAT-illegal
		{"  leading and trailing  ", "leading and trailing"}, // outer whitespace
		{"runs   of    spaces", "runs of spaces"},            // collapse internal whitespace
		{"trailing dots...", "trailing dots"},                // strip trailing dots
		{"", "_"},                                            // empty input
		{"...", "_"},                                         // becomes empty after strip
		{"control\x01char", "controlchar"},                   // strip control chars
		{"Die Ehefrau – Was hat sie zu verbergen?", "Die Ehefrau – Was hat sie zu verbergen_"}, // unicode preserved
	}
	for _, tc := range cases {
		if got := sanitize(tc.in); got != tc.want {
			t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPickAcquisition_PrefersEpub(t *testing.T) {
	links := []link{
		{Rel: "self", Type: "application/atom+xml"},
		{Rel: AcquisitionRel, Type: "application/x-cbz", Href: "/cbz"},
		{Rel: AcquisitionRel, Type: "application/epub+zip", Href: "/epub"},
		{Rel: AcquisitionRel, Type: "application/pdf", Href: "/pdf"},
	}
	if got := pickAcquisition(links).Href; got != "/epub" {
		t.Errorf("expected EPUB to win, got %q", got)
	}
}

func TestPickAcquisition_FallsThroughToCBZ(t *testing.T) {
	links := []link{
		{Rel: AcquisitionRel, Type: "application/x-cbz", Href: "/cbz"},
		{Rel: AcquisitionRel, Type: "application/pdf", Href: "/pdf"},
	}
	if got := pickAcquisition(links).Href; got != "/cbz" {
		t.Errorf("expected CBZ when no EPUB, got %q", got)
	}
}

func TestPickAcquisition_NoneSupported(t *testing.T) {
	links := []link{
		{Rel: AcquisitionRel, Type: "application/x-mobipocket-ebook", Href: "/mobi"},
	}
	if got := pickAcquisition(links).Href; got != "" {
		t.Errorf("expected empty when no preferred format, got %q", got)
	}
}

func TestNextLink(t *testing.T) {
	links := []link{
		{Rel: "self", Href: "/page1"},
		{Rel: "next", Href: "/page2"},
	}
	if got := nextLink(links); got != "/page2" {
		t.Errorf("nextLink = %q, want /page2", got)
	}
	if got := nextLink(nil); got != "" {
		t.Errorf("nextLink(nil) = %q, want empty", got)
	}
}
