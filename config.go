package main

import (
	"bufio"
	"fmt"
	"os"
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
