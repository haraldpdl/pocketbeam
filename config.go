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

// defaultUpdateURL is the release endpoint queried when check_updates is
// on and the config hasn't overridden it. Points at the Gitea instance
// during dev; the public release will flip this to a project-hosted
// JSON at build time via ldflags.
const defaultUpdateURL = "http://gitea.example.internal:3000/api/v1/repos/haraldpdl/pocketbeam/releases/latest"

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
	Library       string   // local target dir, e.g. /mnt/ext1/Books/CWA
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
				return fmt.Errorf("%s profile %q row %d: bad shelf_id %q: %w", path, c.Profile, n, kv.value, err)
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
			return fmt.Errorf("%s profile %q: unknown key %q", path, c.Profile, kv.key)
		}
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
