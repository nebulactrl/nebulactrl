package coolify

import (
	"encoding/json"
	"errors"
	"strings"
)

// shellWords splits s into words the way a POSIX shell does, without running
// one: unquoted whitespace (including newlines) separates words, single quotes
// keep everything literally, double quotes keep everything except that a
// backslash escapes \ " $ ` and a newline, and a backslash outside quotes
// escapes the next character (a backslash-newline pair is a line continuation).
// No expansion of any kind happens. An unterminated quote is an error.
func shellWords(s string) ([]string, error) {
	var (
		out    []string
		cur    strings.Builder
		inWord bool
	)
	flush := func() {
		if inWord {
			out = append(out, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		case c == '\\':
			if i+1 >= len(rs) {
				cur.WriteRune(c)
				inWord = true
				break
			}
			i++
			if rs[i] == '\n' {
				break // line continuation
			}
			if rs[i] == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
				i++
				break
			}
			cur.WriteRune(rs[i])
			inWord = true
		case c == '\'':
			inWord = true
			j := i + 1
			for ; j < len(rs) && rs[j] != '\''; j++ {
				cur.WriteRune(rs[j])
			}
			if j >= len(rs) {
				return nil, errors.New("unterminated single quote")
			}
			i = j
		case c == '"':
			inWord = true
			j := i + 1
			for ; j < len(rs) && rs[j] != '"'; j++ {
				if rs[j] == '\\' && j+1 < len(rs) {
					switch rs[j+1] {
					case '\\', '"', '$', '`':
						j++
					case '\n':
						j++
						continue
					}
				}
				cur.WriteRune(rs[j])
			}
			if j >= len(rs) {
				return nil, errors.New("unterminated double quote")
			}
			i = j
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	flush()
	return out, nil
}

// commandArgv turns a Coolify command string (a pre-/post-deployment command)
// into an argv. Coolify runs these through `sh -c`, so a command that uses
// shell syntax keeps it by running under sh -c; a plain command is split into words.
func commandArgv(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	if strings.HasPrefix(cmd, "[") {
		var arr []string
		if json.Unmarshal([]byte(cmd), &arr) == nil && len(arr) > 0 {
			return arr
		}
	}
	if strings.ContainsAny(cmd, "|&;<>()$`\\\n*?~{}#") {
		return []string{"sh", "-c", cmd}
	}
	words, err := shellWords(cmd)
	if err != nil || len(words) == 0 {
		return []string{"sh", "-c", cmd}
	}
	return words
}

// quoteForNote renders an argv for a review note (display only).
func quoteForNote(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\"'$\\") {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}
