package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// UserAgentFn returns the HTTP User-Agent used by updater requests.
// The default prints only `pocketbeam/<version>`; the InkView build
// overrides it at startup with the device-specific form built in
// ui_arm.go. Disable update checks via check_updates = off.
var UserAgentFn = func() string {
	return "pocketbeam/" + version
}

// Release captures the subset of a Gitea / GitHub release payload that
// the updater needs: the tag name (treated as a semver), a direct URL to
// the ARM .app asset, the asset byte size (for "X.Y MB" in the UI
// pre-download), and a published SHA-256 checksum for integrity
// verification.
//
// Releases since v0.4.2 also publish a gzip-compressed copy of the
// binary as `pocketbeam.app.gz`. When present, Download prefers it and
// decompresses on the fly, halving the bytes that flow over the
// device's Wi-Fi. SHA256 always hashes the *decompressed* binary, so
// integrity is preserved end-to-end either way.
type Release struct {
	Version       string // tag_name, e.g. "v0.1.0"; compared semver-wise
	BinaryURL     string // raw .app asset (legacy + fallback for first-install tooling)
	CompressedURL string // .app.gz asset; preferred by Download when non-empty
	BinarySize    int64  // size of whatever Download will actually fetch (compressed when available, raw otherwise)
	SHA256        string // lowercase hex of the decompressed binary; empty means the release body omitted it
}

// giteaRelease mirrors the fields we pull from the release API. The
// Gitea and GitHub shapes are compatible, so one struct works for both.
type giteaRelease struct {
	TagName string `json:"tag_name"`
	Body    string `json:"body"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// assetName is the raw release asset. Keeping the name stable across
// releases simplifies the build script. assetNameCompressed is the
// gzip-compressed copy published since v0.4.2; the updater prefers it
// when present and falls back to the raw asset otherwise so downgrades
// and freshly-flashed devices still work.
const (
	assetName           = "pocketbeam.app"
	assetNameCompressed = "pocketbeam.app.gz"
)

// sha256Pattern extracts a sha256 hex digest from a release body. The
// convention is `sha256: <64-hex-chars>` (case-insensitive) anywhere in
// the body; this avoids forcing a separate SHA256SUMS asset for the
// MVP.
var sha256Pattern = regexp.MustCompile(`(?i)sha256[:\s]+([0-9a-f]{64})`)

// CheckLatest asks the release endpoint for the most recent release and
// returns (newer, release, nil) when its version is strictly greater
// than currentVersion. Any 4xx/5xx is returned as an error so the
// caller can distinguish "no update" from "couldn't check at all".
func CheckLatest(ctx context.Context, endpoint, currentVersion string) (newer bool, rel Release, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return false, Release{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgentFn())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, Release{}, fmt.Errorf("release endpoint %s: %s", endpoint, resp.Status)
	}
	var g giteaRelease
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return false, Release{}, fmt.Errorf("parse release: %w", err)
	}
	rel.Version = g.TagName
	var rawSize int64
	for _, a := range g.Assets {
		switch a.Name {
		case assetName:
			rel.BinaryURL = a.URL
			rawSize = a.Size
		case assetNameCompressed:
			rel.CompressedURL = a.URL
			rel.BinarySize = a.Size
		}
	}
	// BinarySize reflects what Download will actually fetch so the UI's
	// progress bar shows accurate totals. Fall back to the raw asset
	// size only when no compressed asset was published.
	if rel.CompressedURL == "" {
		rel.BinarySize = rawSize
	}
	if m := sha256Pattern.FindStringSubmatch(g.Body); len(m) == 2 {
		rel.SHA256 = strings.ToLower(m[1])
	}
	return semverGreater(rel.Version, currentVersion), rel, nil
}

// Download streams the release binary into destPath, verifying the
// SHA-256 of the installable binary as it goes. Prefers rel.CompressedURL
// (gzip-on-the-wire) and transparently decompresses into destPath,
// falling back to rel.BinaryURL when no compressed asset was published.
// progress (if non-nil) reports cumulative bytes downloaded over the
// wire and the Content-Length total (-1 when unknown), so the UI's
// percentage matches what's actually being fetched rather than the
// final on-disk size. On failure the incomplete file is removed.
func Download(ctx context.Context, rel Release, destPath string, progress func(written, total int64)) error {
	url := rel.CompressedURL
	compressed := url != ""
	if !compressed {
		url = rel.BinaryURL
	}
	if url == "" {
		return errors.New("release has no binary URL")
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgentFn())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	// Wire counter wraps the raw HTTP body so progress reflects bytes
	// over Wi-Fi; when compressed we then layer a gzip reader on top so
	// the hash and file writes see the decompressed binary.
	var src io.Reader = &countingReader{r: resp.Body, total: resp.ContentLength, cb: progress}
	if compressed {
		gzr, err := gzip.NewReader(src)
		if err != nil {
			return fmt.Errorf("gzip reader: %w", err)
		}
		defer gzr.Close()
		src = gzr
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	w := io.MultiWriter(f, hasher)
	if _, err := io.Copy(w, src); err != nil {
		f.Close()
		os.Remove(destPath)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(destPath)
		return err
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if rel.SHA256 != "" && got != rel.SHA256 {
		os.Remove(destPath)
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, rel.SHA256)
	}
	return nil
}

// Install atomically replaces targetPath with the already-downloaded
// file at stagedPath. On Linux the running binary's inode stays alive
// for the current process, so this is safe to call from the app
// updating itself; the user must relaunch for the new version to run.
//
// Chmod is best-effort: PocketBook's /mnt/ext1 is a vfat filesystem on
// many models, and vfat takes per-file mode bits from the mount options
// rather than from chmod syscalls. A chmod EPERM on vfat is meaningless;
// the executable bit is already set by the mount's fmask, so we proceed
// to the rename regardless of chmod's return.
func Install(stagedPath, targetPath string) error {
	_ = os.Chmod(stagedPath, 0o755)
	if err := os.Rename(stagedPath, targetPath); err != nil {
		// Don't leak the staged file; the next sync's stale sweep would
		// eventually remove it, but cleaning up here keeps the filesystem
		// tidy after a failed install.
		_ = os.Remove(stagedPath)
		return err
	}
	return nil
}

// countingReader wraps an io.Reader and invokes cb with the running
// total + the announced Content-Length (or -1 when unknown) after each
// read. Gives the UI enough information to show a percentage bar when
// the server advertised a size and a raw byte counter otherwise.
type countingReader struct {
	r     io.Reader
	n     int64
	total int64
	cb    func(written, total int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.cb != nil && n > 0 {
		c.cb(c.n, c.total)
	}
	return n, err
}

// semverGreater returns true when a > b, treating leading 'v' and any
// suffix after the numeric triplet as noise. "v0.2.0" > "v0.1.9",
// "v0.1.0-dev" > "v0.0.9", "v0.1.0" == "v0.1.0". An unparseable
// version is treated as the smallest possible so unknown-current
// always reports as "update available".
func semverGreater(a, b string) bool {
	am := parseSemver(a)
	bm := parseSemver(b)
	for i := 0; i < 3; i++ {
		if am[i] != bm[i] {
			return am[i] > bm[i]
		}
	}
	return false
}

func parseSemver(s string) [3]int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	// Drop anything after the first character that isn't a digit or dot
	// so "0.1.0-dev" parses as 0.1.0 for comparison purposes.
	end := len(s)
	for i, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			end = i
			break
		}
	}
	parts := strings.Split(s[:end], ".")
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return [3]int{}
		}
		out[i] = n
	}
	return out
}
