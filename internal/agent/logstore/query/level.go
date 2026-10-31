package query

import "regexp"

// Port of apps/web/src/features/logs/level.ts. Only the level is needed, not
// its span, so the JS lookahead `(?=[\s:\]|]|$)` becomes a consumed character.

func levelFromName(word string) string {
	switch jsLower(word) {
	case "trace", "debug":
		return "debug"
	case "info", "notice":
		return "info"
	case "warn", "warning":
		return "warn"
	case "error", "err", "fatal", "panic", "critical":
		return "error"
	}
	return ""
}

// level=warn, lvl=info, "level":"error", severity: ERROR (logfmt and JSON).
var keyedLevel = regexp.MustCompile(`(?:^|[` + jsWS + `{,])"?(?:level|lvl|severity)"?[` + jsWS + `]*[=:][` + jsWS + `]*"?([A-Za-z]+)"?`)

const (
	levelStamp = `(?:[0-9]{4}[-/][0-9]{2}[-/][0-9]{2}[T ][0-9:.,]+(?:Z|[+-][0-9]{2}:?[0-9]{2})?[` + jsWS + `]+)?`
	// The longer word first where one is a prefix of the other.
	levelWords = `INFO|NOTICE|WARNING|WARN|ERROR|ERR|FATAL|PANIC|CRITICAL|DEBUG|TRACE`
)

// A leading level word after an optional timestamp: "[ERROR] x", "WARN x",
// "2025-01-02T03:04:05Z INFO x", nginx's "2025/01/02 03:04:05 [error] 29#29: x".
// A bare word is upper case only, so "Error loading" is prose; a bracketed
// word may be any case: "[info]", "[warn]".
var (
	leadingLevel = regexp.MustCompile(`^[` + jsWS + `]*` + levelStamp + `\[?(` + levelWords + `)\]?(?:[` + jsWS + `:\]|]|$)`)
	bracketLevel = regexp.MustCompile(`(?i)^[` + jsWS + `]*` + levelStamp + `\[(` + levelWords + `)\]`)
)

// textLevel is the level a line states in its text: key=value, JSON or a
// leading word. "" when it states none.
func textLevel(line string) string {
	if m := keyedLevel.FindStringSubmatch(line); m != nil {
		if level := levelFromName(m[1]); level != "" {
			return level
		}
	}
	for _, re := range [...]*regexp.Regexp{leadingLevel, bracketLevel} {
		if m := re.FindStringSubmatch(line); m != nil {
			if level := levelFromName(m[1]); level != "" {
				return level
			}
		}
	}
	return ""
}
