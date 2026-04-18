package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Host       string // base URL, e.g. http://cwa.example.internal:8083
	User       string // OPDS server user
	Pass       string // OPDS server password
	Library    string // local target dir, e.g. /mnt/ext1/Books/CWA
	StateDB    string // sqlite path, e.g. /mnt/ext1/system/config/bookbeam.db
	FilterHref string // OPDS path to sync (empty = all books). Works for CWA shelves AND generic subsections.
	FilterName string // last-known display name of the filter (cache; refreshed in the picker)
}

// LoadConfig reads a flat key=value file. Comments begin with #. Whitespace
// around keys, values, and the `=` is trimmed. Values are taken verbatim
// (no quoting) so passwords with spaces work as long as there's no leading/trailing space.
func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	c := &Config{}
	s := bufio.NewScanner(f)
	for n := 1; s.Scan(); n++ {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: missing '='", path, n)
		}
		switch strings.TrimSpace(k) {
		case "host":
			c.Host = strings.TrimRight(strings.TrimSpace(v), "/")
		case "user":
			c.User = strings.TrimSpace(v)
		case "password":
			c.Pass = strings.TrimSpace(v)
		case "library":
			c.Library = strings.TrimSpace(v)
		case "state_db":
			c.StateDB = strings.TrimSpace(v)
		case "filter_href":
			c.FilterHref = strings.TrimSpace(v)
		case "filter_name":
			c.FilterName = strings.TrimSpace(v)
		case "shelf_id":
			// Back-compat: old configs wrote shelf_id. Convert to filter_href.
			id, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("%s:%d: bad shelf_id %q: %w", path, n, v, err)
			}
			if id > 0 && c.FilterHref == "" {
				c.FilterHref = fmt.Sprintf("/opds/shelf/%d", id)
			}
		case "shelf_name":
			// Back-compat: old configs wrote shelf_name. Preserve as the display name.
			if c.FilterName == "" {
				c.FilterName = strings.TrimSpace(v)
			}
		default:
			return nil, fmt.Errorf("%s:%d: unknown key %q", path, n, k)
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"host": c.Host, "library": c.Library, "state_db": c.StateDB} {
		if v == "" {
			return nil, fmt.Errorf("%s: missing required key %q", path, k)
		}
	}
	return c, nil
}

// SaveConfig writes the config atomically to path. Parent directories are
// created as needed. The file is created with mode 0600 because it contains
// credentials.
func SaveConfig(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "host = %s\n", c.Host)
	fmt.Fprintf(&b, "user = %s\n", c.User)
	fmt.Fprintf(&b, "password = %s\n", c.Pass)
	fmt.Fprintf(&b, "library = %s\n", c.Library)
	fmt.Fprintf(&b, "state_db = %s\n", c.StateDB)
	if c.FilterHref != "" {
		fmt.Fprintf(&b, "filter_href = %s\n", c.FilterHref)
		if c.FilterName != "" {
			fmt.Fprintf(&b, "filter_name = %s\n", c.FilterName)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
