/**
 * Pure ranking behind the Name field of "New variable". Given what the person
 * has typed and what the environment can offer, it returns the names worth
 * suggesting, best first. The React side lives in `name-combobox.tsx`.
 *
 * Sources, in the order they win a tie:
 * - `export`: a virtual variable another service of the environment offers
 *   (a database's DATABASE_URL). Picking it fills the name and the value
 *   `${{slug.KEY}}`.
 * - `shared`: a shared variable, filled as `${{shared.KEY}}`.
 * - `team`: a team variable, filled as `${{team.KEY}}`.
 * - `env-example`: a key of the repository's `.env.example` not set yet.
 * - `common`: a name usual for the kind of service.
 * - `existing` / `platform`: keys that cannot be used; listed disabled, with
 *   the reason, only while the typed text matches them.
 */

import {
	PLATFORM_REFERENCE_KEYS,
	REFERENCE_OPEN,
	referenceFor,
	SHARED_NAMESPACE,
	TEAM_NAMESPACE,
} from "./reference-autocomplete";

export type NameSource =
	| "export"
	| "shared"
	| "team"
	| "env-example"
	| "common"
	| "existing"
	| "platform";

export type NameServiceKind = "app" | "worker" | "database" | "route" | "stack";

export interface NameSuggestion {
	name: string;
	/** What picking it puts in the value field; absent when the person must type one. */
	value?: string;
	source: NameSource;
	/** Where it comes from: a service slug, or the layer of a shared variable. */
	from?: string;
	description?: string;
	/** The value is or contains a secret (an export), or `.env.example` proposes one. */
	secret?: boolean;
	disabled?: boolean;
	/** Why a disabled suggestion cannot be picked. */
	reason?: string;
	/** The typed text inside `name`, for highlighting; absent when matched elsewhere. */
	match?: { start: number; end: number };
}

/** Another service of the environment, with the exports it offers. */
export interface NameService {
	slug: string;
	name: string;
	exports: readonly { key: string; description?: string; secret: boolean }[];
}

export interface EnvExampleKey {
	key: string;
	/** The proposed value; used only when the person does not have to supply one. */
	value?: string;
	needsInput?: boolean;
	proposedSecret?: boolean;
}

export interface NameSuggestionInput {
	/** The text in the Name field. */
	query: string;
	serviceKind: NameServiceKind;
	/** The other services of the environment (the edited service left out). */
	services: readonly NameService[];
	shared: readonly { key: string; layer?: string; secret?: boolean }[];
	/** The team's variables. */
	team?: readonly { key: string; secret?: boolean }[];
	envExample: readonly EnvExampleKey[];
	existingKeys: readonly string[];
	/** Defaults to the platform's reference keys. */
	reservedKeys?: readonly string[];
	limit?: number;
}

export const REASON_EXISTING = "Already set";
export const REASON_PLATFORM = "Provided by the platform";

/** Names usual for a kind of service. PORT is the platform's, so it is not here. */
const COMMON_NAMES: Record<NameServiceKind, readonly string[]> = {
	app: [
		"NODE_ENV",
		"LOG_LEVEL",
		"DATABASE_URL",
		"REDIS_URL",
		"SESSION_SECRET",
		"JWT_SECRET",
		"TZ",
	],
	worker: ["NODE_ENV", "LOG_LEVEL", "DATABASE_URL", "REDIS_URL", "TZ"],
	database: ["TZ"],
	route: [],
	// A stack's variables only fill `${VAR}` in its compose file.
	stack: [],
};

const SOURCE_ORDER: Record<NameSource, number> = {
	export: 0,
	shared: 1,
	team: 2,
	"env-example": 3,
	common: 4,
	existing: 5,
	platform: 6,
};

/** PORT and everything starting with NEBULA_ belong to the platform. */
export function isReservedKey(key: string): boolean {
	return key === "PORT" || key.toUpperCase().startsWith("NEBULA_");
}

/**
 * How well `needle` matches `name`: 0 at the start, 1 at a word start
 * (after an underscore), 2 anywhere else, null not at all.
 */
function matchName(
	name: string,
	needle: string,
): { score: number; start: number } | null {
	if (needle === "") return { score: 0, start: 0 };
	const start = name.toLowerCase().indexOf(needle.toLowerCase());
	if (start < 0) return null;
	if (start === 0) return { score: 0, start };
	return { score: name[start - 1] === "_" ? 1 : 2, start };
}

interface Row {
	suggestion: NameSuggestion;
	score: number;
	at: number;
}

/**
 * The suggestions for what is typed, best first: usable ones before disabled
 * ones, a closer match before a looser one, then by source. Without a typed
 * text only the usable offers (and offers already set) are listed.
 */
export function suggestNames(input: NameSuggestionInput): NameSuggestion[] {
	// A reference does not belong in the name: the field moves it to the value.
	if (input.query.includes(REFERENCE_OPEN)) return [];
	// A hyphen or a blank typed where an underscore belongs still finds the name.
	const needle = input.query.trim().replace(/[\s-]+/g, "_");
	const existing = new Set(input.existingKeys);
	const reserved = new Set(input.reservedKeys ?? PLATFORM_REFERENCE_KEYS);

	const offered: NameSuggestion[] = [];
	for (const service of [...input.services].sort((a, b) =>
		a.slug.localeCompare(b.slug),
	)) {
		for (const exported of [...service.exports].sort((a, b) =>
			a.key.localeCompare(b.key),
		)) {
			offered.push({
				name: exported.key,
				value: referenceFor(service.slug, exported.key),
				source: "export",
				from: service.slug,
				secret: exported.secret,
				...(exported.description ? { description: exported.description } : {}),
			});
		}
	}
	for (const variable of [...input.shared].sort((a, b) =>
		a.key.localeCompare(b.key),
	)) {
		offered.push({
			name: variable.key,
			value: referenceFor(SHARED_NAMESPACE, variable.key),
			source: "shared",
			from: variable.layer ?? SHARED_NAMESPACE,
			...(variable.secret ? { secret: true } : {}),
		});
	}
	for (const variable of [...(input.team ?? [])].sort((a, b) =>
		a.key.localeCompare(b.key),
	)) {
		offered.push({
			name: variable.key,
			value: referenceFor(TEAM_NAMESPACE, variable.key),
			source: "team",
			from: TEAM_NAMESPACE,
			...(variable.secret ? { secret: true } : {}),
		});
	}
	const referenced = new Set(offered.map((entry) => entry.name));
	for (const entry of [...input.envExample].sort((a, b) =>
		a.key.localeCompare(b.key),
	)) {
		// A reference already fills this name, and with a better value.
		if (referenced.has(entry.key)) continue;
		offered.push({
			name: entry.key,
			...(entry.value && !entry.needsInput ? { value: entry.value } : {}),
			source: "env-example",
			from: ".env.example",
			...(entry.proposedSecret ? { secret: true } : {}),
		});
	}
	const named = new Set(offered.map((entry) => entry.name));
	for (const name of COMMON_NAMES[input.serviceKind]) {
		if (named.has(name)) continue;
		offered.push({ name, source: "common" });
	}

	const rows: Row[] = [];
	offered.forEach((suggestion, at) => {
		const byName = matchName(suggestion.name, needle);
		let score: number;
		let match: NameSuggestion["match"];
		if (byName) {
			score = byName.score;
			if (needle !== "") {
				match = { start: byName.start, end: byName.start + needle.length };
			}
		} else if (
			// "pos" finds what postgres offers.
			suggestion.from !== undefined &&
			suggestion.source !== "env-example" &&
			suggestion.from.toLowerCase().includes(needle.toLowerCase())
		) {
			score = 3;
		} else {
			return;
		}
		const blocked =
			reserved.has(suggestion.name) || isReservedKey(suggestion.name)
				? REASON_PLATFORM
				: existing.has(suggestion.name)
					? REASON_EXISTING
					: null;
		rows.push({
			suggestion: {
				...suggestion,
				...(match ? { match } : {}),
				...(blocked ? { disabled: true, reason: blocked } : {}),
			},
			score,
			at,
		});
	});

	// Keys that cannot be used, listed only once the text points at them.
	if (needle !== "") {
		const listed = new Set(rows.map((row) => row.suggestion.name));
		const blockedKeys: [string, NameSource, string][] = [
			...[...reserved].map((key): [string, NameSource, string] => [
				key,
				"platform",
				REASON_PLATFORM,
			]),
			...input.existingKeys.map((key): [string, NameSource, string] => [
				key,
				"existing",
				REASON_EXISTING,
			]),
		];
		for (const [key, source, reason] of blockedKeys) {
			if (listed.has(key)) continue;
			const found = matchName(key, needle);
			if (!found) continue;
			listed.add(key);
			rows.push({
				suggestion: {
					name: key,
					source,
					disabled: true,
					reason,
					match: { start: found.start, end: found.start + needle.length },
				},
				score: found.score,
				at: offered.length + rows.length,
			});
		}
	}

	rows.sort(
		(a, b) =>
			Number(a.suggestion.disabled === true) -
				Number(b.suggestion.disabled === true) ||
			a.score - b.score ||
			SOURCE_ORDER[a.suggestion.source] - SOURCE_ORDER[b.suggestion.source] ||
			a.at - b.at,
	);
	return rows.slice(0, input.limit ?? 30).map((row) => row.suggestion);
}
