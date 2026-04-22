package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSemverGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.9", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v1.0.0", "v0.99.99", true},
		{"0.1.0", "v0.1.0", false},
		{"v0.1.0-dev", "v0.0.9", true},
		{"v0.1.0", "v0.1.0-rc1", false}, // equal core; prerelease ignored
		{"", "v0.0.0", false},
		{"garbage", "v0.0.1", false},
	}
	for _, tc := range cases {
		if got := semverGreater(tc.a, tc.b); got != tc.want {
			t.Errorf("semverGreater(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// newReleaseMock stands up a Gitea-shaped release endpoint plus an
// asset download path at /asset. The response body is configurable so
// tests can exercise missing-asset and missing-sha cases.
func newReleaseMock(t *testing.T, tagName, assetBody, sha string) *httptest.Server {
	t.Helper()
	assetBodyBytes := []byte(assetBody)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/x/pocketbeam/releases/latest":
			body := fmt.Sprintf("some notes\nsha256: %s\nmore notes", sha)
			fmt.Fprintf(w, `{"tag_name":%q,"body":%q,"assets":[{"name":%q,"browser_download_url":%q,"size":%d}]}`,
				tagName, body, assetName, srv.URL+"/asset", len(assetBodyBytes))
		case "/asset":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(assetBodyBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

func TestCheckLatest_NewerVersionDetected(t *testing.T) {
	const body = "pretend new binary"
	sum := sha256.Sum256([]byte(body))
	srv := newReleaseMock(t, "v0.2.0", body, hex.EncodeToString(sum[:]))
	defer srv.Close()

	newer, rel, err := CheckLatest(context.Background(), srv.URL+"/api/v1/repos/x/pocketbeam/releases/latest", "v0.1.0")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	if !newer {
		t.Fatalf("expected newer=true for v0.2.0 vs v0.1.0; got rel=%+v", rel)
	}
	if rel.Version != "v0.2.0" {
		t.Errorf("Version = %q, want v0.2.0", rel.Version)
	}
	if rel.BinaryURL == "" || rel.SHA256 == "" {
		t.Errorf("missing fields: %+v", rel)
	}
	if rel.BinarySize != int64(len("pretend new binary")) {
		t.Errorf("BinarySize = %d, want %d (from manifest assets[].size)",
			rel.BinarySize, len("pretend new binary"))
	}
}

func TestCheckLatest_SameVersionNotNewer(t *testing.T) {
	const body = "whatever"
	sum := sha256.Sum256([]byte(body))
	srv := newReleaseMock(t, "v0.1.0", body, hex.EncodeToString(sum[:]))
	defer srv.Close()

	newer, _, err := CheckLatest(context.Background(), srv.URL+"/api/v1/repos/x/pocketbeam/releases/latest", "v0.1.0")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	if newer {
		t.Errorf("expected newer=false for equal versions")
	}
}

func TestDownload_VerifiesSHA(t *testing.T) {
	const body = "pocketbeam arm binary here"
	sum := sha256.Sum256([]byte(body))
	srv := newReleaseMock(t, "v0.2.0", body, hex.EncodeToString(sum[:]))
	defer srv.Close()

	_, rel, err := CheckLatest(context.Background(), srv.URL+"/api/v1/repos/x/pocketbeam/releases/latest", "v0.1.0")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}

	dir := t.TempDir()
	staged := filepath.Join(dir, "pocketbeam.app.new")
	var lastWritten, lastTotal int64
	if err := Download(context.Background(), rel, staged, func(w, tot int64) {
		lastWritten = w
		lastTotal = tot
	}); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if lastWritten != int64(len(body)) {
		t.Errorf("progress written = %d, want %d", lastWritten, len(body))
	}
	if lastTotal != int64(len(body)) {
		t.Errorf("progress total = %d, want %d (httptest advertises Content-Length)", lastTotal, len(body))
	}
	got, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("read staged: %v", err)
	}
	if string(got) != body {
		t.Errorf("staged content mismatch")
	}
}

func TestDownload_RejectsBadSHA(t *testing.T) {
	const body = "mismatched"
	const wrongHash = "0000000000000000000000000000000000000000000000000000000000000000"
	srv := newReleaseMock(t, "v0.2.0", body, wrongHash)
	defer srv.Close()

	_, rel, err := CheckLatest(context.Background(), srv.URL+"/api/v1/repos/x/pocketbeam/releases/latest", "v0.1.0")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}

	staged := filepath.Join(t.TempDir(), "pocketbeam.app.new")
	err = Download(context.Background(), rel, staged, nil)
	if err == nil {
		t.Errorf("expected sha256 mismatch error, got nil")
	}
	if _, statErr := os.Stat(staged); !os.IsNotExist(statErr) {
		t.Errorf("failed download left a file behind: %v", statErr)
	}
}

func TestInstall_SwapsBinary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pocketbeam.app")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "pocketbeam.app.new")
	if err := os.WriteFile(staged, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(staged, target); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("target content = %q, want %q", got, "new")
	}
	// Staged path should no longer exist after the rename.
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("staged still present: %v", err)
	}
	// Mode should be executable on the target.
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("target mode = %o, want +x", fi.Mode().Perm())
	}
}

// newGzippedReleaseMock is like newReleaseMock but publishes a second
// `pocketbeam.app.gz` asset alongside the raw binary and serves a
// gzipped copy at /asset.gz. The release body records the SHA of the
// decompressed binary, matching what the release.sh script emits.
func newGzippedReleaseMock(t *testing.T, tagName, rawBody, sha string) *httptest.Server {
	t.Helper()
	var gzBuf bytes.Buffer
	gzw := gzip.NewWriter(&gzBuf)
	_, _ = gzw.Write([]byte(rawBody))
	_ = gzw.Close()
	gzBytes := gzBuf.Bytes()
	rawBytes := []byte(rawBody)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/x/pocketbeam/releases/latest":
			body := fmt.Sprintf("some notes\nsha256: %s\nmore notes", sha)
			fmt.Fprintf(w, `{"tag_name":%q,"body":%q,"assets":[{"name":%q,"browser_download_url":%q,"size":%d},{"name":%q,"browser_download_url":%q,"size":%d}]}`,
				tagName, body,
				assetName, srv.URL+"/asset", len(rawBytes),
				assetNameCompressed, srv.URL+"/asset.gz", len(gzBytes))
		case "/asset":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(rawBytes)
		case "/asset.gz":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(gzBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

func TestCheckLatest_PrefersCompressedAsset(t *testing.T) {
	// Use a highly compressible body so the gzipped size is strictly
	// smaller than the raw size; short bodies can otherwise grow past
	// raw due to gzip's fixed header/trailer overhead.
	body := strings.Repeat("pretend-arm-binary-contents-", 64)
	sum := sha256.Sum256([]byte(body))
	srv := newGzippedReleaseMock(t, "v0.4.2", body, hex.EncodeToString(sum[:]))
	defer srv.Close()

	_, rel, err := CheckLatest(context.Background(), srv.URL+"/api/v1/repos/x/pocketbeam/releases/latest", "v0.4.1")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	if rel.CompressedURL == "" {
		t.Fatalf("expected CompressedURL to be picked up from assets; rel=%+v", rel)
	}
	if rel.BinaryURL == "" {
		t.Fatalf("expected BinaryURL to remain populated as fallback; rel=%+v", rel)
	}
	// BinarySize must reflect what Download actually fetches (compressed)
	// so the progress bar percentage matches the bytes crossing the wire.
	if rel.BinarySize >= int64(len(body)) {
		t.Errorf("BinarySize = %d, expected compressed size (< raw %d)", rel.BinarySize, len(body))
	}
}

func TestDownload_DecompressesGzipAndVerifiesSHA(t *testing.T) {
	const body = "pocketbeam arm binary here, longer body to make gzip meaningful"
	sum := sha256.Sum256([]byte(body))
	srv := newGzippedReleaseMock(t, "v0.4.2", body, hex.EncodeToString(sum[:]))
	defer srv.Close()

	_, rel, err := CheckLatest(context.Background(), srv.URL+"/api/v1/repos/x/pocketbeam/releases/latest", "v0.4.1")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}

	dir := t.TempDir()
	staged := filepath.Join(dir, "pocketbeam.app.new")
	if err := Download(context.Background(), rel, staged, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("read staged: %v", err)
	}
	if string(got) != body {
		t.Errorf("staged content mismatch: got %q", got)
	}
}

func TestDownload_RejectsBadSHAOnCompressedStream(t *testing.T) {
	const body = "valid gzip but SHA on release body is wrong"
	const wrongHash = "0000000000000000000000000000000000000000000000000000000000000000"
	srv := newGzippedReleaseMock(t, "v0.4.2", body, wrongHash)
	defer srv.Close()

	_, rel, err := CheckLatest(context.Background(), srv.URL+"/api/v1/repos/x/pocketbeam/releases/latest", "v0.4.1")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	staged := filepath.Join(t.TempDir(), "pocketbeam.app.new")
	if err := Download(context.Background(), rel, staged, nil); err == nil {
		t.Errorf("expected sha256 mismatch error, got nil")
	}
	if _, statErr := os.Stat(staged); !os.IsNotExist(statErr) {
		t.Errorf("failed gzip download left a file behind: %v", statErr)
	}
}

// Suppress unused-import warning when io is only needed through helpers.
var _ = io.EOF
