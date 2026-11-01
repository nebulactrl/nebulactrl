package cronexpr

import (
	"testing"
	"time"
)

func utc(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

func TestNext(t *testing.T) {
	base := utc(2026, time.October, 26, 10, 7) // Monday
	cases := []struct {
		spec string
		want time.Time
	}{
		{"* * * * *", utc(2026, 10, 26, 10, 8)},
		{"*/5 * * * *", utc(2026, 10, 26, 10, 10)},
		{"7 * * * *", utc(2026, 10, 26, 11, 7)}, // strictly after
		{"0 3 * * *", utc(2026, 10, 27, 3, 0)},
		{"30 10 * * *", utc(2026, 10, 26, 10, 30)},
		{"0,15,30,45 * * * *", utc(2026, 10, 26, 10, 15)},
		{"10-20/5 10 * * *", utc(2026, 10, 26, 10, 10)},
		{"5/20 10 * * *", utc(2026, 10, 26, 10, 25)}, // 5,25,45
		{"0 0 1 * *", utc(2026, 11, 1, 0, 0)},
		{"0 0 1 1 *", utc(2027, 1, 1, 0, 0)},
		{"0 12 * * mon-fri", utc(2026, 10, 26, 12, 0)},
		{"0 12 * * 0", utc(2026, 11, 1, 12, 0)}, // Sunday
		{"0 12 * * 7", utc(2026, 11, 1, 12, 0)}, // 7 = Sunday
		{"0 0 * jan,jul *", utc(2027, 1, 1, 0, 0)},
		{"0 0 29 2 *", utc(2028, 2, 29, 0, 0)}, // leap day
		{"15 10 ? * ?", utc(2026, 10, 26, 10, 15)},
	}
	for _, tc := range cases {
		s, err := Parse(tc.spec)
		if err != nil {
			t.Fatalf("%q: %v", tc.spec, err)
		}
		if got := s.Next(base); !got.Equal(tc.want) {
			t.Errorf("%q: got %v want %v", tc.spec, got, tc.want)
		}
	}
}

func TestMacros(t *testing.T) {
	base := utc(2026, time.October, 26, 10, 7) // Monday
	cases := map[string]time.Time{
		"@hourly":   utc(2026, 10, 26, 11, 0),
		"@daily":    utc(2026, 10, 27, 0, 0),
		"@midnight": utc(2026, 10, 27, 0, 0),
		"@weekly":   utc(2026, 11, 1, 0, 0), // Sunday
		"@monthly":  utc(2026, 11, 1, 0, 0),
		"@yearly":   utc(2027, 1, 1, 0, 0),
		"@annually": utc(2027, 1, 1, 0, 0),
		"@DAILY":    utc(2026, 10, 27, 0, 0),
	}
	for spec, want := range cases {
		s, err := Parse(spec)
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		if got := s.Next(base); !got.Equal(want) {
			t.Errorf("%s: got %v want %v", spec, got, want)
		}
	}
}

func TestDomDowSemantics(t *testing.T) {
	base := utc(2026, time.October, 26, 10, 7)
	// Both restricted: either matches. 1st of month OR Wednesday. Next Wed is Oct 28.
	s := MustParse("0 0 1 * wed")
	if got, want := s.Next(base), utc(2026, 10, 28, 0, 0); !got.Equal(want) {
		t.Errorf("either-match: got %v want %v", got, want)
	}
	// dom restricted, dow star: only dom.
	s = MustParse("0 0 15 * *")
	if got, want := s.Next(base), utc(2026, 11, 15, 0, 0); !got.Equal(want) {
		t.Errorf("dom only: got %v want %v", got, want)
	}
}

func TestNeverFires(t *testing.T) {
	s := MustParse("0 0 30 2 *")
	if got := s.Next(utc(2026, 1, 1, 0, 0)); !got.IsZero() {
		t.Errorf("Feb 30 should never fire, got %v", got)
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		"", "   ", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *",
		"* * 32 * *", "* * * 13 *", "* * * * 8", "5-1 * * * *", "*/0 * * * *", "*/x * * * *",
		"a * * * *", "1,,2 * * * *", "@never", "-5 * * * *", "1-2-3 * * * *", "* * * foo *",
	}
	for _, spec := range bad {
		if _, err := Parse(spec); err == nil {
			t.Errorf("%q: expected error", spec)
		}
	}
}

func TestParseWhitespaceAndCase(t *testing.T) {
	s, err := Parse("  0   3 * * SUN  ")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.Next(utc(2026, 10, 26, 0, 0)), utc(2026, 11, 1, 3, 0); !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSequenceAcrossDays(t *testing.T) {
	s := MustParse("30 23 * * *")
	cur := utc(2026, 12, 30, 0, 0)
	want := []time.Time{utc(2026, 12, 30, 23, 30), utc(2026, 12, 31, 23, 30), utc(2027, 1, 1, 23, 30)}
	for i, w := range want {
		cur = s.Next(cur)
		if !cur.Equal(w) {
			t.Fatalf("step %d: got %v want %v", i, cur, w)
		}
	}
}

// DST: next-run in UTC is unaffected by local DST dates.
func TestNextUTCAcrossDSTDates(t *testing.T) {
	// US spring-forward 2026-03-08, fall-back 2026-11-01. UTC has no DST, so a
	// daily 02:30 UTC job fires every 24h across both dates.
	s := MustParse("30 2 * * *")
	cur := utc(2026, 3, 7, 12, 0)
	for _, w := range []time.Time{utc(2026, 3, 8, 2, 30), utc(2026, 3, 9, 2, 30)} {
		cur = s.Next(cur)
		if !cur.Equal(w) {
			t.Fatalf("got %v want %v", cur, w)
		}
	}
	cur = utc(2026, 10, 31, 12, 0)
	for _, w := range []time.Time{utc(2026, 11, 1, 2, 30), utc(2026, 11, 2, 2, 30)} {
		cur = s.Next(cur)
		if !cur.Equal(w) {
			t.Fatalf("got %v want %v", cur, w)
		}
	}
	got, err := s.NextIn(utc(2026, 3, 7, 12, 0), "UTC")
	if err != nil || !got.Equal(utc(2026, 3, 8, 2, 30)) {
		t.Fatalf("NextIn UTC: %v %v", got, err)
	}
}

func TestNextDSTZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	// Spring forward 2026-03-08 02:00 -> 03:00: 02:30 does not exist; skipped that day.
	s := MustParse("30 2 * * *")
	from := time.Date(2026, 3, 8, 0, 0, 0, 0, ny)
	got := s.Next(from)
	if want := time.Date(2026, 3, 9, 2, 30, 0, 0, ny); !got.Equal(want) {
		t.Fatalf("gap day: got %v want %v", got, want)
	}
	// Fall back 2026-11-01 02:00 EDT -> 01:00 EST: 01:30 happens twice, fires once (first).
	s = MustParse("30 1 * * *")
	from = time.Date(2026, 11, 1, 0, 0, 0, 0, ny)
	first := s.Next(from)
	if first.Hour() != 1 || first.Minute() != 30 || first.Day() != 1 {
		t.Fatalf("overlap first: %v", first)
	}
	second := s.Next(first)
	if second.Day() != 2 {
		t.Fatalf("overlap must fire once; next after first = %v", second)
	}
	// Hourly across the gap keeps firing on the hour with no duplicates.
	h := MustParse("0 * * * *")
	cur := time.Date(2026, 3, 8, 0, 30, 0, 0, ny)
	var hours []int
	for range 4 {
		cur = h.Next(cur)
		hours = append(hours, cur.Hour())
	}
	if hours[0] != 1 || hours[1] != 3 || hours[2] != 4 || hours[3] != 5 {
		t.Fatalf("hourly across gap: %v", hours)
	}
}

func TestNextInTimezone(t *testing.T) {
	if _, err := LoadLocation("Europe/Berlin"); err != nil {
		t.Skip("tzdata unavailable")
	}
	s := MustParse("0 3 * * *")
	got, err := s.NextIn(utc(2026, 7, 1, 0, 0), "Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	if want := utc(2026, 7, 1, 1, 0); !got.Equal(want) { // 03:00 CEST = 01:00 UTC
		t.Fatalf("got %v want %v", got, want)
	}
	if _, err := s.NextIn(time.Now(), "Not/AZone"); err == nil {
		t.Fatal("expected unknown timezone error")
	}
}
