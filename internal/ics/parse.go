// Package ics reads iCalendar (RFC 5545) feeds and computes busy time from
// them. It is pure: no network, no clock, no storage. It reads only what busy
// time needs (start, end, duration, recurrence, transparency, status, and
// time zones) and never parses an event's subject, description, location, or
// attendees, so nothing it returns can carry them.
package ics

import (
	"bytes"
	"errors"
	"strings"
)

// ErrNotCalendar is returned for input that is not an iCalendar document.
var ErrNotCalendar = errors.New("not an iCalendar document")

// property is one content line: NAME;PARAM=VALUE:value.
type property struct {
	name   string            // uppercased
	params map[string]string // keys uppercased, values unquoted
	value  string
}

// component is a BEGIN/END block and what it contains.
type component struct {
	name  string // uppercased
	props []property
	subs  []*component
}

// get returns the first property called name, or nil.
func (c *component) get(name string) *property {
	for i := range c.props {
		if c.props[i].name == name {
			return &c.props[i]
		}
	}
	return nil
}

// all returns every property called name.
func (c *component) all(name string) []*property {
	var out []*property
	for i := range c.props {
		if c.props[i].name == name {
			out = append(out, &c.props[i])
		}
	}
	return out
}

// kept is every property busy time needs. Everything else, SUMMARY,
// DESCRIPTION, LOCATION, ATTENDEE, and the rest, is dropped as it is read.
var kept = map[string]bool{
	"DTSTART": true, "DTEND": true, "DURATION": true,
	"RRULE": true, "EXDATE": true, "RDATE": true, "RECURRENCE-ID": true, "UID": true,
	"TRANSP": true, "STATUS": true, "X-MICROSOFT-CDO-BUSYSTATUS": true,
	"TZID": true, "TZOFFSETFROM": true, "TZOFFSETTO": true,
}

// parse reads the document into a tree and returns the first VCALENDAR.
// Content lines it cannot read are skipped; only a document with no
// VCALENDAR at all is an error.
func parse(data []byte) (*component, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	root := &component{}
	stack := []*component{root}
	for _, line := range unfold(data) {
		p, ok := parseLine(line)
		if !ok {
			continue
		}
		top := stack[len(stack)-1]
		switch p.name {
		case "BEGIN":
			c := &component{name: strings.ToUpper(strings.TrimSpace(p.value))}
			top.subs = append(top.subs, c)
			stack = append(stack, c)
		case "END":
			// Pop to the matching BEGIN; a stray END is ignored.
			name := strings.ToUpper(strings.TrimSpace(p.value))
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].name == name {
					stack = stack[:i]
					break
				}
			}
		default:
			if kept[p.name] {
				top.props = append(top.props, p)
			}
		}
	}
	for _, c := range root.subs {
		if c.name == "VCALENDAR" {
			return c, nil
		}
	}
	return nil, ErrNotCalendar
}

// unfold splits data into logical content lines, joining folded
// continuation lines (those starting with a space or tab) and accepting LF
// as well as CRLF line ends.
func unfold(data []byte) []string {
	raw := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var out []string
	for _, l := range raw {
		l = strings.TrimSuffix(l, "\r")
		if len(l) > 0 && (l[0] == ' ' || l[0] == '\t') && len(out) > 0 {
			out[len(out)-1] += l[1:]
			continue
		}
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// parseLine splits a content line into name, parameters, and value.
// Parameter values may be double-quoted and contain ';', ':', and ','.
func parseLine(line string) (property, bool) {
	p := property{params: map[string]string{}}
	i := strings.IndexAny(line, ";:")
	if i <= 0 {
		return p, false
	}
	p.name = strings.ToUpper(line[:i])
	rest := line[i:]
	for len(rest) > 0 && rest[0] == ';' {
		rest = rest[1:]
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			return p, false
		}
		key := strings.ToUpper(rest[:eq])
		rest = rest[eq+1:]
		var val strings.Builder
		for len(rest) > 0 {
			if rest[0] == '"' {
				end := strings.IndexByte(rest[1:], '"')
				if end < 0 {
					return p, false
				}
				val.WriteString(rest[1 : end+1])
				rest = rest[end+2:]
				continue
			}
			if rest[0] == ';' || rest[0] == ':' {
				break
			}
			val.WriteByte(rest[0])
			rest = rest[1:]
		}
		p.params[key] = val.String()
	}
	if len(rest) == 0 || rest[0] != ':' {
		return p, false
	}
	p.value = rest[1:]
	return p, true
}
