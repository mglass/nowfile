// Package config reads the nowfile server's configuration from the
// environment.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config is the server's configuration. Fields map to NOWFILE_* environment
// variables.
type Config struct {
	// Dev is NOWFILE_DEV=1. It changes the defaults for FingerAddr and DBPath
	// so the server runs unprivileged on a laptop.
	Dev bool
	// HTTPAddr is NOWFILE_HTTP_ADDR, default ":8080".
	HTTPAddr string
	// FingerAddr is NOWFILE_FINGER_ADDR, default ":79", or ":7979" in dev.
	FingerAddr string
	// DBPath is NOWFILE_DB_PATH, default "/data/app.db", or "data/app.db"
	// (relative to the working directory, gitignored) in dev.
	DBPath string
	// BaseURL is NOWFILE_BASE_URL, the public origin without a trailing
	// slash, for example "https://nowfile.org". Default
	// "http://localhost" plus the HTTP port.
	BaseURL string
	// PostmarkToken is NOWFILE_POSTMARK_TOKEN, the server token for sign-in
	// mail. Empty means mail is not configured.
	PostmarkToken string
	// MailFrom is NOWFILE_MAIL_FROM, the sender address for sign-in mail.
	MailFrom string
	// AdminToken is NOWFILE_ADMIN_TOKEN. Empty means admin over HTTP is off.
	AdminToken string
}

// FromEnv reads the configuration from the process environment.
func FromEnv() Config { return Load(os.Getenv) }

// Load reads the configuration through getenv, so tests can supply their
// own environment.
func Load(getenv func(string) string) Config {
	c := Config{
		Dev:           getenv("NOWFILE_DEV") == "1",
		HTTPAddr:      getenv("NOWFILE_HTTP_ADDR"),
		FingerAddr:    getenv("NOWFILE_FINGER_ADDR"),
		DBPath:        getenv("NOWFILE_DB_PATH"),
		BaseURL:       strings.TrimRight(getenv("NOWFILE_BASE_URL"), "/"),
		PostmarkToken: getenv("NOWFILE_POSTMARK_TOKEN"),
		MailFrom:      getenv("NOWFILE_MAIL_FROM"),
		AdminToken:    getenv("NOWFILE_ADMIN_TOKEN"),
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8080"
	}
	if c.FingerAddr == "" {
		c.FingerAddr = ":79"
		if c.Dev {
			c.FingerAddr = ":7979"
		}
	}
	if c.DBPath == "" {
		c.DBPath = "/data/app.db"
		if c.Dev {
			c.DBPath = "data/app.db"
		}
	}
	if c.BaseURL == "" {
		host := c.HTTPAddr
		if strings.HasPrefix(host, ":") {
			host = "localhost" + host
		}
		c.BaseURL = "http://" + host
	}
	return c
}

// String describes the configuration for logs. Secrets are reported only as
// set or unset.
func (c Config) String() string {
	set := func(s string) string {
		if s == "" {
			return "unset"
		}
		return "set"
	}
	return fmt.Sprintf("dev=%v http=%s finger=%s db=%s base_url=%s mail_from=%q postmark_token=%s admin_token=%s",
		c.Dev, c.HTTPAddr, c.FingerAddr, c.DBPath, c.BaseURL, c.MailFrom, set(c.PostmarkToken), set(c.AdminToken))
}
