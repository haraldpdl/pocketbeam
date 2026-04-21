package main

import (
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
// overrides it at startup to include PocketBook device model, hardware
// type, firmware version, and screen size so the release backend can
// distinguish devices in its access logs. The tradeoff is disclosed in
// the README: the alternative is silent fingerprinting from IP and
// timing, which is worse. Disable via check_updates = off.
var UserAgentFn = func() string {
	return "pocketbeam/" + version
}

// Release captures the subset of a Gitea / GitHub release payload that
// the updater needs: the tag name (treated as a semver), a direct URL to
// the ARM .app asset, the asset byte size (for "X.Y MB" in the UI
// pre-download), and a published SHA-256 checksum for integrity
// verification.
type Release struct {
	Version   string // tag_name, e.g. "v0.1.0"; compared semver-wise
	BinaryURL string
	BinarySize int64 // asset size in bytes from the release manifest; 0 when absent
	SHA256    string // lowercase hex; empty means the release body omitted it
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

// assetName is the release asset the updater looks for. Keeping the
// name stable across releases simplifies the GitHub Actions build.
const assetName = "pocketbeam.app"

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
	for _, a := range g.Assets {
		if a.Name == assetName {
			rel.BinaryURL = a.URL
			rel.BinarySize = a.Size
			break
		}
	}
	if m := sha256Pattern.FindStringSubmatch(g.Body); len(m) == 2 {
		rel.SHA256 = strings.ToLower(m[1])
	}
	return semverGreater(rel.Version, currentVersion), rel, nil
}

// Download streams rel.BinaryURL into destPath, verifying the SHA-256
// as it goes. progress (if non-nil) is invoked periodically with the
// cumulative bytes downloaded and the total from Content-Length (-1
// when the server didn't announce one, so the UI can fall back to a
// byte-counter). On failure the incomplete file is removed and the
// hash mismatch is reported to the caller.
func Download(ctx context.Context, rel Release, destPath string, progress func(written, total int64)) error {
	if rel.BinaryURL == "" {
		return errors.New("release has no binary URL")
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rel.BinaryURL, nil)
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
		return fmt.Errorf("GET %s: %s", rel.BinaryURL, resp.Status)
	}
	total := resp.ContentLength
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	w := io.MultiWriter(f, hasher)
	reader := &countingReader{r: resp.Body, total: total, cb: progress}
	if _, err := io.Copy(w, reader); err != nil {
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
