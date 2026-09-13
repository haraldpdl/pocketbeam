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
		{"sci/fi:shelf", "sci_fi_shelf"}, // path separators and FAT-illegal chars
		{"", "default"},                  // no name at all
		{"...", "default"},               // filters away to nothing
	}
	for _, tc := range cases {
		if got := libraryFolderName(tc.in); got != tc.want {
			t.Errorf("libraryFolderName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestProfileNameToLibraryFolder covers the composition the device runs:
// keyboard input becomes a profile name through sanitizeProfileName, and
// only that name reaches libraryFolderName.
func TestProfileNameToLibraryFolder(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Nextcloud Books", "Nextcloud-Books"}, // spaces become dashes in the name itself
		{"  spaced  ", "spaced"},               // outer whitespace is gone before the filter
		{"sci/fi", "sci_fi"},                   // the filter still has to catch separators
		{"...", "default"},
	}
	for _, tc := range cases {
		if got := libraryFolderName(sanitizeProfileName(tc.in)); got != tc.want {
			t.Errorf("libraryFolderName(sanitizeProfileName(%q)) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLibraryPathForAvoidsExistingLibraries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pocketbeam.cfg")
	cfg := "active = a:b\nstate_db = /db.sqlite\n\n" +
		"[a:b]\nhost = http://one.lan\nlibrary = /mnt/ext1/Books/a_b\n\n" +
		"[a?b]\nhost = http://two.lan\nlibrary = /mnt/ext1/Books/a_b-2\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	books := "/mnt/ext1/Books"
	// A third name that filters to the same folder skips both taken ones.
	if got := libraryPathFor(path, books, "a*b"); got != "/mnt/ext1/Books/a_b-3" {
		t.Errorf("libraryPathFor(a*b) = %q, want /mnt/ext1/Books/a_b-3", got)
	}
	// A profile does not collide with itself: re-deriving keeps the folder.
	if got := libraryPathFor(path, books, "a:b"); got != "/mnt/ext1/Books/a_b" {
		t.Errorf("libraryPathFor(a:b) = %q, want /mnt/ext1/Books/a_b", got)
	}
	// An unrelated name is untouched, and so is the first run, where the
	// config file does not exist yet.
	if got := libraryPathFor(path, books, "nas"); got != "/mnt/ext1/Books/nas" {
		t.Errorf("libraryPathFor(nas) = %q, want /mnt/ext1/Books/nas", got)
	}
	if got := libraryPathFor(filepath.Join(dir, "absent.cfg"), books, "default"); got != "/mnt/ext1/Books/default" {
		t.Errorf("libraryPathFor(first run) = %q, want /mnt/ext1/Books/default", got)
	}
}

// A legacy install's library differs only in case from what a new profile
// derives. The device's FAT storage cannot tell the two apart, so the
// collision has to be detected with case folded.
func TestLibraryPathForFoldsCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pocketbeam.cfg")
	cfg := "active = legacy\nstate_db = /db.sqlite\n\n" +
		"[legacy]\nhost = http://one.lan\nlibrary = /mnt/ext1/Books/CWA\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := libraryPathFor(path, "/mnt/ext1/Books", "cwa"); got != "/mnt/ext1/Books/cwa-2" {
		t.Errorf("libraryPathFor(cwa) = %q, want /mnt/ext1/Books/cwa-2", got)
	}
}

func TestProfileNameTaken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pocketbeam.cfg")
	cfg := "active = home\nstate_db = /db.sqlite\n\n" +
		"[home]\nhost = http://one.lan\nlibrary = /mnt/ext1/Books/CWA\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cases := []struct {
		name string
		want bool
	}{
		{"home", true},
		{"HOME", true}, // one folder on FAT, so one name here too
		{"nas", false},
	}
	for _, tc := range cases {
		if got := profileNameTaken(path, tc.name); got != tc.want {
			t.Errorf("profileNameTaken(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	// Nothing to clash with before the first profile exists.
	if profileNameTaken(filepath.Join(dir, "absent.cfg"), "home") {
		t.Error("profileNameTaken on a missing file = true, want false")
	}
}

func TestResolveProfileForProbe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pocketbeam.cfg")
	cfg := "active = home\nstate_db = /db.sqlite\n\n" +
		"[home]\nbackend = webdav\nhost = http://one.lan\nlibrary = /mnt/ext1/Books/CWA\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	session := &Config{Profile: "session", Host: "http://session.lan"}

	// Adding a profile never reuses another one, session or not.
	if got := resolveProfileForProbe(path, session, true); got != nil {
		t.Errorf("addProfile: got %+v, want nil", got)
	}
	// Editing prefers the profile in session.
	if got := resolveProfileForProbe(path, session, false); got != session {
		t.Errorf("with session: got %+v, want the session config", got)
	}
	// No session (the app fell back to the wizard after a client or store
	// failure): the file on disk is the source.
	got := resolveProfileForProbe(path, nil, false)
	if got == nil || got.Profile != "home" {
		t.Fatalf("without session: got %+v, want the stored home profile", got)
	}
	// No session and no readable config: a fresh install.
	if got := resolveProfileForProbe(filepath.Join(dir, "absent.cfg"), nil, false); got != nil {
		t.Errorf("first run: got %+v, want nil", got)
	}
}

func TestProfileAfterProbeKeepsStoredProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pocketbeam.cfg")
	cur := &Config{
		Profile:       "home",
		Backend:       BackendWebDAV,
		Host:          "http://old.lan",
		User:          "old",
		Pass:          "old",
		Library:       "/mnt/ext1/Books/CWA",
		StateDB:       "/mnt/ext1/system/config/pocketbeam.db",
		Path:          "/Books/Fiction",
		DeleteMissing: true,
		FilterHrefs:   []string{"/opds/shelf/3"},
		FilterNames:   []string{"Shelf"},
	}
	got := profileAfterProbe(path, cur, probeInput{
		Host: "http://new.lan", User: "new", Pass: "secret",
		BooksDir: "/mnt/ext1/Books", StateDB: "/other.db",
	})
	if got.Host != "http://new.lan" || got.User != "new" || got.Pass != "secret" {
		t.Errorf("connection details not applied: %+v", got)
	}
	// "Change server info" skips the backend step and asks for nothing
	// else, so everything the wizard did not collect stays as stored.
	if got.Backend != BackendWebDAV {
		t.Errorf("Backend = %q, want webdav (probing it as OPDS would fail)", got.Backend)
	}
	if got.Profile != "home" || got.Library != "/mnt/ext1/Books/CWA" {
		t.Errorf("profile/library changed: %q / %q", got.Profile, got.Library)
	}
	if got.Path != "/Books/Fiction" {
		t.Errorf("Path = %q, want the stored sub-directory", got.Path)
	}
	if !got.DeleteMissing || !reflect.DeepEqual(got.FilterHrefs, cur.FilterHrefs) ||
		!reflect.DeepEqual(got.FilterNames, cur.FilterNames) {
		t.Errorf("settings dropped: %+v", got)
	}
	if got.StateDB != cur.StateDB {
		t.Errorf("StateDB = %q, want the configured %q", got.StateDB, cur.StateDB)
	}
	if cur.Host != "http://old.lan" {
		t.Errorf("the stored config was mutated in place: %+v", cur)
	}
}

func TestProfileAfterProbeNewProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pocketbeam.cfg")
	cfg := "active = home\nstate_db = /db.sqlite\ncheck_updates = off\n\n" +
		"[home]\nhost = http://one.lan\nlibrary = /mnt/ext1/Books/home\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := profileAfterProbe(path, nil, probeInput{
		Profile: "nas", Backend: BackendWebDAV,
		Host: "http://two.lan", User: "u", Pass: "p",
		BooksDir: "/mnt/ext1/Books", StateDB: "/mnt/ext1/system/config/pocketbeam.db",
	})
	if got.Profile != "nas" || got.Library != "/mnt/ext1/Books/nas" {
		t.Errorf("profile/library = %q / %q, want nas / /mnt/ext1/Books/nas", got.Profile, got.Library)
	}
	if got.Path != "/" {
		t.Errorf("Path = %q, want / for a new WebDAV profile", got.Path)
	}
	if got.StateDB != "/db.sqlite" {
		t.Errorf("StateDB = %q, want the file's global /db.sqlite", got.StateDB)
	}
	// check_updates is a global key that SaveConfig rewrites from the
	// config it is handed; adding a profile must not switch it back on.
	if got.CheckUpdates {
		t.Error("CheckUpdates = true, want the file's explicit off")
	}
}

func TestProfileAfterProbeFirstRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.cfg")
	got := profileAfterProbe(path, nil, probeInput{
		Host: "http://one.lan", BooksDir: "/mnt/ext1/Books", StateDB: "/state.db",
	})
	if got.Profile != defaultProfileName || got.Library != "/mnt/ext1/Books/default" {
		t.Errorf("profile/library = %q / %q, want default / /mnt/ext1/Books/default", got.Profile, got.Library)
	}
	if got.Backend != BackendOPDS {
		t.Errorf("Backend = %q, want opds", got.Backend)
	}
	if got.StateDB != "/state.db" || !got.CheckUpdates {
		t.Errorf("StateDB/CheckUpdates = %q / %v, want /state.db / true", got.StateDB, got.CheckUpdates)
	}
}
