package main

import (
	"os"
	"path/filepath"
	"reflect"
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
	if got.FilterHref() != "/opds/shelf/5" {
		t.Errorf("FilterHref() = %q, want /opds/shelf/5", got.FilterHref())
	}
	if len(got.FilterNames) == 0 || got.FilterNames[0] != "old-shelf" {
		t.Errorf("FilterNames = %v, want first entry old-shelf", got.FilterNames)
	}
}

func TestConfigRoundTripWebDAV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "webdav.cfg")
	want := &Config{
		Profile: "default",
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
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch\n got=%+v\nwant=%+v", got, want)
	}
}

func TestProfilesAddListSwitchDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.cfg")

	first := &Config{
		Profile: "home",
		Backend: BackendOPDS,
		Host:    "http://cwa.home:8083",
		User:    "me",
		Pass:    "x",
		Library: "/lib",
		StateDB: "/state.db",
	}
	if err := SaveConfig(path, first); err != nil {
		t.Fatalf("save home: %v", err)
	}
	second := &Config{
		Profile: "nas",
		Backend: BackendWebDAV,
		Host:    "https://nas.example",
		User:    "me",
		Pass:    "x",
		Library: "/lib",
		StateDB: "/state.db",
		Path:    "/Books",
	}
	if err := SaveConfig(path, second); err != nil {
		t.Fatalf("save nas: %v", err)
	}

	names, active, err := ListProfiles(path)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if active != "home" {
		t.Errorf("active = %q, want home (first created stays active)", active)
	}
	if len(names) != 2 || names[0] != "home" || names[1] != "nas" {
		t.Errorf("names = %v, want [home nas]", names)
	}

	if err := SetActiveProfile(path, "nas"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load after switch: %v", err)
	}
	if got.Profile != "nas" || got.Backend != BackendWebDAV {
		t.Errorf("after switch: got %+v", got)
	}

	if err := DeleteProfile(path, "home"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	names, active, err = ListProfiles(path)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(names) != 1 || names[0] != "nas" || active != "nas" {
		t.Errorf("after delete: names=%v active=%q", names, active)
	}
	// Deleting the last profile now succeeds (leaves the file empty);
	// the caller is expected to drop the user into the first-run
	// wizard when LoadConfig subsequently reports no profiles.
	if err := DeleteProfile(path, "nas"); err != nil {
		t.Errorf("deleting last profile: %v", err)
	}
	names, active, err = ListProfiles(path)
	if err != nil {
		t.Fatalf("list after empty: %v", err)
	}
	if len(names) != 0 || active != "" {
		t.Errorf("after deleting last profile: names=%v active=%q, want empty", names, active)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Errorf("LoadConfig should fail on empty config so the caller can trigger first-run")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "pocketbeam.cfg")

	want := &Config{
		Profile:     "default",
		Backend:     BackendOPDS,
		Host:        "http://cwa.lan:8083",
		User:        "alice",
		Pass:        "hunter2",
		Library:     "/mnt/ext1/Books/CWA",
		StateDB:     "/mnt/ext1/system/config/pocketbeam.db",
		FilterHrefs: []string{"/opds/shelf/7"},
		FilterNames: []string{"to-pocketbook"},
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
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch\n got=%+v\nwant=%+v", got, want)
	}
}

func TestConfigMultiFilterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.cfg")
	want := &Config{
		Profile:     "default",
		Backend:     BackendOPDS,
		Host:        "http://cwa.lan:8083",
		User:        "alice",
		Pass:        "pw",
		Library:     "/lib",
		StateDB:     "/db.sqlite",
		FilterHrefs: []string{"/opds/shelf/3", "/opds/category/tag/7"},
		FilterNames: []string{"to-pocketbook", "Fantasy"},
	}
	if err := SaveConfig(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(got.FilterHrefs, want.FilterHrefs) {
		t.Errorf("FilterHrefs = %v, want %v", got.FilterHrefs, want.FilterHrefs)
	}
	if !reflect.DeepEqual(got.FilterNames, want.FilterNames) {
		t.Errorf("FilterNames = %v, want %v", got.FilterNames, want.FilterNames)
	}
	if got.FilterLabel() != "2 selected" {
		t.Errorf("FilterLabel = %q, want %q", got.FilterLabel(), "2 selected")
	}
}

func TestLibraryFolderName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"home", "home"},
		{"Nextcloud Books", "Nextcloud Books"}, // spaces survive, they are legal on vfat
		{"sci/fi:shelf", "sci_fi_shelf"},       // path separators and FAT-illegal chars
		{"  spaced  ", "spaced"},               // outer whitespace trimmed
		{"", "default"},                        // no name at all
		{"...", "default"},                     // filters away to nothing
	}
	for _, tc := range cases {
		if got := libraryFolderName(tc.in); got != tc.want {
			t.Errorf("libraryFolderName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
