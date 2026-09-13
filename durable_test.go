package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncClose_FlushesAndCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.epub")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteString("payload"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := syncClose(f); err != nil {
		t.Fatalf("syncClose: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("content = %q, want %q", got, "payload")
	}
	// The descriptor must be gone, otherwise a long sync run would leak
	// one per downloaded book.
	if _, err := f.WriteString("more"); err == nil {
		t.Error("write after syncClose succeeded, file was left open")
	}
}

func TestSyncClose_ReportsErrorWithoutPanicking(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "staged"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// An already-closed file makes both Sync and Close fail; the caller
	// relies on getting that error so it can drop the staged file
	// instead of renaming unflushed data into place.
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := syncClose(f); err == nil {
		t.Error("syncClose on a closed file returned nil")
	}
}

func TestSyncDir_ToleratesMissingDirectory(t *testing.T) {
	// Callers ignore the outcome, so the only contract is "never blows up
	// and never leaves the directory open".
	syncDir(filepath.Join(t.TempDir(), "does-not-exist"))
	syncDir(t.TempDir())
}
