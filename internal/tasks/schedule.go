package tasks

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/cronexpr"
)

// nextRun returns the first run strictly after t, evaluating expr in the IANA
// zone tz ("" means UTC). The result is an absolute instant (UTC). A wall time
// that does not exist (the spring-forward gap) is skipped for that day; one that
// occurs twice (the fall-back hour) fires once, the first time it is reached.
func nextRun(expr, tz string, t time.Time) (time.Time, error) {
	sch, err := cronexpr.Parse(expr)
	if err != nil {
		return time.Time{}, apierr.Unprocessable("invalid_cron", err.Error())
	}
	next, err := sch.NextIn(t, tz)
	if err != nil {
		return time.Time{}, apierr.Unprocessable("invalid_timezone", strings.TrimPrefix(err.Error(), "cron: "))
	}
	if next.IsZero() {
		return time.Time{}, apierr.Unprocessable("cron_never_fires", "the expression never matches a date")
	}
	return next, nil
}

// NormalizeTimezone validates an IANA zone name; "" and "UTC" (any case) are
// "UTC". The returned name is what is stored.
func NormalizeTimezone(tz string) (string, error) {
	tz = strings.TrimSpace(tz)
	if _, err := cronexpr.LoadLocation(tz); err != nil {
		return "", apierr.Unprocessable("invalid_timezone", strings.TrimPrefix(err.Error(), "cron: "))
	}
	if tz == "" || strings.EqualFold(tz, "utc") {
		return "UTC", nil
	}
	return tz, nil
}

// PreviewSchedule validates expr and returns a description plus the next count
// runs, evaluated in tz (the runs are absolute instants, serialized in UTC).
// An invalid expression or zone is not an error: the editor shows why.
func PreviewSchedule(expr, tz string, from time.Time, count int) SchedulePreview {
	p := SchedulePreview{NextRuns: []time.Time{}}
	norm, err := NormalizeTimezone(tz)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	p.Timezone = norm
	sch, err := cronexpr.Parse(expr)
	if err != nil {
		p.Error = strings.TrimPrefix(err.Error(), "cron: ")
		return p
	}
	t, err := sch.NextIn(from, norm)
	if err != nil || t.IsZero() {
		p.Error = "the expression never matches a date"
		return p
	}
	p.Valid = true
	p.Description = Describe(expr, norm)
	for i := 0; i < count && !t.IsZero(); i++ {
		p.NextRuns = append(p.NextRuns, t.UTC())
		t, _ = sch.NextIn(t, norm)
	}
	return p
}

var macroExpansion = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *", "@weekly": "0 0 * * 0",
	"@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var (
	dayNames   = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	dayAbbrevs = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
	monthNames = []string{"", "January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	monthAbbr  = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
)

// Describe summarizes a cron expression in plain English, naming the zone tz
// ("" = UTC). Expressions it has no phrase for come back as "Custom schedule:
// <expression> (<zone>)", so the description is always true, if sometimes less
// friendly.
func Describe(expr, tz string) string {
	if tz == "" {
		tz = "UTC"
	}
	spec := strings.ToLower(strings.TrimSpace(expr))
	if exp, ok := macroExpansion[spec]; ok {
		spec = exp
	}
	f := strings.Fields(spec)
	custom := "Custom schedule: " + strings.TrimSpace(expr) + " (" + tz + ")"
	if len(f) != 5 {
		return custom
	}
	minute, hour, dom, month, dow := f[0], f[1], f[2], f[3], f[4]
	star := func(s string) bool { return s == "*" || s == "?" }

	// Minute and hour patterns.
	var when string
	switch {
	case star(minute) && star(hour):
		when = "every minute"
	case isStep(minute) && star(hour):
		n := stepOf(minute)
		when = "every " + plural(n, "minute")
	case isNum(minute) && star(hour):
		m, _ := strconv.Atoi(minute)
		if m == 0 {
			when = "every hour, on the hour"
		} else {
			when = fmt.Sprintf("every hour at minute %d", m)
		}
	case isNum(minute) && isStep(hour):
		m, _ := strconv.Atoi(minute)
		when = "every " + plural(stepOf(hour), "hour") + fmt.Sprintf(" at minute %d", m)
	case isNum(minute) && isNum(hour):
		m, _ := strconv.Atoi(minute)
		h, _ := strconv.Atoi(hour)
		when = fmt.Sprintf("at %02d:%02d", h, m)
	default:
		return custom
	}
	timed := strings.HasPrefix(when, "at ")

	switch {
	case star(dom) && star(month) && star(dow):
		if timed {
			return "Every day " + when + " " + tz
		}
		return capitalize(when) + " (" + tz + ")"
	case star(dom) && star(month) && !star(dow):
		days, ok := describeDays(dow)
		if !ok {
			return custom
		}
		if timed {
			return "Every " + days + " " + when + " " + tz
		}
		return capitalize(when) + " on " + days + " (" + tz + ")"
	case isNum(dom) && star(month) && star(dow):
		d, _ := strconv.Atoi(dom)
		if timed {
			return fmt.Sprintf("On day %d of every month %s %s", d, when, tz)
		}
		return fmt.Sprintf("%s on day %d of every month (%s)", capitalize(when), d, tz)
	case isNum(dom) && !star(month) && star(dow):
		d, _ := strconv.Atoi(dom)
		mn, ok := monthOf(month)
		if !ok {
			return custom
		}
		if timed {
			return fmt.Sprintf("On %s %d %s %s", mn, d, when, tz)
		}
		return fmt.Sprintf("%s on %s %d (%s)", capitalize(when), mn, d, tz)
	}
	return custom
}

func isNum(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func isStep(s string) bool {
	rest, ok := strings.CutPrefix(s, "*/")
	return ok && isNum(rest)
}

func stepOf(s string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(s, "*/"))
	return n
}

func plural(n int, unit string) string {
	if n <= 1 {
		return unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func dayOf(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		if n == 7 {
			n = 0
		}
		return n, n >= 0 && n <= 6
	}
	n, ok := dayAbbrevs[s]
	return n, ok
}

// describeDays renders a day-of-week field made of single days, lists and one
// range ("1-5" is "weekday").
func describeDays(field string) (string, bool) {
	if a, b, ok := strings.Cut(field, "-"); ok && !strings.Contains(field, ",") && !strings.Contains(field, "/") {
		from, ok1 := dayOf(a)
		to, ok2 := dayOf(b)
		if !ok1 || !ok2 {
			return "", false
		}
		if from == 1 && to == 5 {
			return "weekday (Monday to Friday)", true
		}
		return dayNames[from] + " to " + dayNames[to], true
	}
	if strings.Contains(field, "/") {
		return "", false
	}
	var names []string
	for _, part := range strings.Split(field, ",") {
		d, ok := dayOf(part)
		if !ok {
			return "", false
		}
		names = append(names, dayNames[d])
	}
	switch len(names) {
	case 1:
		return names[0], true
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1], true
	}
}

func monthOf(s string) (string, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return monthNames[n%13], n >= 1 && n <= 12
	}
	if n, ok := monthAbbr[s]; ok {
		return monthNames[n], true
	}
	return "", false
}
