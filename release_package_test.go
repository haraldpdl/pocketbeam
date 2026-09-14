//go:build !arm

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPackageRelease runs the release packaging script over a stand-in
// binary and checks the two contracts a release has to keep: SHA256SUMS
// verifies clean against the files the release actually publishes, and the
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

	sums, err := os.ReadFile(filepath.Join(dist, "SHA256SUMS"))
	if err != nil {
		t.Fatalf("reading SHA256SUMS: %v", err)
	}
	var listed []string
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("SHA256SUMS line %q is not '<digest>  <file>'", line)
		}
		listed = append(listed, strings.TrimPrefix(fields[1], "*"))
	}
	want := []string{"pocketbeam.app.gz", "pocketbeam-app.zip"}
	if strings.Join(listed, " ") != strings.Join(want, " ") {
		t.Errorf("SHA256SUMS lists %v, want %v (only published assets; GitHub refuses a .app asset)", listed, want)
	}

	// A downloader has the published assets and nothing else, so the raw
	// binary goes away before the check: with it listed, sha256sum reported
	// it missing and exited non-zero.
	if err := os.Remove(filepath.Join(dist, "pocketbeam.app")); err != nil {
		t.Fatalf("removing pocketbeam.app: %v", err)
	}
	check := exec.Command("sha256sum", "-c", "SHA256SUMS")
	check.Dir = dist
	if out, err := check.CombinedOutput(); err != nil {
		t.Errorf("sha256sum -c SHA256SUMS: %v\n%s", err, out)
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
}
