package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigBackCompatShelfID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.cfg")
	old := "host = http://cwa.lan:8083\n" +
		"user = alice\npassword = pw\n" +
		"library = /lib\nstate_db = /db.sqlite\n" +
		"shelf_id = 5\nshelf_name = old-shelf\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.FilterHref != "/opds/shelf/5" {
		t.Errorf("FilterHref = %q, want /opds/shelf/5", got.FilterHref)
	}
	if got.FilterName != "old-shelf" {
		t.Errorf("FilterName = %q, want old-shelf", got.FilterName)
	}
}

func TestConfigRoundTripWebDAV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "webdav.cfg")
	want := &Config{
		Backend: BackendWebDAV,
		Host:    "https://nc.example/remote.php/dav/files/alice",
		User:    "alice",
		Pass:    "hunter2",
		Library: "/mnt/ext1/Books/WebDAV",
		StateDB: "/mnt/ext1/system/config/pocketbeam.db",
		Path:    "/Books/Fiction",
	}
	if err := SaveConfig(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if *got != *want {
		t.Errorf("round-trip mismatch\n got=%+v\nwant=%+v", got, want)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "pocketbeam.cfg")

	want := &Config{
		Backend:    BackendOPDS,
		Host:       "http://cwa.lan:8083",
		User:       "alice",
		Pass:       "hunter2",
		Library:    "/mnt/ext1/Books/CWA",
		StateDB:    "/mnt/ext1/system/config/pocketbeam.db",
		FilterHref: "/opds/shelf/7",
		FilterName: "to-pocketbook",
	}
	if err := SaveConfig(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}

	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if *got != *want {
		t.Errorf("round-trip mismatch\n got=%+v\nwant=%+v", got, want)
	}
}
