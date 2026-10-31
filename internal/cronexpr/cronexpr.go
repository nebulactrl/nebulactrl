// Package cronexpr is an in-house parser for standard 5-field cron expressions
// (minute hour day-of-month month day-of-week) and the usual @macros, plus a
// next-run calculator that is timezone and DST aware.
//
// Supported syntax per field: "*", "?" (same as "*"), single values, ranges
// "a-b", lists "a,b,c", steps "*/n", "a-b/n" and "a/n" (a through the field
// maximum). Month and weekday names (JAN-DEC, SUN-SAT, case-insensitive) work
// in values and ranges. Day-of-week accepts 0-7 where both 0 and 7 are Sunday.
//
// Macros: @yearly, @annually, @monthly, @weekly, @daily, @midnight, @hourly.
//
// Day-of-month and day-of-week follow classic cron semantics: when both are
// restricted a time matches if EITHER matches; when one is "*" only the other
// applies.
//
// The package is deliberately small and dependency-free. Backup schedules and
// cron jobs both use it.
package cronexpr

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression. It is immutable and safe for
// concurrent use.
type Schedule struct {
	minute uint64 // bits 0-59
	hour   uint64 // bits 0-23
	dom    uint64 // bits 1-31
	month  uint64 // bits 1-12
	dow    uint64 // bits 0-6 (Sunday = 0)

	domStar bool // day-of-month field started with * or ?
	dowStar bool
	spec    string
}

var macros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

type fieldSpec struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	monthNames = map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}
	dowNames = map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}
	fMinute = fieldSpec{"minute", 0, 59, nil}
	fHour   = fieldSpec{"hour", 0, 23, nil}
	fDom    = fieldSpec{"day-of-month", 1, 31, nil}
	fMonth  = fieldSpec{"month", 1, 12, monthNames}
	fDow    = fieldSpec{"day-of-week", 0, 7, dowNames}
)

// Parse parses a 5-field cron expression or an @macro.
func Parse(spec string) (*Schedule, error) {
	orig := spec
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("cron: empty expression")
	}
	if strings.HasPrefix(spec, "@") {
		exp, ok := macros[strings.ToLower(spec)]
		if !ok {
			return nil, fmt.Errorf("cron: unknown macro %q", spec)
		}
		spec = exp
	}
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron: expected 5 fields (minute hour day-of-month month day-of-week), got %d", len(parts))
	}
	s := &Schedule{spec: strings.TrimSpace(orig)}
	var err error
	if s.minute, _, err = parseField(parts[0], fMinute); err != nil {
		return nil, err
	}
	if s.hour, _, err = parseField(parts[1], fHour); err != nil {
		return nil, err
	}
	if s.dom, s.domStar, err = parseField(parts[2], fDom); err != nil {
		return nil, err
	}
	if s.month, _, err = parseField(parts[3], fMonth); err != nil {
		return nil, err
	}
	var dow uint64
	if dow, s.dowStar, err = parseField(parts[4], fDow); err != nil {
		return nil, err
	}
	// Fold 7 (Sunday) onto 0.
	if dow&(1<<7) != 0 {
		dow |= 1
	}
	s.dow = dow & 0x7f
	return s, nil
}

// MustParse is like Parse but panics. For constants and tests.
func MustParse(spec string) *Schedule {
	s, err := Parse(spec)
	if err != nil {
		panic(err)
	}
	return s
}

// String returns the expression the schedule was parsed from.
func (s *Schedule) String() string { return s.spec }

func parseField(field string, f fieldSpec) (bits uint64, star bool, err error) {
	if field == "" {
		return 0, false, fmt.Errorf("cron: empty %s field", f.name)
	}
	star = field[0] == '*' || field[0] == '?'
	for _, term := range strings.Split(field, ",") {
		b, err := parseTerm(term, f)
		if err != nil {
			return 0, false, err
		}
		bits |= b
	}
	return bits, star, nil
}

func parseTerm(term string, f fieldSpec) (uint64, error) {
	if term == "" {
		return 0, fmt.Errorf("cron: empty list item in %s field", f.name)
	}
	rangePart, stepPart, hasStep := strings.Cut(term, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepPart)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("cron: invalid step %q in %s field", stepPart, f.name)
		}
		step = n
	}
	var lo, hi int
	switch {
	case rangePart == "*" || rangePart == "?":
		lo, hi = f.min, f.max
		if f.max == 7 { // day-of-week: * means 0-6
			hi = 6
		}
	case strings.Contains(rangePart, "-"):
		a, b, _ := strings.Cut(rangePart, "-")
		var err error
		if lo, err = parseValue(a, f); err != nil {
			return 0, err
		}
		if hi, err = parseValue(b, f); err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, fmt.Errorf("cron: range %q is backwards in %s field", rangePart, f.name)
		}
	default:
		v, err := parseValue(rangePart, f)
		if err != nil {
			return 0, err
		}
		lo, hi = v, v
		if hasStep { // "a/n" means a through max
			hi = f.max
			if f.max == 7 {
				hi = 6
			}
		}
	}
	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << uint(v)
	}
	return bits, nil
}

func parseValue(s string, f fieldSpec) (int, error) {
	if f.names != nil {
		if v, ok := f.names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("cron: invalid value %q in %s field", s, f.name)
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("cron: value %d out of range %d-%d in %s field", v, f.min, f.max, f.name)
	}
	return v, nil
}

func (s *Schedule) dayMatches(y int, m time.Month, d int, loc *time.Location) bool {
	domOK := s.dom&(1<<uint(d)) != 0
	wd := time.Date(y, m, d, 12, 0, 0, 0, loc).Weekday()
	dowOK := s.dow&(1<<uint(wd)) != 0
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dowOK
	case s.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

// searchYears bounds the search; an expression like "0 0 30 2 *" never fires.
const searchYears = 8

// Next returns the first matching time strictly after t, in t's location, or
// the zero time when the expression never fires within 8 years. Wall-clock
// fields are matched in t's location: a wall time that does not exist (spring
// forward gap) is skipped for that day; an ambiguous wall time (fall back
// overlap) fires once, at its first occurrence.
func (s *Schedule) Next(t time.Time) time.Time {
	loc := t.Location()
	start := t.Truncate(time.Minute).Add(time.Minute)
	y, mo, d := start.Date()
	endYear := y + searchYears
	for first := true; y <= endYear; first = false {
		if s.month&(1<<uint(mo)) == 0 {
			y, mo, d = normDate(y, mo+1, 1, loc)
			continue
		}
		if s.dayMatches(y, mo, d, loc) {
			h0, m0 := 0, 0
			if first {
				h0, m0 = start.Hour(), start.Minute()
			}
			for h := h0; h < 24; h++ {
				if s.hour&(1<<uint(h)) == 0 {
					continue
				}
				mi0 := 0
				if h == h0 {
					mi0 = m0
				}
				for mi := mi0; mi < 60; mi++ {
					if s.minute&(1<<uint(mi)) == 0 {
						continue
					}
					c := time.Date(y, mo, d, h, mi, 0, 0, loc)
					// Date normalises a nonexistent wall time (DST gap) to a
					// different wall time; skip those.
					if c.Hour() != h || c.Minute() != mi || c.Day() != d {
						continue
					}
					if !c.Before(start) {
						return c
					}
				}
			}
		}
		y, mo, d = normDate(y, mo, d+1, loc)
	}
	return time.Time{}
}

func normDate(y int, m time.Month, d int, loc *time.Location) (int, time.Month, int) {
	return time.Date(y, m, d, 12, 0, 0, 0, loc).Date()
}

// NextIn is Next evaluated in the named location. An empty name means UTC.
func (s *Schedule) NextIn(t time.Time, tz string) (time.Time, error) {
	loc, err := LoadLocation(tz)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(t.In(loc)).UTC(), nil
}

// LoadLocation resolves an IANA zone name; "" and "UTC" mean UTC.
func LoadLocation(tz string) (*time.Location, error) {
	if tz == "" || strings.EqualFold(tz, "utc") {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("cron: unknown timezone %q", tz)
	}
	return loc, nil
}
