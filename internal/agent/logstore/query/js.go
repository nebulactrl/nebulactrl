package query

// JavaScript semantics the web implementation relies on: Number(string),
// String(number), trim() and toLowerCase(). The Go port has to agree with them
// on every input, so they are spelled out here rather than approximated with
// strconv and strings.

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// isJSSpace is JavaScript's WhiteSpace plus LineTerminator set (\s, trim()).
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// jsWS is the body of a regexp character class matching isJSSpace.
const jsWS = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// jsToNumber implements Number(s) for a string; ok is false for NaN.
func jsToNumber(s string) (n float64, ok bool) {
	s = jsTrim(s)
	if s == "" {
		return 0, true
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	if len(s) > 2 && s[0] == '0' {
		base := 0
		switch s[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			return parseRadix(s[2:], base)
		}
	}
	if !validDecimal(s) {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return f, true
		}
		return 0, false
	}
	return f, true
}

func parseRadix(digits string, base int) (float64, bool) {
	for i := range len(digits) {
		c := digits[i]
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, false
		}
		if d >= base {
			return 0, false
		}
	}
	if u, err := strconv.ParseUint(digits, base, 64); err == nil {
		return float64(u), true
	}
	z, ok := new(big.Int).SetString(digits, base)
	if !ok {
		return 0, false
	}
	f, _ := new(big.Float).SetInt(z).Float64()
	return f, true
}

// validDecimal reports whether s is a StrDecimalLiteral: [+-] digits [. digits] [e[+-]digits].
func validDecimal(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digits := func() int {
		n := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			n++
		}
		return n
	}
	n := digits()
	if i < len(s) && s[i] == '.' {
		i++
		n += digits()
	}
	if n == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if digits() == 0 {
			return false
		}
	}
	return i == len(s)
}

// jsNumString implements String(n) for a number.
func jsNumString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case f == 0:
		return "0"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	sign := ""
	if f < 0 {
		sign = "-"
		f = -f
	}
	// Shortest round-trip digits: d.ddde±xx.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr)
	digits := strings.Replace(mant, ".", "", 1)
	k := len(digits)
	n := exp + 1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	ex := n - 1
	esign := "+"
	if ex < 0 {
		esign = "-"
		ex = -ex
	}
	if k == 1 {
		return sign + digits + "e" + esign + strconv.Itoa(ex)
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + esign + strconv.Itoa(ex)
}

// jsLower implements String.prototype.toLowerCase: simple lower casing plus
// the two special cases (U+0130 and the final sigma).
func jsLower(s string) string {
	ascii, upper := true, false
	for i := range len(s) {
		c := s[i]
		if c >= utf8.RuneSelf {
			ascii = false
			break
		}
		if c >= 'A' && c <= 'Z' {
			upper = true
		}
	}
	if ascii {
		if !upper {
			return s
		}
		return strings.ToLower(s)
	}
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range rs {
		switch {
		case r == 0x130:
			b.WriteString("i\u0307")
		case r == 'Σ':
			if finalSigma(rs, i) {
				b.WriteRune('ς')
			} else {
				b.WriteRune('σ')
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func isCased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r)
}

func isCaseIgnorable(r rune) bool {
	switch r {
	case '\'', '.', ':', '·', '‘', '’', '․', '‧', '﹒', '﹕', '＇', '．', '：', '^', '`', '¨', '¯', '´', '¸':
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}

// finalSigma: preceded by a cased letter and not followed by one, skipping
// case-ignorable characters on both sides.
func finalSigma(rs []rune, i int) bool {
	before := false
	for j := i - 1; j >= 0; j-- {
		if isCaseIgnorable(rs[j]) {
			continue
		}
		before = isCased(rs[j])
		break
	}
	if !before {
		return false
	}
	for j := i + 1; j < len(rs); j++ {
		if isCaseIgnorable(rs[j]) {
			continue
		}
		return !isCased(rs[j])
	}
	return true
}
