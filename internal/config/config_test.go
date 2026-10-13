package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c := Load(env(nil))
	if c.Dev || c.HTTPAddr != ":8080" || c.FingerAddr != ":79" || c.DBPath != "/data/app.db" || c.BaseURL != "http://localhost:8080" {
		t.Errorf("defaults: %+v", c)
	}
	d := Load(env(map[string]string{"NOWFILE_DEV": "1"}))
	if !d.Dev || d.FingerAddr != ":7979" || d.DBPath != "data/app.db" {
		t.Errorf("dev defaults: %+v", d)
	}
}

func TestOverrides(t *testing.T) {
	c := Load(env(map[string]string{
		"NOWFILE_DEV":            "1",
		"NOWFILE_HTTP_ADDR":      "127.0.0.1:9000",
		"NOWFILE_FINGER_ADDR":    ":79",
		"NOWFILE_DB_PATH":        "/tmp/x.db",
		"NOWFILE_BASE_URL":       "https://nowfile.org/",
		"NOWFILE_POSTMARK_TOKEN": "pm-secret",
		"NOWFILE_MAIL_FROM":      "hello@nowfile.org",
		"NOWFILE_ADMIN_TOKEN":    "admin-secret",
	}))
	if c.HTTPAddr != "127.0.0.1:9000" || c.FingerAddr != ":79" || c.DBPath != "/tmp/x.db" || c.BaseURL != "https://nowfile.org" ||
		c.PostmarkToken != "pm-secret" || c.MailFrom != "hello@nowfile.org" || c.AdminToken != "admin-secret" {
		t.Errorf("overrides: %+v", c)
	}
	s := c.String()
	if strings.Contains(s, "secret") || !strings.Contains(s, "postmark_token=set") {
		t.Errorf("String leaks or omits secrets: %s", s)
	}
}
