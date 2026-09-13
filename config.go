package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// BackendOPDS and BackendWebDAV are the valid values for Config.Backend.
// An empty Backend is treated as OPDS for back-compat with configs written
// before backend support landed.
const (
	BackendOPDS   = "opds"
	BackendWebDAV = "webdav"
)

// defaultProfileName is used for the sole profile of a legacy (pre-profile)
// config that is upgraded in place, and as the name of the first profile
// the wizard creates.
const defaultProfileName = "default"

// sanitizeProfileName strips whitespace and characters that would confuse
// the config-file section parser (brackets, equals). Empty input returns
// "" so the wizard can re-ask.
func sanitizeProfileName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.Trim(s, "[]=")
	return s
}

// libraryFolderName returns the folder name under <flash>/Books that holds
// a profile's downloaded books. Naming it after the profile keeps two
// profiles pointing at different servers from sharing one folder. The name
// goes through the same filename filter as book titles because the
// device's storage is vfat; a name that filters away to nothing falls back
// to defaultProfileName rather than the opaque "_" used for filenames.
func libraryFolderName(profile string) string {
	return sanitizeWith(profile, defaultProfileName)
}

// libraryPathFor returns the library directory for a newly created
// profile: its filtered name under booksDir, suffixed with "-2", "-3", ...
// when another profile in the config file already downloads into that
// path. The filter is lossy ("a:b" and "a?b" both reduce to "a_b", and a
// name of only dots reduces to "default"), so two differently named
// profiles can derive one folder, and two servers writing into the same
// directory is exactly what per-profile folders exist to prevent. Stored
// and derived paths are compared through pathKey, so a hand-edited
// "Books/CWA/" and a derived "Books/cwa" count as the one directory they
// are on the device's FAT storage. An unreadable config file leaves
// nothing to collide with.
func libraryPathFor(cfgPath, booksDir, profile string) string {
	base := filepath.Join(booksDir, libraryFolderName(profile))
	taken := map[string]bool{}
	if doc, err := parseDoc(cfgPath); err == nil {
		for name, rows := range doc.sections {
			if name == profile {
				continue
			}
			for _, row := range rows {
				if row.key == "library" && row.value != "" {
					taken[pathKey(row.value)] = true
				}
			}
		}
	}
	path := base
	for n := 2; taken[pathKey(path)]; n++ {
		path = fmt.Sprintf("%s-%d", base, n)
	}
	return path
}

// profileNameTaken reports whether the config file already holds a
// profile called name. The wizard rejects a duplicate rather than
// letting SaveConfig overwrite the existing section, which would repoint
// that profile at a freshly derived library and orphan its books. Names
// are compared with ASCII case folded for the same reason library paths
// are: two profiles that differ only in case would fight over one folder
// on the device's FAT storage. A file that does not exist yet (or does
// not parse) holds no names to clash with.
func profileNameTaken(cfgPath, name string) bool {
	names, _, err := ListProfiles(cfgPath)
	if err != nil {
		return false
	}
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// probeInput carries the connection details the setup wizard collected,
// ready to be merged into the profile that gets written once the probe
// succeeds. Profile is empty when the wizard is editing an existing
// profile, Backend when the entry point skipped the backend question.
type probeInput struct {
	Profile  string
	Backend  string
	Host     string
	User     string
	Pass     string
	BooksDir string // parent directory of a newly derived library folder
	StateDB  string // default state DB path, used only when none is configured
}

// resolveProfileForProbe returns the stored profile the wizard is about
// to rewrite, or nil when it is creating a new one. Two entry points
// reach the probe as an edit: Settings -> "Change server info", which
// re-enters the wizard at the URL step with the active profile in
// session, and the first-run screen the app falls back to when the config
// file parses but the client or the store cannot be opened (a hand-edited
// host, an SD card that is not in the device yet). The second has no
// session, so the file on disk is the only source.
func resolveProfileForProbe(cfgPath string, session *Config, addProfile bool) *Config {
	if addProfile {
		return nil
	}
	if session != nil {
		return session
	}
	if cur, err := LoadConfig(cfgPath); err == nil {
		return cur
	}
	// LoadConfig rejects the whole file over one bad value (a typo'd
	// backend, a missing library, an unknown key), which is exactly the
	// state this entry point exists to repair. The section still names the
	// profile and usually still carries its library, so read it leniently:
	// treating the file as absent would derive a fresh folder, overwrite
	// the broken section with it, and orphan the books that profile
	// already downloaded.
	return recoverActiveProfile(cfgPath)
}

// recoverActiveProfile returns the active profile's stored fields without
// the validation LoadConfig applies: rows that do not parse are skipped,
// and a backend that is neither opds nor webdav is cleared so the wizard's
// answer (or the opds default) replaces it. Returns nil when the file
// holds no profile at all.
func recoverActiveProfile(path string) *Config {
	doc, err := parseDoc(path)
	if err != nil {
		return nil
	}
	active := doc.active
	if active == "" {
		if len(doc.order) == 0 {
			return nil
		}
		active = doc.order[0]
	}
	rows, ok := doc.sections[active]
	if !ok {
		// active names a section that is not in the file: it was renamed
		// or deleted by hand. Fall back to the first profile, as the
		// empty-active branch does. Returning nil here would send the
		// wizard down the new-profile path, which derives a fresh
		// Books/default and lets SaveConfig overwrite an existing
		// [default] section, orphaning the books it already holds.
		if len(doc.order) == 0 {
			return nil
		}
		active = doc.order[0]
		rows = doc.sections[active]
	}
	c := &Config{Profile: active, StateDB: doc.stateDB, CheckUpdates: true, UpdateURL: doc.updateURL}
	if doc.checkUpdatesSeen {
		c.CheckUpdates = doc.checkUpdates
	}
	for _, row := range rows {
		_ = applyProfileRow(c, row)
	}
	if c.Backend != BackendOPDS && c.Backend != BackendWebDAV {
		c.Backend = ""
	}
	return c
}

// profileAfterProbe returns the config to persist once the probe
// succeeds: the profile being edited with the newly entered connection
// details written over it, or a new profile when cur is nil. Everything
// the wizard does not ask about is carried over, because re-entering a
// URL and password must not silently widen the next sync (dropping an
// OPDS feed filter or a WebDAV sub-path) or flip a setting the user made
// (delete-missing, update checks). For a new profile the file's global
// keys are read off disk for the same reason: state_db, check_updates
// and update_url are shared by every profile and SaveConfig rewrites
// them from whatever config it is handed, so adding a profile would
// otherwise move the whole install's state DB and re-enable update
// checks. in.StateDB is only the default for an install that has none.
func profileAfterProbe(cfgPath string, cur *Config, in probeInput) *Config {
	cfg := &Config{CheckUpdates: true}
	if cur != nil {
		c := *cur
		cfg = &c
	} else if disk, err := LoadConfig(cfgPath); err == nil {
		cfg.StateDB, cfg.CheckUpdates, cfg.UpdateURL = disk.StateDB, disk.CheckUpdates, disk.UpdateURL
	}
	if in.Profile != "" {
		cfg.Profile = in.Profile
	}
	if cfg.Profile == "" {
		cfg.Profile = defaultProfileName
	}
	if in.Backend != "" {
		cfg.Backend = in.Backend
	}
	if cfg.Backend == "" {
		cfg.Backend = BackendOPDS
	}
	cfg.Host, cfg.User, cfg.Pass = in.Host, in.User, in.Pass
	if cfg.StateDB == "" {
		cfg.StateDB = in.StateDB
	}
	if cfg.Library == "" {
		cfg.Library = libraryPathFor(cfgPath, in.BooksDir, cfg.Profile)
	}
	if cfg.Backend == BackendWebDAV && cfg.Path == "" {
		cfg.Path = "/"
	}
	return cfg
}

// defaultUpdateURL is the release endpoint queried when check_updates is
// on and the config hasn't overridden it via update_url. It is a var so a
// build can point it elsewhere with
// -ldflags "-X main.defaultUpdateURL=https://host/path".
var defaultUpdateURL = "https://pocketbeam.shinyredapples.com/releases/latest"

// Config represents the currently-active profile's fields, plus its name.
// The on-disk file can hold multiple profiles as [sections]; LoadConfig
// returns the active one and the on-disk representation is managed
// separately (see fileDoc).
type Config struct {
	Profile       string   // name of the active profile; matches its section header on disk
	Backend       string   // "opds" (default) or "webdav"
	Host          string   // base URL, e.g. http://cwa.example.internal:8083
	User          string   // server user
	Pass          string   // server password
	Library       string   // local target dir, e.g. /mnt/ext1/Books/home (named after the profile by the wizard)
	StateDB       string   // sqlite path, e.g. /mnt/ext1/system/config/pocketbeam.db
	FilterHrefs   []string // OPDS only: one or more feed paths to sync (empty slice = all books). Each is a CWA shelf, generic subsection, or any other OPDS feed URL. Walking multiple produces a deduped union by UUID.
	FilterNames   []string // display labels parallel to FilterHrefs (picker sets these); surplus or missing entries are tolerated.
	Path          string   // WebDAV only: absolute directory on the server to mirror, e.g. "/Books/Fiction"
	DeleteMissing bool     // when true, sync removes local books absent from the current remote listing (opt-in; prompts for confirmation on device / stdin in CLI)
	CheckUpdates  bool     // when true, the app polls UpdateURL on startup (and on manual request) for a newer release
	UpdateURL     string   // release endpoint (Gitea / GitHub releases API). Empty falls back to defaultUpdateURL.
}

// FilterHref returns the first filter URL, or "" when no filter is set.
// Preserved for call sites that only care about the primary selection.
func (c *Config) FilterHref() string {
	if len(c.FilterHrefs) == 0 {
		return ""
	}
	return c.FilterHrefs[0]
}

// EffectiveUpdateURL returns the release endpoint to query, falling
// back to the compiled-in default when the config didn't override it.
func (c *Config) EffectiveUpdateURL() string {
	if c.UpdateURL != "" {
		return c.UpdateURL
	}
	return defaultUpdateURL
}

// FilterLabel returns a display-friendly description of the current
// filter: the single name, "N selected" for multi, or "All books" when
// nothing is set.
func (c *Config) FilterLabel() string {
	switch len(c.FilterHrefs) {
	case 0:
		return "All books"
	case 1:
		name := ""
		if len(c.FilterNames) > 0 {
			name = c.FilterNames[0]
		}
		if name == "" {
			name = c.FilterHrefs[0]
		}
		return name
	default:
		return fmt.Sprintf("%d selected", len(c.FilterHrefs))
	}
}

// fileDoc is the parsed in-memory representation of the whole config
// file. Top-level keys (active, state_db, check_updates, update_url)
// are shared across profiles; each [name] section carries one
// profile's fields as a flat key=value block.
type fileDoc struct {
	active           string
	stateDB          string
	checkUpdates     bool
	checkUpdatesSeen bool // distinguishes absent-from-file (default true) from explicit "off"
	updateURL        string
	order            []string            // section names in file order, for stable serialisation
	sections         map[string][]kvLine // per-section ordered key/value pairs
}

// kvLine preserves insertion order when we rewrite a section so the file
// remains diff-friendly across saves.
type kvLine struct {
	key, value string
}

// LoadConfig reads the file and returns the active profile's fields.
// Legacy files with no [sections] are treated as a single profile named
// "default" for back-compat.
func LoadConfig(path string) (*Config, error) {
	doc, err := parseDoc(path)
	if err != nil {
		return nil, err
	}
	active := doc.active
	if active == "" {
		// Pick the first section if no explicit active was set.
		if len(doc.order) > 0 {
			active = doc.order[0]
		} else {
			return nil, fmt.Errorf("%s: no profiles defined", path)
		}
	}
	rows, ok := doc.sections[active]
	if !ok {
		return nil, fmt.Errorf("%s: active profile %q has no section", path, active)
	}
	// CheckUpdates defaults to true so existing configs (written before
	// the key existed, or migrated from the pre-rename bookbeam.cfg)
	// opt in by default. An explicit `check_updates = off` still wins.
	checkUpdates := true
	if doc.checkUpdatesSeen {
		checkUpdates = doc.checkUpdates
	}
	c := &Config{
		Profile:      active,
		StateDB:      doc.stateDB,
		CheckUpdates: checkUpdates,
		UpdateURL:    doc.updateURL,
	}
	if err := applyProfile(c, path, rows); err != nil {
		return nil, err
	}
	if c.Backend == "" {
		c.Backend = BackendOPDS
	}
	if c.Backend != BackendOPDS && c.Backend != BackendWebDAV {
		return nil, fmt.Errorf("%s: unknown backend %q (expected opds or webdav)", path, c.Backend)
	}
	for k, v := range map[string]string{"host": c.Host, "library": c.Library, "state_db": c.StateDB} {
		if v == "" {
			return nil, fmt.Errorf("%s: missing required key %q", path, k)
		}
	}
	return c, nil
}

// LoadProfileByName reads one profile's fields without making it the
// active profile. Useful for UI screens that show another profile's
// server / backend alongside the active one.
func LoadProfileByName(path, name string) (*Config, error) {
	doc, err := parseDoc(path)
	if err != nil {
		return nil, err
	}
	rows, ok := doc.sections[name]
	if !ok {
		return nil, fmt.Errorf("profile %q does not exist", name)
	}
	c := &Config{Profile: name, StateDB: doc.stateDB}
	if err := applyProfile(c, path, rows); err != nil {
		return nil, err
	}
	if c.Backend == "" {
		c.Backend = BackendOPDS
	}
	return c, nil
}

// ListProfiles returns the names of all profiles in the file, plus the
// name of the active one.
func ListProfiles(path string) (names []string, active string, err error) {
	doc, err := parseDoc(path)
	if err != nil {
		return nil, "", err
	}
	return append([]string(nil), doc.order...), doc.active, nil
}

// SetActiveProfile updates the active profile in the file without
// rewriting any profile section. Errors if name is not a known profile.
func SetActiveProfile(path, name string) error {
	doc, err := parseDoc(path)
	if err != nil {
		return err
	}
	if _, ok := doc.sections[name]; !ok {
		return fmt.Errorf("profile %q does not exist", name)
	}
	doc.active = name
	return writeDoc(path, doc)
}

// DeleteProfile removes a profile from the file. If it was the active
// profile, the first remaining profile is promoted. Deleting the last
// profile leaves the file empty of sections and with no active marker;
// the next LoadConfig will fail cleanly, which the caller should
// translate into a first-run wizard.
func DeleteProfile(path, name string) error {
	doc, err := parseDoc(path)
	if err != nil {
		return err
	}
	if _, ok := doc.sections[name]; !ok {
		return fmt.Errorf("profile %q does not exist", name)
	}
	delete(doc.sections, name)
	filtered := doc.order[:0]
	for _, n := range doc.order {
		if n != name {
			filtered = append(filtered, n)
		}
	}
	doc.order = filtered
	if doc.active == name {
		if len(doc.order) > 0 {
			doc.active = doc.order[0]
		} else {
			doc.active = ""
		}
	}
	return writeDoc(path, doc)
}

// SaveConfig persists the given profile into the file, preserving any
// other profiles already on disk. If the file does not exist yet, it is
// created with the profile marked active. A missing Config.Profile
// defaults to "default".
func SaveConfig(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	profile := c.Profile
	if profile == "" {
		profile = defaultProfileName
	}
	// Load existing doc or start fresh.
	doc, err := parseDoc(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if doc == nil {
		doc = &fileDoc{sections: map[string][]kvLine{}}
	}
	// Globals come from c.
	if c.StateDB != "" {
		doc.stateDB = c.StateDB
	}
	doc.checkUpdates = c.CheckUpdates
	if c.UpdateURL != "" {
		doc.updateURL = c.UpdateURL
	}
	if doc.active == "" {
		doc.active = profile
	}
	if _, existed := doc.sections[profile]; !existed {
		doc.order = append(doc.order, profile)
	}
	doc.sections[profile] = profileToKV(c)
	return writeDoc(path, doc)
}

// parseDoc reads path and returns an in-memory document. A legacy file
// with no [sections] is wrapped into a single "default" section.
func parseDoc(path string) (*fileDoc, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	doc := &fileDoc{sections: map[string][]kvLine{}}
	current := "" // "" means "top-level, no section seen yet"

	s := bufio.NewScanner(f)
	for n := 1; s.Scan(); n++ {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.TrimSpace(line[1 : len(line)-1])
			if current == "" {
				return nil, fmt.Errorf("%s:%d: empty section name", path, n)
			}
			if _, ok := doc.sections[current]; !ok {
				doc.sections[current] = nil
				doc.order = append(doc.order, current)
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: missing '='", path, n)
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if current == "" {
			switch key {
			case "active":
				doc.active = val
			case "state_db":
				doc.stateDB = val
			case "check_updates":
				doc.checkUpdates = parseBool(val)
				doc.checkUpdatesSeen = true
			case "update_url":
				doc.updateURL = val
			default:
				// Legacy flat-format key; route it to the implicit
				// default profile so pre-profile configs still load.
				if _, ok := doc.sections[defaultProfileName]; !ok {
					doc.sections[defaultProfileName] = nil
					doc.order = append(doc.order, defaultProfileName)
				}
				doc.sections[defaultProfileName] = append(doc.sections[defaultProfileName], kvLine{key: key, value: val})
			}
			continue
		}
		doc.sections[current] = append(doc.sections[current], kvLine{key: key, value: val})
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	// If we only saw legacy flat keys, promote the implicit default as active.
	if doc.active == "" && len(doc.order) == 1 {
		doc.active = doc.order[0]
	}
	return doc, nil
}

// writeDoc serialises the document atomically.
func writeDoc(path string, doc *fileDoc) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	if doc.active != "" {
		fmt.Fprintf(&b, "active = %s\n", doc.active)
	}
	if doc.stateDB != "" {
		fmt.Fprintf(&b, "state_db = %s\n", doc.stateDB)
	}
	// Always write check_updates explicitly so a false value survives a
	// save / reload round-trip (absent-from-file now means "default on").
	if doc.checkUpdates {
		fmt.Fprintf(&b, "check_updates = on\n")
	} else {
		fmt.Fprintf(&b, "check_updates = off\n")
	}
	if doc.updateURL != "" {
		fmt.Fprintf(&b, "update_url = %s\n", doc.updateURL)
	}
	b.WriteString("\n")
	for _, name := range doc.order {
		fmt.Fprintf(&b, "[%s]\n", name)
		for _, kv := range doc.sections[name] {
			fmt.Fprintf(&b, "%s = %s\n", kv.key, kv.value)
		}
		b.WriteString("\n")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// applyProfile copies the parsed key/value pairs onto c. Back-compat
// aliases (shelf_id / shelf_name) are honoured here so legacy sections
// continue to load cleanly.
func applyProfile(c *Config, path string, rows []kvLine) error {
	for n, kv := range rows {
		if err := applyProfileRow(c, kv); err != nil {
			return fmt.Errorf("%s profile %q row %d: %w", path, c.Profile, n, err)
		}
	}
	return nil
}

// applyProfileRow writes one key/value pair onto c. Split out of
// applyProfile so the recovery path can apply the rows it understands and
// drop the rest.
func applyProfileRow(c *Config, kv kvLine) error {
	switch kv.key {
	case "backend":
		c.Backend = kv.value
	case "host":
		c.Host = strings.TrimRight(kv.value, "/")
	case "user":
		c.User = kv.value
	case "password":
		c.Pass = kv.value
	case "library":
		c.Library = kv.value
	case "filter_href":
		c.FilterHrefs = append(c.FilterHrefs, kv.value)
	case "filter_name":
		c.FilterNames = append(c.FilterNames, kv.value)
	case "path":
		c.Path = kv.value
	case "delete_missing":
		c.DeleteMissing = parseBool(kv.value)
	case "shelf_id":
		id, err := strconv.Atoi(kv.value)
		if err != nil {
			return fmt.Errorf("bad shelf_id %q: %w", kv.value, err)
		}
		if id > 0 && len(c.FilterHrefs) == 0 {
			c.FilterHrefs = append(c.FilterHrefs, fmt.Sprintf("/opds/shelf/%d", id))
		}
	case "shelf_name":
		if len(c.FilterNames) == 0 {
			c.FilterNames = append(c.FilterNames, kv.value)
		}
	case "state_db":
		// Legacy flat-format configs wrote state_db inside the
		// (implicit) default section. Promote it to the global slot.
		if c.StateDB == "" {
			c.StateDB = kv.value
		}
	default:
		return fmt.Errorf("unknown key %q", kv.key)
	}
	return nil
}

// profileToKV flattens a Config's profile-scoped fields into key/value
// rows ready for writeDoc. Order matches the classic layout so saved
// files stay diff-friendly.
func profileToKV(c *Config) []kvLine {
	rows := []kvLine{
		{"backend", fallback(c.Backend, BackendOPDS)},
		{"host", c.Host},
		{"user", c.User},
		{"password", c.Pass},
		{"library", c.Library},
	}
	backend := c.Backend
	if backend == "" {
		backend = BackendOPDS
	}
	if backend == BackendOPDS {
		for i, href := range c.FilterHrefs {
			rows = append(rows, kvLine{"filter_href", href})
			if i < len(c.FilterNames) && c.FilterNames[i] != "" {
				rows = append(rows, kvLine{"filter_name", c.FilterNames[i]})
			}
		}
	}
	if backend == BackendWebDAV && c.Path != "" {
		rows = append(rows, kvLine{"path", c.Path})
	}
	if c.DeleteMissing {
		rows = append(rows, kvLine{"delete_missing", "on"})
	}
	return rows
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// parseBool accepts the human-friendly spellings of true/false commonly
// typed into config files: on/off, true/false, yes/no, 1/0. Unknown
// values parse as false so a typo cannot silently enable a destructive
// flag.
func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		return true
	}
	return false
}
