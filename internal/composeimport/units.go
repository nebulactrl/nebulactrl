package composeimport

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// scalar renders a YAML scalar the way Compose does when it becomes an
// environment value or an argument.
func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case uint64:
		return strconv.FormatUint(x, 10), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	}
	return "", false
}

// toInt reads an integer written as a number or a numeric string.
func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case uint64:
		return int(x), true
	case float64:
		if x == math.Trunc(x) {
			return int(x), true
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	}
	return 0, false
}

// toFloat reads a number or numeric string.
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// parseDuration reads a Compose duration ("30s", "1m30s", "2h", "500ms").
func parseDuration(v any) (time.Duration, bool) {
	switch x := v.(type) {
	case string:
		x = strings.TrimSpace(x)
		if x == "" {
			return 0, false
		}
		d, err := time.ParseDuration(x)
		return d, err == nil
	case int:
		return time.Duration(x) * time.Second, true
	case float64:
		return time.Duration(x * float64(time.Second)), true
	}
	return 0, false
}

var memRe = regexp.MustCompile(`(?i)^\s*([0-9]*\.?[0-9]+)\s*(b|k|kb|kib|m|mb|mib|g|gb|gib|t|tb|tib)?\s*$`)

// parseMemoryMB reads a Compose byte size ("512m", "1.5g", "1GB", 268435456)
// and returns whole mebibytes, rounded up.
func parseMemoryMB(v any) (int, bool) {
	var s string
	switch x := v.(type) {
	case int:
		s = strconv.Itoa(x)
	case int64:
		s = strconv.FormatInt(x, 10)
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		s = x
	default:
		return 0, false
	}
	m := memRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	mult := 1.0
	switch strings.ToLower(m[2]) {
	case "k", "kb", "kib":
		mult = 1 << 10
	case "m", "mb", "mib":
		mult = 1 << 20
	case "g", "gb", "gib":
		mult = 1 << 30
	case "t", "tb", "tib":
		mult = 1 << 40
	}
	return int(math.Ceil(n * mult / (1 << 20))), true
}

// shellSplit splits a shell-form command the way Compose does (no shell is
// involved): whitespace separates words, single and double quotes group, and a
// backslash escapes the next character outside single quotes.
func shellSplit(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, fmt.Errorf("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
			inWord = true
		case c == '"':
			i++
			closed := false
			for ; i < len(s); i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte(`"\$`+"`", s[i+1]) >= 0 {
					cur.WriteByte(s[i+1])
					i++
					continue
				}
				if s[i] == '"' {
					closed = true
					break
				}
				cur.WriteByte(s[i])
			}
			if !closed {
				return nil, fmt.Errorf("unterminated double quote")
			}
			inWord = true
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(s[i+1])
			i++
			inWord = true
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// maskURLSecrets hides the password of every scheme://user:password@host in
// s, except passwords that are references.
func maskURLSecrets(s string) string {
	return urlUserinfoRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := urlUserinfoRe.FindStringSubmatch(m)
		if strings.HasPrefix(strings.TrimSuffix(m[len(sub[1])+len(sub[2])+1:], "@"), "${{") {
			return m
		}
		return sub[1] + sub[2] + ":••••••@"
	})
}

// urlUserinfoRe matches scheme://user:password@ and captures scheme://, user.
var urlUserinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^:/@\s]*):[^@/\s]+@`)

var dsnPasswordRe = regexp.MustCompile(`(?i)((?:password|pwd)\s*=\s*)([^\s;&'"]+)`)

// maskDSN hides password=... entries of connection strings.
func maskDSN(s string) string {
	return dsnPasswordRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := dsnPasswordRe.FindStringSubmatch(m)
		if strings.HasPrefix(sub[2], "${{") {
			return m
		}
		return sub[1] + "••••••"
	})
}
