package ics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rrule is the subset of RFC 5545 recurrence rules nowfile expands: DAILY
// and WEEKLY fully (INTERVAL, COUNT, UNTIL, BYDAY, BYMONTH, BYMONTHDAY,
// WKST), MONTHLY with BYDAY (with or without an ordinal) and BYMONTHDAY, and
// YEARLY with no BY parts. Anything else fails to parse and the event is
// skipped with a warning.
type rrule struct {
	freq       string
	interval   int
	count      int
	until      until
	byday      []weekdayNum
	bymonthday []int
	bymonth    []int
	wkst       time.Weekday
}

type weekdayNum struct {
	n   int // 0 means every such weekday in the period
	day time.Weekday
}

// until is an UNTIL bound: a UTC instant, a floating date-time, or a date.
type until struct {
	set     bool
	isUTC   bool
	isDate  bool
	civil   time.Time
	instant time.Time
}

// after reports whether an occurrence starting at civil time c, instant i,
// falls after the bound.
func (u until) after(c, i time.Time) bool {
	switch {
	case !u.set:
		return false
	case u.isUTC:
		return i.After(u.instant)
	case u.isDate:
		return c.Truncate(24 * time.Hour).After(u.civil)
	default:
		return c.After(u.civil)
	}
}

var weekdays = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

func parseRRule(v string) (*rrule, error) {
	r := &rrule{interval: 1, wkst: time.Monday}
	for _, part := range strings.Split(strings.TrimSpace(v), ";") {
		if part == "" {
			continue
		}
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("bad RRULE part %q", part)
		}
		k = strings.ToUpper(k)
		val = strings.ToUpper(val)
		var err error
		switch k {
		case "FREQ":
			r.freq = val
		case "INTERVAL":
			if r.interval, err = strconv.Atoi(val); err != nil || r.interval < 1 {
				return nil, fmt.Errorf("bad INTERVAL %q", val)
			}
		case "COUNT":
			if r.count, err = strconv.Atoi(val); err != nil || r.count < 1 {
				return nil, fmt.Errorf("bad COUNT %q", val)
			}
		case "UNTIL":
			if r.until, err = parseUntil(val); err != nil {
				return nil, err
			}
		case "BYDAY":
			for _, d := range strings.Split(val, ",") {
				wd, ok := weekdays[d[max(0, len(d)-2):]]
				if !ok || len(d) < 2 {
					return nil, fmt.Errorf("bad BYDAY %q", d)
				}
				n := 0
				if num := d[:len(d)-2]; num != "" {
					if n, err = strconv.Atoi(num); err != nil || n == 0 || n < -53 || n > 53 {
						return nil, fmt.Errorf("bad BYDAY %q", d)
					}
				}
				r.byday = append(r.byday, weekdayNum{n, wd})
			}
		case "BYMONTHDAY":
			for _, d := range strings.Split(val, ",") {
				n, err := strconv.Atoi(d)
				if err != nil || n == 0 || n < -31 || n > 31 {
					return nil, fmt.Errorf("bad BYMONTHDAY %q", d)
				}
				r.bymonthday = append(r.bymonthday, n)
			}
		case "BYMONTH":
			for _, d := range strings.Split(val, ",") {
				n, err := strconv.Atoi(d)
				if err != nil || n < 1 || n > 12 {
					return nil, fmt.Errorf("bad BYMONTH %q", d)
				}
				r.bymonth = append(r.bymonth, n)
			}
		case "WKST":
			wd, ok := weekdays[val]
			if !ok {
				return nil, fmt.Errorf("bad WKST %q", val)
			}
			r.wkst = wd
		default:
			return nil, fmt.Errorf("unsupported RRULE part %s", k)
		}
	}
	switch r.freq {
	case "DAILY", "WEEKLY":
		for _, wd := range r.byday {
			if wd.n != 0 {
				return nil, fmt.Errorf("BYDAY with an ordinal in a %s rule", r.freq)
			}
		}
	case "MONTHLY":
	case "YEARLY":
		// Observances in VTIMEZONE use BYMONTH and BYDAY; their onsets are
		// computed separately. An event's YEARLY rule must have no BY parts.
	case "":
		return nil, fmt.Errorf("RRULE without FREQ")
	default:
		return nil, fmt.Errorf("unsupported FREQ %s", r.freq)
	}
	return r, nil
}

func parseUntil(v string) (until, error) {
	switch {
	case len(v) == 8:
		t, err := time.Parse("20060102", v)
		return until{set: true, isDate: true, civil: t}, err
	case strings.HasSuffix(v, "Z"):
		t, err := time.Parse("20060102T150405Z", v)
		return until{set: true, isUTC: true, instant: t}, err
	default:
		t, err := time.Parse("20060102T150405", v)
		return until{set: true, civil: t}, err
	}
}

// eventSupported reports whether the rule can expand an event, as opposed
// to a VTIMEZONE observance.
func (r *rrule) eventSupported() error {
	if r.freq == "YEARLY" && (len(r.byday) > 0 || len(r.bymonthday) > 0 || len(r.bymonth) > 0) {
		return fmt.Errorf("unsupported YEARLY rule with BY parts")
	}
	return nil
}

// maxPeriods bounds the periods one rule may step through, so a rule whose
// filters never match cannot spin.
const maxPeriods = 20000

// starts calls yield with each civil start the rule generates after dtstart
// (dtstart itself is the caller's), in order, until yield returns false,
// the period passes limit, or maxPeriods is reached. When skip is true the
// caller does not need COUNT, so whole periods before skipTo are jumped.
func (r *rrule) starts(dtstart, limit, skipTo time.Time, skip bool, yield func(time.Time) bool) {
	tod := time.Duration(dtstart.Hour())*time.Hour + time.Duration(dtstart.Minute())*time.Minute + time.Duration(dtstart.Second())*time.Second
	day0 := dtstart.Truncate(24 * time.Hour)
	emit := func(days []time.Time) bool {
		sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
		for _, d := range days {
			c := d.Add(tod)
			if !c.After(dtstart) || !r.monthOK(c) {
				continue
			}
			if !yield(c) {
				return false
			}
		}
		return true
	}
	k0 := 0
	switch r.freq {
	case "DAILY":
		if skip {
			k0 = max(0, int(skipTo.Sub(day0).Hours()/24)/r.interval-1)
		}
		for k := k0; k < k0+maxPeriods; k++ {
			d := day0.AddDate(0, 0, k*r.interval)
			if d.After(limit) {
				return
			}
			if !r.dayOK(d) {
				continue
			}
			if !emit([]time.Time{d}) {
				return
			}
		}
	case "WEEKLY":
		ws := day0.AddDate(0, 0, -((int(day0.Weekday()) - int(r.wkst) + 7) % 7))
		if skip {
			k0 = max(0, int(skipTo.Sub(ws).Hours()/24/7)/r.interval-1)
		}
		days := r.byday
		if len(days) == 0 {
			days = []weekdayNum{{0, dtstart.Weekday()}}
		}
		for k := k0; k < k0+maxPeriods; k++ {
			w := ws.AddDate(0, 0, 7*k*r.interval)
			if w.After(limit) {
				return
			}
			var ds []time.Time
			for _, wd := range days {
				ds = append(ds, w.AddDate(0, 0, (int(wd.day)-int(r.wkst)+7)%7))
			}
			if !emit(ds) {
				return
			}
		}
	case "MONTHLY":
		m0 := time.Date(day0.Year(), day0.Month(), 1, 0, 0, 0, 0, time.UTC)
		if skip {
			months := (skipTo.Year()-m0.Year())*12 + int(skipTo.Month()) - int(m0.Month())
			k0 = max(0, months/r.interval-1)
		}
		for k := k0; k < k0+maxPeriods; k++ {
			m := m0.AddDate(0, k*r.interval, 0)
			if m.After(limit) {
				return
			}
			if !emit(r.monthDays(m, dtstart.Day())) {
				return
			}
		}
	case "YEARLY":
		if skip {
			k0 = max(0, (skipTo.Year()-day0.Year())/r.interval-1)
		}
		for k := k0; k < k0+maxPeriods; k++ {
			y := day0.Year() + k*r.interval
			d := time.Date(y, day0.Month(), day0.Day(), 0, 0, 0, 0, time.UTC)
			if d.After(limit) {
				return
			}
			if d.Day() != day0.Day() {
				continue // 29 February in a common year
			}
			if !emit([]time.Time{d}) {
				return
			}
		}
	}
}

// dayOK applies BYDAY and BYMONTHDAY as filters, for DAILY rules.
func (r *rrule) dayOK(d time.Time) bool {
	if len(r.byday) > 0 {
		ok := false
		for _, wd := range r.byday {
			ok = ok || d.Weekday() == wd.day
		}
		if !ok {
			return false
		}
	}
	if len(r.bymonthday) > 0 {
		ok := false
		for _, md := range r.bymonthday {
			if n, valid := monthDay(d.Year(), d.Month(), md); valid && n == d.Day() {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// monthOK applies BYMONTH as a filter.
func (r *rrule) monthOK(c time.Time) bool {
	if len(r.bymonth) == 0 {
		return true
	}
	for _, m := range r.bymonth {
		if int(c.Month()) == m {
			return true
		}
	}
	return false
}

// monthDays returns the days a MONTHLY rule picks in the month starting at
// m. BYDAY and BYMONTHDAY together pick days that match both.
func (r *rrule) monthDays(m time.Time, dtDay int) []time.Time {
	y, mo := m.Year(), m.Month()
	set := map[int]bool{}
	if len(r.byday) > 0 {
		for _, wd := range r.byday {
			if wd.n != 0 {
				if d, ok := nthWeekday(y, mo, wd.n, wd.day); ok {
					set[d] = true
				}
				continue
			}
			for d := 1; d <= daysIn(y, mo); d++ {
				if time.Date(y, mo, d, 0, 0, 0, 0, time.UTC).Weekday() == wd.day {
					set[d] = true
				}
			}
		}
	}
	if len(r.bymonthday) > 0 {
		md := map[int]bool{}
		for _, v := range r.bymonthday {
			if d, ok := monthDay(y, mo, v); ok {
				md[d] = true
			}
		}
		if len(r.byday) > 0 {
			for d := range set {
				if !md[d] {
					delete(set, d)
				}
			}
		} else {
			set = md
		}
	}
	if len(r.byday) == 0 && len(r.bymonthday) == 0 {
		if dtDay <= daysIn(y, mo) {
			set[dtDay] = true
		}
	}
	var out []time.Time
	for d := range set {
		out = append(out, time.Date(y, mo, d, 0, 0, 0, 0, time.UTC))
	}
	return out
}
