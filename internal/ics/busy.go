package ics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Span is a stretch of busy time, from Start up to but not including End.
type Span struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// maxInstances bounds the occurrences one document may expand to.
const maxInstances = 200000

// Busy returns the busy time in data that overlaps [from, to), merged into
// non-overlapping spans in order. Floating times (no zone) are read in
// floating. All-day events, events with TRANSP:TRANSPARENT or
// X-MICROSOFT-CDO-BUSYSTATUS:FREE, cancelled events, and events with no
// duration do not count. Recurring events expand per RRULE, RDATE, EXDATE,
// and RECURRENCE-ID overrides.
//
// Events Busy cannot read or expand are skipped and described in warnings,
// which name the event by its position in the document and never by
// content. The only error is ErrNotCalendar.
func Busy(data []byte, floating *time.Location, from, to time.Time) (spans []Span, warnings []string, err error) {
	cal, err := parse(data)
	if err != nil {
		return nil, nil, err
	}
	if floating == nil {
		floating = time.UTC
	}
	zs := newZones(cal, floating)
	var events []*component
	for _, c := range cal.subs {
		if c.name == "VEVENT" {
			events = append(events, c)
		}
	}
	warn := func(i int, f string, args ...any) {
		warnings = append(warnings, fmt.Sprintf("event %d: ", i+1)+fmt.Sprintf(f, args...))
	}

	// Overrides first: an instance moved or cancelled by a RECURRENCE-ID
	// event is dropped from its master's expansion.
	overridden := map[string]map[int64]bool{}
	for i, ev := range events {
		rid := ev.get("RECURRENCE-ID")
		if rid == nil {
			continue
		}
		d, err := zs.parseProp(rid)
		if err != nil {
			warn(i, "RECURRENCE-ID: %v", err)
			continue
		}
		uid := ""
		if u := ev.get("UID"); u != nil {
			uid = u.value
		}
		if overridden[uid] == nil {
			overridden[uid] = map[int64]bool{}
		}
		overridden[uid][d.instant().Unix()] = true
	}

	budget := maxInstances
	for i, ev := range events {
		if budget <= 0 {
			warn(i, "too many occurrences in this calendar; the rest are skipped")
			break
		}
		if s := ev.get("STATUS"); s != nil && strings.EqualFold(strings.TrimSpace(s.value), "CANCELLED") {
			continue
		}
		if t := ev.get("TRANSP"); t != nil && strings.EqualFold(strings.TrimSpace(t.value), "TRANSPARENT") {
			continue
		}
		if b := ev.get("X-MICROSOFT-CDO-BUSYSTATUS"); b != nil && strings.EqualFold(strings.TrimSpace(b.value), "FREE") {
			continue
		}
		dsp := ev.get("DTSTART")
		if dsp == nil {
			warn(i, "no DTSTART")
			continue
		}
		start, err := zs.parseProp(dsp)
		if err != nil {
			warn(i, "DTSTART: %v", err)
			continue
		}
		if start.isDate {
			continue // all-day
		}
		end, err := endFunc(ev, zs, start)
		if err != nil {
			warn(i, "%v", err)
			continue
		}
		if end == nil {
			continue // no duration, no busy time
		}

		var exInst = map[int64]bool{}
		var exDate = map[time.Time]bool{}
		for _, p := range ev.all("EXDATE") {
			ds, err := zs.parseList(p)
			if err != nil {
				warn(i, "EXDATE: %v", err)
				continue
			}
			for _, d := range ds {
				if d.isDate {
					exDate[d.civil] = true
				} else {
					exInst[d.instant().Unix()] = true
				}
			}
		}
		if ev.get("RECURRENCE-ID") == nil {
			uid := ""
			if u := ev.get("UID"); u != nil {
				uid = u.value
			}
			for k := range overridden[uid] {
				exInst[k] = true
			}
		}
		add := func(c, inst time.Time) {
			budget--
			if exInst[inst.Unix()] || exDate[c.Truncate(24*time.Hour)] {
				return
			}
			e := end(c, inst)
			if e.After(inst) && e.After(from) && inst.Before(to) {
				spans = append(spans, Span{inst.UTC(), e.UTC()})
			}
		}

		rp := ev.get("RRULE")
		if rp == nil || ev.get("RECURRENCE-ID") != nil {
			add(start.civil, start.instant())
		} else {
			rule, err := parseRRule(rp.value)
			if err == nil {
				err = rule.eventSupported()
			}
			if err != nil {
				warn(i, "%v", err)
				continue
			}
			first := start.instant()
			longest := end(start.civil, first).Sub(first)
			limit := to.UTC().Add(48 * time.Hour)
			skipTo := from.UTC().Add(-longest - 48*time.Hour)
			n := 0
			handle := func(c time.Time) bool {
				inst := start.zone.instant(c)
				if rule.until.after(c, inst) {
					return false
				}
				n++
				if rule.count > 0 && n > rule.count {
					return false
				}
				if !inst.Before(to) || budget <= 0 {
					return false
				}
				add(c, inst)
				return true
			}
			if handle(start.civil) {
				rule.starts(start.civil, limit, skipTo, rule.count == 0, handle)
			}
		}
		for _, p := range ev.all("RDATE") {
			if strings.EqualFold(p.params["VALUE"], "PERIOD") {
				warn(i, "RDATE periods are not supported")
				continue
			}
			ds, err := zs.parseList(p)
			if err != nil {
				warn(i, "RDATE: %v", err)
				continue
			}
			for _, d := range ds {
				if !d.isDate {
					add(d.civil, d.instant())
				}
			}
		}
	}
	return Merge(spans), warnings, nil
}

// endFunc returns how to find an occurrence's end from its civil start and
// instant, or nil if the event has no duration.
func endFunc(ev *component, zs *zones, start dateTime) (func(c, inst time.Time) time.Time, error) {
	if p := ev.get("DTEND"); p != nil {
		e, err := zs.parseProp(p)
		if err != nil {
			return nil, fmt.Errorf("DTEND: %v", err)
		}
		if e.isDate {
			return nil, fmt.Errorf("DTEND is a date but DTSTART is a date-time")
		}
		d := e.instant().Sub(start.instant())
		if d <= 0 {
			return nil, nil
		}
		return func(_, inst time.Time) time.Time { return inst.Add(d) }, nil
	}
	if p := ev.get("DURATION"); p != nil {
		days, d, err := parseDuration(p.value)
		if err != nil {
			return nil, err
		}
		if days == 0 && d == 0 {
			return nil, nil
		}
		// Days are nominal: an event lasting P1D ends at the same wall time
		// the next day even across a DST change.
		return func(c, inst time.Time) time.Time {
			if days == 0 {
				return inst.Add(d)
			}
			return start.zone.instant(c.AddDate(0, 0, days)).Add(d)
		}, nil
	}
	return nil, nil
}

// parseDuration reads a positive RFC 5545 duration such as PT1H30M, P1D,
// or P2W into nominal days and an exact remainder.
func parseDuration(v string) (int, time.Duration, error) {
	s := strings.ToUpper(strings.TrimSpace(v))
	s = strings.TrimPrefix(s, "+")
	if !strings.HasPrefix(s, "P") {
		return 0, 0, fmt.Errorf("bad DURATION %q", v)
	}
	s = s[1:]
	days := 0
	var d time.Duration
	inTime := false
	num := ""
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			num += string(r)
			continue
		case r == 'T':
			inTime = true
			continue
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			return 0, 0, fmt.Errorf("bad DURATION %q", v)
		}
		num = ""
		switch {
		case r == 'W' && !inTime:
			days += 7 * n
		case r == 'D' && !inTime:
			days += n
		case r == 'H' && inTime:
			d += time.Duration(n) * time.Hour
		case r == 'M' && inTime:
			d += time.Duration(n) * time.Minute
		case r == 'S' && inTime:
			d += time.Duration(n) * time.Second
		default:
			return 0, 0, fmt.Errorf("bad DURATION %q", v)
		}
	}
	if num != "" {
		return 0, 0, fmt.Errorf("bad DURATION %q", v)
	}
	return days, d, nil
}

// Merge sorts spans and joins any that overlap or touch, so back-to-back
// meetings read as one busy stretch. It does not modify its argument.
func Merge(spans []Span) []Span {
	s := append([]Span(nil), spans...)
	sort.Slice(s, func(i, j int) bool { return s[i].Start.Before(s[j].Start) })
	var out []Span
	for _, sp := range s {
		if n := len(out); n > 0 && !sp.Start.After(out[n-1].End) {
			if sp.End.After(out[n-1].End) {
				out[n-1].End = sp.End
			}
			continue
		}
		out = append(out, sp)
	}
	return out
}

// Current returns the span containing now, if any. spans must be merged.
func Current(spans []Span, now time.Time) (Span, bool) {
	for _, s := range spans {
		if !now.Before(s.Start) && now.Before(s.End) {
			return s, true
		}
	}
	return Span{}, false
}

// Next returns the first span starting after now, if any. spans must be
// merged.
func Next(spans []Span, now time.Time) (Span, bool) {
	for _, s := range spans {
		if s.Start.After(now) {
			return s, true
		}
	}
	return Span{}, false
}
