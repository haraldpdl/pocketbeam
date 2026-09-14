//go:build !arm

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestPackageRelease runs the release packaging script over a stand-in
// binary and checks the two contracts a release has to keep: SHA256SUMS
// verifies clean against the files release.yml actually publishes, and the
// notes carry the unpacked binary's digest in the form the on-device
// updater parses.
func TestPackageRelease(t *testing.T) {
	for _, tool := range []string{"bash", "gzip", "zip", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}

	script, err := filepath.Abs(filepath.Join(".github", "scripts", "package-release.sh"))
	if err != nil {
		t.Fatalf("resolving script path: %v", err)
	}
	dist := t.TempDir()
	binary := []byte("not really an ARM binary")
	if err := os.WriteFile(filepath.Join(dist, "pocketbeam.app"), binary, 0o755); err != nil {
		t.Fatalf("writing pocketbeam.app: %v", err)
	}

	cmd := exec.Command("bash", script, dist, "v1.2.3")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("package-release.sh: %v\n%s", err, out)
	}

	// What a downloader can get, read off the workflow rather than
	// restated here: issue #59 was SHA256SUMS and the upload list drifting
	// apart, which a hardcoded expectation cannot catch.
	uploaded := uploadedAssets(t)
	if !slices.Contains(uploaded, "SHA256SUMS") {
		t.Fatalf("release.yml uploads %v, without SHA256SUMS: nothing to verify a download against", uploaded)
	}
	for _, asset := range uploaded {
		if _, err := os.Stat(filepath.Join(dist, asset)); err != nil {
			t.Errorf("release.yml uploads %s, which the packaging script does not produce: %v", asset, err)
		}
	}

	// SHA256SUMS cannot hash itself, so it has to name the rest exactly.
	want := slices.DeleteFunc(slices.Clone(uploaded), func(a string) bool { return a == "SHA256SUMS" })
	got := listedFiles(t, filepath.Join(dist, "SHA256SUMS"))
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("SHA256SUMS lists %v, want %v (the assets release.yml uploads)", got, want)
	}

	notes, err := os.ReadFile(filepath.Join(dist, "notes.md"))
	if err != nil {
		t.Fatalf("reading notes.md: %v", err)
	}
	if !strings.Contains(string(notes), "pocketbeam v1.2.3") {
		t.Errorf("notes.md does not name the version:\n%s", notes)
	}
	sum := sha256.Sum256(binary)
	// The same pattern CheckLatest applies to a release body, so a notes
	// reformat that hides the digest from the device fails here.
	m := sha256Pattern.FindStringSubmatch(string(notes))
	if len(m) != 2 {
		t.Fatalf("notes.md carries no parsable sha256 digest:\n%s", notes)
	}
	if m[1] != hex.EncodeToString(sum[:]) {
		t.Errorf("notes.md digest = %s, want %s (the unpacked binary's)", m[1], hex.EncodeToString(sum[:]))
	}

	// A downloader has the published assets and nothing else, so everything
	// the release does not ship goes away before the check: with the raw
	// binary listed, sha256sum reported it missing and exited non-zero.
	entries, err := os.ReadDir(dist)
	if err != nil {
		t.Fatalf("reading dist: %v", err)
	}
	for _, e := range entries {
		if slices.Contains(uploaded, e.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(dist, e.Name())); err != nil {
			t.Fatalf("pruning %s: %v", e.Name(), err)
		}
	}
	if out, err := sha256sumCheck(dist, "-c", "SHA256SUMS"); err != nil {
		t.Errorf("sha256sum -c SHA256SUMS over the full release: %v\n%s", err, out)
	}

	// The README tells readers to take one asset and verify it with
	// --ignore-missing, so every single-asset download has to pass that way.
	for _, asset := range want {
		partial := t.TempDir()
		for _, f := range []string{asset, "SHA256SUMS"} {
			data, err := os.ReadFile(filepath.Join(dist, f))
			if err != nil {
				t.Fatalf("reading %s: %v", f, err)
			}
			if err := os.WriteFile(filepath.Join(partial, f), data, 0o644); err != nil {
				t.Fatalf("writing %s: %v", f, err)
			}
		}
		if out, err := sha256sumCheck(partial, "--ignore-missing", "-c", "SHA256SUMS"); err != nil {
			t.Errorf("sha256sum --ignore-missing -c SHA256SUMS with only %s: %v\n%s", asset, err, out)
		}
	}
}

func sha256sumCheck(dir string, args ...string) (string, error) {
	cmd := exec.Command("sha256sum", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// listedFiles returns the file names a sha256sum checksum file names.
func listedFiles(t *testing.T, path string) []string {
	t.Helper()
	sums, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var listed []string
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("%s line %q is not '<digest>  <file>'", path, line)
		}
		listed = append(listed, strings.TrimPrefix(fields[1], "*"))
	}
	return listed
}

// uploadedAssets returns the dist/ files release.yml hands to
// `gh release create` as release assets, i.e. what someone can download.
func uploadedAssets(t *testing.T) []string {
	t.Helper()
	workflow := filepath.Join(".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("reading %s: %v", workflow, err)
	}
	lines := strings.Split(string(data), "\n")
	start := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "gh release create") })
	if start < 0 {
		t.Fatalf("%s runs no `gh release create`", workflow)
	}
	var invocation []string
	for _, line := range lines[start:] {
		line = strings.TrimSpace(line)
		invocation = append(invocation, strings.TrimSuffix(line, `\`))
		if !strings.HasSuffix(line, `\`) {
			break
		}
	}

	// Assets are the positional arguments. A dist/ path right after a flag
	// is that flag's value (--notes-file dist/notes.md), not an asset; a
	// valueless flag before an asset would trip this, and the resulting
	// mismatch is the signal to revisit here.
	fields := strings.Fields(strings.Join(invocation, " "))
	var assets []string
	for i, f := range fields {
		name, ok := strings.CutPrefix(f, "dist/")
		if !ok || (i > 0 && strings.HasPrefix(fields[i-1], "-")) {
			continue
		}
		assets = append(assets, name)
	}
	if len(assets) == 0 {
		t.Fatalf("no release assets found in %q", strings.Join(fields, " "))
	}
	return assets
}
