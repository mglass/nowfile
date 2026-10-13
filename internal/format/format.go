// Package format implements the nowfile document format described in
// docs/format.md (draft 1): parsing, composing, rendering, and validating the
// plain-text document that finger, HTTP text, JSON, and HTML all derive from.
package format

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// CurrentVersion is the format version this package writes. Render emits it
// as the first line, "nowfile/" + CurrentVersion.
const CurrentVersion = "0"

// versionPrefix is the start of the version line.
const versionPrefix = "nowfile/"

// MaxLineBytes is the longest line, in bytes, that Validate accepts.
const MaxLineBytes = 200

// MaxLinks is the most Link fields a document may carry.
const MaxLinks = 5

// ErrInvalid is wrapped by every error Validate returns, so callers can map
// validation failures to a single response (for example HTTP 422).
var ErrInvalid = errors.New("invalid document")

// Document is one user's nowfile document.
//
// A classic plan (no nowfile/ version line) has Version "" and its whole text
// in Plan; the other fields are empty.
type Document struct {
	// Version is the format version without the "nowfile/" prefix, "0" for
	// draft 1, or "" for a classic plan.
	Version  string
	Name     string
	Headline string
	Photo    string
	Links    []string
	// Slots holds the dynamic fields in document order. Fields with keys this
	// package does not know are kept here too, under their original key, so a
	// document round-trips; renderers that only want known slots should check
	// IsSlotKey.
	Slots []Slot
	// Plan is the free text after the "Plan:" line, without its trailing
	// newline.
	Plan string
}

// Slot is one dynamic field and its metadata.
type Slot struct {
	Key   string
	Value string
	// Set is when the value was written. Zero means unknown.
	Set time.Time
	// Until is when the value expires. Zero means it does not.
	Until time.Time
	// Source names the writer, for example "calendar" or "lastfm". Empty
	// means unspecified.
	Source string
}

// Expired reports whether the slot's Until is at or before now.
func (s Slot) Expired(now time.Time) bool {
	return !s.Until.IsZero() && !now.Before(s.Until)
}

// KnownSlotKeys lists the dynamic field keys from docs/format.md in canonical
// casing and canonical order.
var KnownSlotKeys = []string{"Status", "Calendar", "Focus", "Listening", "Working", "Reading", "Watching", "Playing"}

// headlineOrder is the precedence for Headline among slots.
var headlineOrder = []string{"Status", "Calendar", "Focus", "Listening"}

// staticKeys are the static fields in canonical casing.
var staticKeys = []string{"Name", "Headline", "Photo", "Link"}

// nonSlotKeys may never be used as a slot key.
var nonSlotKeys = []string{"Name", "Headline", "Photo", "Link", "Plan"}

// IsSlotKey reports whether key, compared case-insensitively, is one of
// KnownSlotKeys.
func IsSlotKey(key string) bool {
	_, ok := CanonicalSlotKey(key)
	return ok
}

// CanonicalSlotKey returns the canonical casing of a known slot key, compared
// case-insensitively, and whether it is known.
func CanonicalSlotKey(key string) (string, bool) {
	for _, k := range KnownSlotKeys {
		if strings.EqualFold(k, key) {
			return k, true
		}
	}
	return "", false
}

// Compose returns doc with every slot whose Until is at or before now
// removed. It does not modify doc. Every server path that emits a document
// must pass it through Compose first; that is how expired slots are never
// served.
func Compose(doc Document, now time.Time) Document {
	out := doc
	out.Links = append([]string(nil), doc.Links...)
	out.Slots = nil
	for _, s := range doc.Slots {
		if s.Expired(now) {
			continue
		}
		out.Slots = append(out.Slots, s)
	}
	return out
}

// Headline returns the single line shown where a renderer has room for one:
// the first present of Status, Calendar, Focus, Listening, then the static
// Headline. It does not check expiry; callers pass a composed document.
func Headline(doc Document) string {
	for _, key := range headlineOrder {
		for _, s := range doc.Slots {
			if s.Key == key && s.Value != "" {
				return s.Value
			}
		}
	}
	return doc.Headline
}

// Render returns the document as text. The short form (long false) omits the
// Plan block; the long form is the whole document. Metadata lines are
// indented two spaces and timestamps are RFC 3339 in the zone they carry.
// Render does not drop expired slots; compose first. A classic plan
// (Version "") renders as its raw text.
func Render(doc Document, long bool) string {
	if doc.Version == "" {
		if doc.Plan == "" || strings.HasSuffix(doc.Plan, "\n") {
			return doc.Plan
		}
		return doc.Plan + "\n"
	}

	var b strings.Builder
	b.WriteString(versionPrefix + doc.Version + "\n")
	field := func(k, v string) {
		if v != "" {
			b.WriteString(k + ": " + v + "\n")
		}
	}
	field("Name", doc.Name)
	field("Headline", doc.Headline)
	field("Photo", doc.Photo)
	for _, l := range doc.Links {
		field("Link", l)
	}

	if len(doc.Slots) > 0 {
		b.WriteString("\n")
		for _, s := range doc.Slots {
			b.WriteString(s.Key + ": " + s.Value + "\n")
			if !s.Set.IsZero() {
				b.WriteString("  set: " + s.Set.Format(time.RFC3339) + "\n")
			}
			if !s.Until.IsZero() {
				b.WriteString("  until: " + s.Until.Format(time.RFC3339) + "\n")
			}
			if s.Source != "" {
				b.WriteString("  source: " + s.Source + "\n")
			}
		}
	}

	if long && doc.Plan != "" {
		b.WriteString("\nPlan:\n" + doc.Plan + "\n")
	}
	return b.String()
}

// Parse reads a document following the client rules in docs/format.md.
//
// Everything before the first "nowfile/" version line is treated as a
// wrapper (classic fingerd's Login, Directory, Shell, and Plan lines) and
// ignored. Input with no version line is a classic plan: Version is "" and
// Plan is the whole input with CRLF normalised to LF. Keys are matched
// case-insensitively and stored in canonical casing; unknown keys are kept in
// Slots. If a non-Link field repeats, the first occurrence wins. Lines that
// are not fields or metadata are skipped, as are metadata lines that do not
// parse. Parse is lenient by design; Validate is where strictness lives.
func Parse(text string) (Document, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	start := -1
	var version string
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(t, versionPrefix); ok && v != "" && !strings.ContainsAny(v, " \t") {
			start, version = i, v
			break
		}
	}
	if start < 0 {
		return Document{Plan: strings.TrimRight(text, "\n")}, nil
	}

	doc := Document{Version: version}
	seen := map[string]bool{}
	var cur *Slot // slot that metadata lines attach to, nil if none
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if cur != nil {
				parseMeta(cur, strings.TrimSpace(line))
			}
			continue
		}
		key, value, ok := splitField(line)
		if !ok {
			cur = nil
			continue
		}
		if strings.EqualFold(key, "Plan") {
			plan := lines[i+1:]
			if value != "" {
				plan = append([]string{value}, plan...)
			}
			doc.Plan = strings.TrimRight(strings.Join(plan, "\n"), "\n")
			break
		}
		cur = nil
		if strings.EqualFold(key, "Link") {
			doc.Links = append(doc.Links, value)
			continue
		}
		canon := canonicalKey(key)
		lk := strings.ToLower(canon)
		if seen[lk] {
			continue
		}
		seen[lk] = true
		switch canon {
		case "Name":
			doc.Name = value
		case "Headline":
			doc.Headline = value
		case "Photo":
			doc.Photo = value
		default:
			doc.Slots = append(doc.Slots, Slot{Key: canon, Value: value})
			cur = &doc.Slots[len(doc.Slots)-1]
		}
	}
	return doc, nil
}

// canonicalKey returns the canonical casing for a known static or slot key,
// or key unchanged if unknown.
func canonicalKey(key string) string {
	if k, ok := CanonicalSlotKey(key); ok {
		return k
	}
	for _, k := range staticKeys {
		if strings.EqualFold(k, key) {
			return k
		}
	}
	return key
}

// splitField splits "Key: value" at column 0. The key must be non-empty
// printable ASCII without spaces or colons.
func splitField(line string) (key, value string, ok bool) {
	key, value, ok = strings.Cut(line, ":")
	if !ok || !validKey(key) {
		return "", "", false
	}
	return key, strings.TrimSpace(value), true
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c <= ' ' || c >= 0x7f || c == ':' {
			return false
		}
	}
	return true
}

// parseMeta applies one "key: value" metadata line to s. Unknown metadata
// and unparseable timestamps are ignored.
func parseMeta(s *Slot, line string) {
	k, v, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	v = strings.TrimSpace(v)
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "set":
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			s.Set = t
		}
	case "until":
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			s.Until = t
		}
	case "source":
		s.Source = v
	}
}

// Validate reports whether doc is one a server may store and emit: every
// rendered line at most MaxLineBytes, at most MaxLinks links, each other
// field at most once, slot keys well-formed, single-line values, and Source
// a single token. A classic plan (Version "") is checked only for line
// length. Every returned error wraps ErrInvalid.
func Validate(doc Document) error {
	if doc.Version != "" {
		if len(doc.Links) > MaxLinks {
			return invalid("at most %d links, got %d", MaxLinks, len(doc.Links))
		}
		for _, f := range []struct{ k, v string }{{"Name", doc.Name}, {"Headline", doc.Headline}, {"Photo", doc.Photo}} {
			if err := checkValue(f.k, f.v); err != nil {
				return err
			}
		}
		for _, l := range doc.Links {
			if l == "" {
				return invalid("empty Link")
			}
			if err := checkValue("Link", l); err != nil {
				return err
			}
		}
		seen := map[string]bool{}
		for _, s := range doc.Slots {
			if !validKey(s.Key) {
				return invalid("bad key %q", s.Key)
			}
			lk := strings.ToLower(s.Key)
			for _, k := range nonSlotKeys {
				if strings.EqualFold(k, s.Key) {
					return invalid("%s cannot be a slot", s.Key)
				}
			}
			if seen[lk] {
				return invalid("%s appears more than once", s.Key)
			}
			seen[lk] = true
			if s.Value == "" {
				return invalid("%s is empty", s.Key)
			}
			if err := checkValue(s.Key, s.Value); err != nil {
				return err
			}
			if strings.ContainsAny(s.Source, " \t\r\n") {
				return invalid("%s source must be a single token", s.Key)
			}
		}
	}
	for i, line := range strings.Split(Render(doc, true), "\n") {
		if len(line) > MaxLineBytes {
			return invalid("line %d is %d bytes, limit %d", i+1, len(line), MaxLineBytes)
		}
	}
	return nil
}

// checkValue rejects values that would break the line structure.
func checkValue(key, v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return invalid("%s must be one line", key)
	}
	if v != strings.TrimSpace(v) {
		return invalid("%s has leading or trailing space", key)
	}
	return nil
}

func invalid(f string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(f, args...))
}
