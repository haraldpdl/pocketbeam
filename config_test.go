package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "bookbeam.cfg")

	want := &Config{
		Host:    "http://cwa.lan:8083",
		User:    "alice",
		Pass:    "hunter2",
		Library: "/mnt/ext1/Books/CWA",
		StateDB: "/mnt/ext1/system/config/bookbeam.db",
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
