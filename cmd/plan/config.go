package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// config is the CLI's saved settings.
type config struct {
	BaseURL string // base_url, for example https://nowfile.org
	Token   string // token, a per-device API token
	Name    string // name, the user's own username, for plan show
}

// defaultConfigPath is $XDG_CONFIG_HOME/nowfile/config, else
// ~/.config/nowfile/config on every platform, including macOS.
func defaultConfigPath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "nowfile", "config")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "nowfile", "config")
	}
	return filepath.Join(home, ".config", "nowfile", "config")
}

// loadConfig reads key=value lines. Blank lines and lines starting with #
// are skipped, unknown keys ignored. A missing file is an empty config.
func loadConfig(path string) (config, error) {
	var c config
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return c, fmt.Errorf("%s:%d: want key=value", path, n)
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "base_url":
			c.BaseURL = strings.TrimRight(v, "/")
		case "token":
			c.Token = v
		case "name":
			c.Name = v
		}
	}
	return c, sc.Err()
}

// saveConfig writes the config readable only by the user, since it holds
// a token.
func saveConfig(path string, c config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# nowfile CLI config, written by plan login\n")
	fmt.Fprintf(&b, "base_url=%s\ntoken=%s\n", c.BaseURL, c.Token)
	if c.Name != "" {
		fmt.Fprintf(&b, "name=%s\n", c.Name)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
