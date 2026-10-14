package ics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Wall-clock ("civil") times are carried as time.Time values in UTC whose
// fields are the local date and time, so calendar arithmetic (AddDate) never
// crosses a DST change. A zone turns them into instants.

// zone converts a civil time in some time zone to an instant.
type zone interface {
	instant(civil time.Time) time.Time
}

// locZone is a zone backed by the Go time zone database.
type locZone struct{ loc *time.Location }

func (z locZone) instant(c time.Time) time.Time {
	return time.Date(c.Year(), c.Month(), c.Day(), c.Hour(), c.Minute(), c.Second(), 0, z.loc)
}

var utcZone = locZone{time.UTC}

// loadLocation is time.LoadLocation without the names that mean "the
// server's zone".
func loadLocation(name string) (*time.Location, bool) {
	if name == "" || name == "Local" || strings.Contains(name, "..") {
		return nil, false
	}
	loc, err := time.LoadLocation(name)
	return loc, err == nil
}

// zones resolves TZID parameters against the Go database first and the
// document's own VTIMEZONE definitions second, so IANA names use current
// rules and Outlook-style names ("Eastern Standard Time") still work.
type zones struct {
	defined  map[string]*vtzone
	floating zone
	cache    map[string]zone
}

func newZones(cal *component, floating *time.Location) *zones {
	z := &zones{defined: map[string]*vtzone{}, floating: locZone{floating}, cache: map[string]zone{}}
	for _, c := range cal.subs {
		if c.name != "VTIMEZONE" {
			continue
		}
		if id := c.get("TZID"); id != nil {
			if vz, err := parseVTimezone(c); err == nil {
				z.defined[id.value] = vz
			}
		}
	}
	return z
}

// lookup returns the zone for a TZID, or false if neither source knows it.
func (z *zones) lookup(tzid string) (zone, bool) {
	if zn, ok := z.cache[tzid]; ok {
		return zn, true
	}
	var zn zone
	if loc, ok := loadLocation(strings.TrimPrefix(tzid, "/")); ok {
		zn = locZone{loc}
	} else if vz, ok := z.defined[tzid]; ok {
		zn = vz
	} else {
		return nil, false
	}
	z.cache[tzid] = zn
	return zn, true
}

// dateTime is a parsed DATE or DATE-TIME value.
type dateTime struct {
	civil  time.Time
	zone   zone
	isDate bool
	isUTC  bool
}

func (d dateTime) instant() time.Time { return d.zone.instant(d.civil) }

// parseValue parses one DATE or DATE-TIME value. tzid is the TZID parameter,
// if any.
func (z *zones) parseValue(v, tzid string, forceDate bool) (dateTime, error) {
	v = strings.TrimSpace(v)
	if forceDate || len(v) == 8 {
		t, err := time.Parse("20060102", v)
		if err != nil {
			return dateTime{}, fmt.Errorf("bad date %q", v)
		}
		return dateTime{civil: t, zone: z.floating, isDate: true}, nil
	}
	if strings.HasSuffix(v, "Z") {
		t, err := time.Parse("20060102T150405", strings.TrimSuffix(v, "Z"))
		if err != nil {
			return dateTime{}, fmt.Errorf("bad date-time %q", v)
		}
		return dateTime{civil: t, zone: utcZone, isUTC: true}, nil
	}
	t, err := time.Parse("20060102T150405", v)
	if err != nil {
		return dateTime{}, fmt.Errorf("bad date-time %q", v)
	}
	if tzid == "" {
		return dateTime{civil: t, zone: z.floating}, nil
	}
	zn, ok := z.lookup(tzid)
	if !ok {
		return dateTime{}, fmt.Errorf("unknown TZID %q", tzid)
	}
	return dateTime{civil: t, zone: zn}, nil
}

// parseProp parses a property's single value.
func (z *zones) parseProp(p *property) (dateTime, error) {
	return z.parseValue(p.value, p.params["TZID"], strings.EqualFold(p.params["VALUE"], "DATE"))
}

// parseList parses a property whose value is a comma-separated list
// (EXDATE, RDATE).
func (z *zones) parseList(p *property) ([]dateTime, error) {
	var out []dateTime
	for _, v := range strings.Split(p.value, ",") {
		if strings.TrimSpace(v) == "" {
			continue
		}
		d, err := z.parseValue(v, p.params["TZID"], strings.EqualFold(p.params["VALUE"], "DATE"))
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// --- VTIMEZONE ---

// vtzone is a time zone defined in the document by STANDARD and DAYLIGHT
// observances.
type vtzone struct {
	obs   []observance
	years map[int][]transition
}

type observance struct {
	start    time.Time // civil onset, in the offset in force before it
	from, to int       // offsets in seconds east of UTC
	rule     *rrule    // yearly, or nil
	rdates   []time.Time
}

type transition struct {
	at       time.Time // instant
	from, to int
}

func parseVTimezone(c *component) (*vtzone, error) {
	vz := &vtzone{years: map[int][]transition{}}
	for _, sub := range c.subs {
		if sub.name != "STANDARD" && sub.name != "DAYLIGHT" {
			continue
		}
		var o observance
		var err error
		ds, fp, tp := sub.get("DTSTART"), sub.get("TZOFFSETFROM"), sub.get("TZOFFSETTO")
		if ds == nil || fp == nil || tp == nil {
			return nil, fmt.Errorf("observance missing DTSTART or offsets")
		}
		if o.start, err = time.Parse("20060102T150405", strings.TrimSpace(ds.value)); err != nil {
			return nil, err
		}
		if o.from, err = parseOffset(fp.value); err != nil {
			return nil, err
		}
		if o.to, err = parseOffset(tp.value); err != nil {
			return nil, err
		}
		if rp := sub.get("RRULE"); rp != nil {
			if o.rule, err = parseRRule(rp.value); err != nil {
				return nil, err
			}
			if o.rule.freq != "YEARLY" {
				return nil, fmt.Errorf("observance rule is not yearly")
			}
		}
		for _, rp := range sub.all("RDATE") {
			for _, v := range strings.Split(rp.value, ",") {
				if t, err := time.Parse("20060102T150405", strings.TrimSpace(v)); err == nil {
					o.rdates = append(o.rdates, t)
				}
			}
		}
		vz.obs = append(vz.obs, o)
	}
	if len(vz.obs) == 0 {
		return nil, fmt.Errorf("no observances")
	}
	return vz, nil
}

// parseOffset reads +HHMM, -HHMM, or +HHMMSS as seconds east of UTC.
func parseOffset(v string) (int, error) {
	v = strings.TrimSpace(v)
	if len(v) != 5 && len(v) != 7 || (v[0] != '+' && v[0] != '-') {
		return 0, fmt.Errorf("bad offset %q", v)
	}
	n := func(s string) int { x, _ := strconv.Atoi(s); return x }
	if _, err := strconv.Atoi(v[1:]); err != nil {
		return 0, fmt.Errorf("bad offset %q", v)
	}
	secs := n(v[1:3])*3600 + n(v[3:5])*60
	if len(v) == 7 {
		secs += n(v[5:7])
	}
	if v[0] == '-' {
		secs = -secs
	}
	return secs, nil
}

// onsets returns the observance's civil onsets in year.
func (o observance) onsets(year int) []time.Time {
	var out []time.Time
	if o.start.Year() == year {
		out = append(out, o.start)
	}
	for _, r := range o.rdates {
		if r.Year() == year {
			out = append(out, r)
		}
	}
	if o.rule == nil || year < o.start.Year() {
		return out
	}
	r := o.rule
	month := o.start.Month()
	if len(r.bymonth) > 0 {
		month = time.Month(r.bymonth[0])
	}
	var days []int
	switch {
	case len(r.byday) > 0:
		for _, wd := range r.byday {
			if d, ok := nthWeekday(year, month, wd.n, wd.day); ok {
				days = append(days, d)
			}
		}
	case len(r.bymonthday) > 0:
		for _, md := range r.bymonthday {
			if d, ok := monthDay(year, month, md); ok {
				days = append(days, d)
			}
		}
	default:
		days = []int{o.start.Day()}
	}
	for _, d := range days {
		t := time.Date(year, month, d, o.start.Hour(), o.start.Minute(), o.start.Second(), 0, time.UTC)
		if t.Before(o.start) || t.Equal(o.start) {
			continue
		}
		if r.until.set && r.until.after(t, t.Add(-time.Duration(o.from)*time.Second)) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// transitions returns the zone's transitions in year, sorted.
func (vz *vtzone) transitions(year int) []transition {
	if ts, ok := vz.years[year]; ok {
		return ts
	}
	var ts []transition
	for _, o := range vz.obs {
		for _, c := range o.onsets(year) {
			ts = append(ts, transition{at: c.Add(-time.Duration(o.from) * time.Second), from: o.from, to: o.to})
		}
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].at.Before(ts[j].at) })
	vz.years[year] = ts
	return ts
}

// offsetAt returns the offset in force at instant t.
func (vz *vtzone) offsetAt(t time.Time) int {
	var last *transition
	var first *transition
	for y := t.Year() - 1; y <= t.Year()+1; y++ {
		for _, tr := range vz.transitions(y) {
			tr := tr
			if first == nil {
				first = &tr
			}
			if !tr.at.After(t) {
				last = &tr
			}
		}
	}
	if last != nil {
		return last.to
	}
	if first != nil {
		return first.from
	}
	// No transitions near t: before any rule or after an UNTIL. Use the
	// observance whose onset is latest before t, else the earliest.
	best := vz.obs[0]
	for _, o := range vz.obs[1:] {
		if o.start.Before(t) && (best.start.After(t) || o.start.After(best.start)) {
			best = o
		}
	}
	return best.to
}

// instant converts a civil time. An ambiguous time (the repeated hour when
// clocks go back) takes the earlier instant, and a time in the skipped hour
// takes the offset in force before the change, as RFC 5545 says.
func (vz *vtzone) instant(c time.Time) time.Time {
	seen := map[int]bool{}
	var offs []int
	for y := c.Year() - 1; y <= c.Year()+1; y++ {
		for _, tr := range vz.transitions(y) {
			for _, o := range []int{tr.from, tr.to} {
				if !seen[o] {
					seen[o] = true
					offs = append(offs, o)
				}
			}
		}
	}
	if len(offs) == 0 {
		off := vz.offsetAt(c)
		return c.Add(-time.Duration(off) * time.Second)
	}
	// Larger offsets give earlier instants.
	sort.Sort(sort.Reverse(sort.IntSlice(offs)))
	for _, off := range offs {
		u := c.Add(-time.Duration(off) * time.Second)
		if vz.offsetAt(u) == off {
			return u
		}
	}
	off := vz.offsetAt(c.Add(-24 * time.Hour))
	return c.Add(-time.Duration(off) * time.Second)
}

// --- calendar helpers ---

// nthWeekday returns the day of month of the nth wd in the month, counting
// from the end when n is negative. n must not be 0.
func nthWeekday(year int, month time.Month, n int, wd time.Weekday) (int, bool) {
	last := daysIn(year, month)
	if n > 0 {
		first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).Weekday()
		d := 1 + (int(wd)-int(first)+7)%7 + (n-1)*7
		return d, d <= last
	}
	lastWd := time.Date(year, month, last, 0, 0, 0, 0, time.UTC).Weekday()
	d := last - (int(lastWd)-int(wd)+7)%7 + (n+1)*7
	return d, d >= 1
}

// monthDay resolves a BYMONTHDAY value, negative counting from the end.
func monthDay(year int, month time.Month, md int) (int, bool) {
	last := daysIn(year, month)
	if md < 0 {
		md = last + 1 + md
	}
	return md, md >= 1 && md <= last
}

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
