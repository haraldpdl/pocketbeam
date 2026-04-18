package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Host    string // base URL, e.g. http://cwa.example.internal:8083
	User    string // CWA user
	Pass    string // CWA password
	Library string // local target dir, e.g. /mnt/ext1/Books/CWA
	StateDB string // sqlite path, e.g. /mnt/ext1/system/config/bookbeam.db
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
	body := fmt.Sprintf(
		"host = %s\nuser = %s\npassword = %s\nlibrary = %s\nstate_db = %s\n",
		c.Host, c.User, c.Pass, c.Library, c.StateDB,
	)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
