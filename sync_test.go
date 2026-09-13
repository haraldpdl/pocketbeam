package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

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

func TestSanitize_LengthCap(t *testing.T) {
	long := strings.Repeat("A", maxFilenameBytes+50)
	got := sanitize(long)
	if len(got) > maxFilenameBytes {
		t.Errorf("sanitize did not cap length: got %d bytes, want <= %d", len(got), maxFilenameBytes)
	}
	// Ensure the cap doesn't split a multi-byte rune. 4-byte runes ("𝄞"
	// musical symbol) packed to just over the cap must end on a rune
	// boundary.
	multiByte := strings.Repeat("𝄞", (maxFilenameBytes/4)+5)
	got = sanitize(multiByte)
	if len(got) > maxFilenameBytes {
		t.Errorf("multi-byte sanitize exceeded cap: %d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("sanitize produced invalid UTF-8 at cap boundary")
	}
}

func TestSweepStalePartFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "Author", "Book.epub.part")
	fresh := filepath.Join(dir, "Author", "Fresh.epub.part")
	keep := filepath.Join(dir, "Author", "Book.epub")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{stale, fresh, keep} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	sweepStalePartFiles(dir, time.Hour)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale .part not removed: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh .part removed (should remain): %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("non-.part file removed: %v", err)
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

func TestShortID(t *testing.T) {
	a, b := shortID("webdav:x/Book.epub"), shortID("webdav:y/Book.epub")
	if len(a) != 8 || a == b {
		t.Errorf("shortID not 8 distinct hex chars: %q vs %q", a, b)
	}
	if a != shortID("webdav:x/Book.epub") {
		t.Errorf("shortID not deterministic")
	}
}
