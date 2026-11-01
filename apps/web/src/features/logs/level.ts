// Finds the log level in a line, but only when the line states one. Free text
// that merely contains the word "error" is not a level: a viewer that colours
// guesses teaches readers to distrust the colours.

export type LogLevel = "debug" | "info" | "warn" | "error";

export interface ParsedLevel {
	level: LogLevel;
	/** Where the level word sits in the line, so the viewer can colour just it. */
	start: number;
	end: number;
}

const BY_NAME: Record<string, LogLevel> = {
	trace: "debug",
	debug: "debug",
	info: "info",
	notice: "info",
	warn: "warn",
	warning: "warn",
	error: "error",
	err: "error",
	fatal: "error",
	panic: "error",
	critical: "error",
};

/** `warning` → `warn`, `FATAL` → `error`; undefined for a word that is no level. */
export function levelFromName(word: string): LogLevel | undefined {
	const key = word.toLowerCase();
	return Object.hasOwn(BY_NAME, key) ? BY_NAME[key] : undefined;
}

// level=warn, lvl=info, "level":"error", severity: ERROR (logfmt and JSON).
const KEYED = /(?:^|[\s{,])"?(?:level|lvl|severity)"?\s*[=:]\s*"?([A-Za-z]+)"?/;

// A leading level word after an optional timestamp: "[ERROR] x", "WARN x",
// "2025-01-02T03:04:05Z INFO x", nginx's "2025/01/02 03:04:05 [error] 29#29: x".
// A bare word is upper case only, so "Error loading" is prose; a bracketed
// word may be any case: "[info]", "[warn]".
const STAMP =
	"(?:\\d{4}[-/]\\d{2}[-/]\\d{2}[T ][\\d:.,]+(?:Z|[+-]\\d{2}:?\\d{2})?\\s+)?";
const WORDS =
	"INFO|NOTICE|WARN|WARNING|ERROR|ERR|FATAL|PANIC|CRITICAL|DEBUG|TRACE";
const LEADING = new RegExp(`^\\s*${STAMP}\\[?(${WORDS})\\]?(?=[\\s:\\]|]|$)`);
const BRACKETED = new RegExp(`^\\s*${STAMP}\\[(${WORDS})\\]`, "i");

/** The level a line states (key=value, JSON or a leading word), or null. */
export function parseLevel(line: string): ParsedLevel | null {
	const keyed = KEYED.exec(line);
	const word = keyed?.[1];
	if (keyed && word) {
		const level = BY_NAME[word.toLowerCase()];
		if (level) {
			// The word is the last capture; locate it inside the match.
			const end = keyed.index + keyed[0].lastIndexOf(word) + word.length;
			return { level, start: end - word.length, end };
		}
	}
	for (const pattern of [LEADING, BRACKETED]) {
		const lead = pattern.exec(line);
		const token = lead?.[1];
		if (!lead || !token) continue;
		const level = BY_NAME[token.toLowerCase()];
		if (level) {
			const start = lead.index + lead[0].lastIndexOf(token);
			return { level, start, end: start + token.length };
		}
	}
	return null;
}
