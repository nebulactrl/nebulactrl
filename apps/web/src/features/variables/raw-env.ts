import type { Variable } from "#/api/models";

/** One `KEY=value` row of the raw `.env` editor. */
export interface EnvEntry {
	key: string;
	value: string;
}

interface EnvParseError {
	/** 1-based line number in the pasted text. */
	line: number;
	message: string;
}

export interface EnvParseResult {
	entries: EnvEntry[];
	errors: EnvParseError[];
}

/** What saving the raw editor changes, as API calls. */
export interface EnvDiff {
	/**
	 * `isSecret` is only ever `true` (replacing an existing secret): left out,
	 * the server keeps the stored flag and lets generators decide it.
	 */
	upserts: Array<{ key: string; value: string; isSecret?: true }>;
	/** Keys to delete (only ever non-secret ones). */
	deletes: string[];
}

export const ENV_KEY_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;

const NEEDS_QUOTES = /[\s#"'\\]/;

function quote(value: string): string {
	if (value === "" || !NEEDS_QUOTES.test(value)) return value;
	const escaped = value
		.replaceAll("\\", "\\\\")
		.replaceAll('"', '\\"')
		.replaceAll("\n", "\\n")
		.replaceAll("\r", "\\r");
	return `"${escaped}"`;
}

/**
 * The raw editor's text: every non-secret variable as `KEY=value`. Secrets
 * are write-only, so they never appear here; saving leaves them alone.
 */
export function serializeEnv(variables: readonly Variable[]): string {
	return variables
		.filter((variable) => !variable.isSecret)
		.map((variable) => `${variable.key}=${quote(variable.value ?? "")}`)
		.join("\n");
}

function unquoteDouble(raw: string): string | undefined {
	let out = "";
	for (let index = 0; index < raw.length; index++) {
		const char = raw[index];
		if (char === "\\") {
			const next = raw[index + 1];
			if (next === undefined) return undefined;
			index++;
			if (next === "n") out += "\n";
			else if (next === "r") out += "\r";
			else if (next === "t") out += "\t";
			else out += next;
		} else if (char === '"') {
			return undefined;
		} else {
			out += char;
		}
	}
	return out;
}

function parseValue(rest: string): string | undefined {
	const trimmed = rest.trim();
	if (trimmed.startsWith('"')) {
		// Find the closing quote, skipping escaped ones.
		let end = -1;
		for (let index = 1; index < trimmed.length; index++) {
			if (trimmed[index] === "\\") index++;
			else if (trimmed[index] === '"') {
				end = index;
				break;
			}
		}
		if (end === -1) return undefined;
		const after = trimmed.slice(end + 1).trim();
		if (after !== "" && !after.startsWith("#")) return undefined;
		return unquoteDouble(trimmed.slice(1, end));
	}
	if (trimmed.startsWith("'")) {
		const end = trimmed.indexOf("'", 1);
		if (end === -1) return undefined;
		const after = trimmed.slice(end + 1).trim();
		if (after !== "" && !after.startsWith("#")) return undefined;
		return trimmed.slice(1, end);
	}
	// Unquoted: a ` #` starts a trailing comment.
	const comment = trimmed.search(/\s#/);
	return (comment === -1 ? trimmed : trimmed.slice(0, comment)).trim();
}

/**
 * Parses `.env` text: `KEY=value`, optional `export `, `#` comments, blank
 * lines, and single- or double-quoted values. A double-quoted value may span
 * several lines. Duplicate keys are an error so a paste cannot silently drop
 * one.
 */
export function parseEnv(text: string): EnvParseResult {
	const entries: EnvEntry[] = [];
	const errors: EnvParseError[] = [];
	const seen = new Set<string>();
	const lines = text.split(/\r?\n/);
	for (let index = 0; index < lines.length; index++) {
		const line = index + 1;
		let content = (lines[index] ?? "").trim();
		if (content === "" || content.startsWith("#")) continue;
		if (content.startsWith("export ")) content = content.slice(7).trim();
		const equals = content.indexOf("=");
		if (equals === -1) {
			errors.push({ line, message: "Expected KEY=value." });
			continue;
		}
		const key = content.slice(0, equals).trim();
		if (!ENV_KEY_PATTERN.test(key)) {
			errors.push({
				line,
				message: `“${key}” is not a valid name: use letters, digits and underscores, not starting with a digit.`,
			});
			continue;
		}
		let rest = content.slice(equals + 1);
		let value = parseValue(rest);
		// An unclosed double quote continues on the following lines.
		let last = index;
		while (
			value === undefined &&
			rest.trim().startsWith('"') &&
			last + 1 < lines.length
		) {
			last++;
			rest += `\n${lines[last] ?? ""}`;
			value = parseValue(rest);
		}
		if (seen.has(key)) {
			errors.push({ line, message: `${key} appears more than once.` });
			if (value !== undefined) index = last;
			continue;
		}
		if (value === undefined) {
			errors.push({
				line,
				message: `The value of ${key} has an unclosed quote.`,
			});
			index = last; // the rest of the text belongs to the unclosed value
			continue;
		}
		index = last;
		seen.add(key);
		entries.push({ key, value });
	}
	return { entries, errors };
}

/**
 * What saving the raw text does to the stored variables.
 *
 * - A key that is new, or whose text changed, is upserted (plain).
 * - A key that names an existing secret is upserted as a secret: the raw view
 *   never shows secrets, so typing one is how it is replaced.
 * - A non-secret key missing from the text is deleted.
 * - Secrets missing from the text are kept: they were never in it.
 */
export function diffEnv(
	existing: readonly Variable[],
	entries: readonly EnvEntry[],
): EnvDiff {
	const byKey = new Map(existing.map((variable) => [variable.key, variable]));
	const upserts: EnvDiff["upserts"] = [];
	for (const entry of entries) {
		const current = byKey.get(entry.key);
		if (current?.isSecret) {
			upserts.push({ key: entry.key, value: entry.value, isSecret: true });
		} else if (!current || (current.value ?? "") !== entry.value) {
			upserts.push({ key: entry.key, value: entry.value });
		}
	}
	const written = new Set(entries.map((entry) => entry.key));
	const deletes = existing
		.filter((variable) => !variable.isSecret && !written.has(variable.key))
		.map((variable) => variable.key);
	return { upserts, deletes };
}
