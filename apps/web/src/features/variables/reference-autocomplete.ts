/**
 * Pure logic behind the `${{service.KEY}}` autocomplete: find the open token
 * before the caret, build the candidates, filter them, and apply one. The
 * React side lives in `reference-suggestions.tsx`.
 *
 * What `${{` can offer: other services' variables, their exports (a
 * database's DATABASE_URL) and platform variables as `slug.KEY`; shared
 * variables as `shared.KEY`; the team's variables as `team.KEY`; the secret
 * managers as `vault:<name>/<path>#<key>` (the name is offered, the path and
 * key are typed); this service's own keys as a bare `KEY`; and the generator
 * and transform functions.
 */

import { FUNCTION_SNIPPETS } from "./generator";

// A reference to another service's variable. It is this product's own
// syntax, not a JavaScript template.
export const REFERENCE_OPEN = "${{";
const REFERENCE_CLOSE = "}}";

/** What the platform injects into every service, in display order. */
export const PLATFORM_REFERENCE_KEYS = [
	"NEBULA_PRIVATE_HOST",
	"PORT",
	"NEBULA_PUBLIC_URL",
	"NEBULA_SERVICE_NAME",
	"NEBULA_ENVIRONMENT",
	"NEBULA_PUBLIC_DOMAIN",
	"NEBULA_PROJECT_NAME",
	"NEBULA_SERVICE_ID",
	"NEBULA_ENVIRONMENT_NAME",
] as const;

/** The namespace of shared variables in `${{shared.KEY}}`. */
export const SHARED_NAMESPACE = "shared";

/** The namespace of team variables in `${{team.KEY}}`. */
export const TEAM_NAMESPACE = "team";

/** The scheme of a secret manager reference: `${{vault:<name>/<path>#<key>}}`. */
const VAULT_SCHEME = "vault";

/** The scheme of a Doppler reference: `${{doppler:<name>[/<project>/<config>]#<key>}}`. */
const DOPPLER_SCHEME = "doppler";

/**
 * What may sit between `${{` and the caret: optional blanks, then a partial
 * `slug.KEY` or `vault:<name>/<path>#<key>`.
 */
const PARTIAL = /^[ \t]*[A-Za-z0-9_.:/#@~+=-]*$/;
/** What an existing, half-typed reference leaves after the caret. */
const TRAILING = /^[A-Za-z0-9_.-]*[ \t]*\}\}/;

/** An unclosed `${{` with the caret inside it. */
export interface ReferenceToken {
	/** Index of the `$` of `${{`. */
	start: number;
	/** The caret: where the typed partial ends. */
	end: number;
	/** The typed partial, e.g. `postgres.NEB`; may be empty. */
	query: string;
}

/** One other service in the environment, with the keys of its variables. */
export interface ReferenceService {
	id: string;
	slug: string;
	name: string;
	/** User-defined variables: names only, never values. */
	variables: readonly { key: string; isSecret: boolean }[];
	/** Virtual variables the service offers (a database's DATABASE_URL). */
	exports?: readonly { key: string; description?: string; secret: boolean }[];
}

type CandidateKind =
	| "variable"
	| "platform"
	| "export"
	| "shared"
	| "team"
	| "vault"
	| "own"
	| "function";

export interface ReferenceCandidate {
	/** Service slug, `shared`, `team`, `vault`, or "" for this service's own keys and functions. */
	slug: string;
	serviceName: string;
	key: string;
	/** The value is or contains a secret (declared or derived). */
	isSecret: boolean;
	/** Provided by the platform rather than defined by the user. */
	platform: boolean;
	kind: CandidateKind;
	description?: string;
	/** Shared variables: where the value lives. */
	layer?: "project" | "environment" | "team";
	/**
	 * What picking it inserts: the full reference `${{slug.KEY}}`, `${{KEY}}` or
	 * `${{fn(...)}}`; for a secret manager only the open `${{vault:<name>/`, the
	 * path and key are typed.
	 */
	insert: string;
}

/** What `buildCandidates` offers besides the other services. */
export interface ExtraCandidates {
	/** This service's own variables: referenced as a bare `${{KEY}}`. */
	own?: readonly { key: string; isSecret: boolean }[];
	shared?: readonly {
		key: string;
		isSecret: boolean;
		layer: "project" | "environment" | "team";
	}[];
	/** The team's variables: `${{team.KEY}}`. */
	team?: readonly { key: string; isSecret: boolean }[];
	/** The team's secret managers: `${{vault:<name>/…}}`. */
	vault?: readonly { name: string; kind: string }[];
	/** Offer the generator and transform functions. */
	functions?: boolean;
}

/**
 * The open `${{` the caret sits in, or null. The token closes as soon as a
 * `}}` (or anything that cannot be part of a `slug.KEY`) comes between the
 * `${{` and the caret.
 */
export function findOpenReference(
	text: string,
	caret: number,
): ReferenceToken | null {
	if (caret < REFERENCE_OPEN.length || caret > text.length) return null;
	const start = text.lastIndexOf(REFERENCE_OPEN, caret - REFERENCE_OPEN.length);
	if (start < 0) return null;
	const query = text.slice(start + REFERENCE_OPEN.length, caret);
	if (!PARTIAL.test(query)) return null;
	return { start, end: caret, query };
}

export function referenceFor(slug: string, key: string): string {
	return slug === ""
		? `${REFERENCE_OPEN}${key}${REFERENCE_CLOSE}`
		: `${REFERENCE_OPEN}${slug}.${key}${REFERENCE_CLOSE}`;
}

/**
 * Every reference worth offering: this service's own keys, shared variables,
 * then for each other service its variables (sorted), exports and platform
 * keys, and last the functions. The service being edited is left out of the
 * services, since a service referencing itself by slug is the bare form.
 */
export function buildCandidates(
	services: readonly ReferenceService[],
	excludeServiceId: string,
	extra: ExtraCandidates = {},
): ReferenceCandidate[] {
	const out: ReferenceCandidate[] = [];
	for (const variable of [...(extra.own ?? [])].sort((a, b) =>
		a.key.localeCompare(b.key),
	)) {
		out.push({
			slug: "",
			serviceName: "this service",
			key: variable.key,
			isSecret: variable.isSecret,
			platform: false,
			kind: "own",
			insert: referenceFor("", variable.key),
		});
	}
	for (const variable of [...(extra.shared ?? [])].sort((a, b) =>
		a.key.localeCompare(b.key),
	)) {
		out.push({
			slug: SHARED_NAMESPACE,
			serviceName: "shared variables",
			key: variable.key,
			isSecret: variable.isSecret,
			platform: false,
			kind: "shared",
			layer: variable.layer,
			insert: referenceFor(SHARED_NAMESPACE, variable.key),
		});
	}
	for (const variable of [...(extra.team ?? [])].sort((a, b) =>
		a.key.localeCompare(b.key),
	)) {
		out.push({
			slug: TEAM_NAMESPACE,
			serviceName: "team variables",
			key: variable.key,
			isSecret: variable.isSecret,
			platform: false,
			kind: "team",
			layer: "team",
			insert: referenceFor(TEAM_NAMESPACE, variable.key),
		});
	}
	for (const manager of [...(extra.vault ?? [])].sort((a, b) =>
		a.name.localeCompare(b.name),
	)) {
		const doppler = manager.kind === DOPPLER_SCHEME;
		out.push({
			slug: doppler ? DOPPLER_SCHEME : VAULT_SCHEME,
			serviceName: "secret managers",
			key: manager.name,
			isSecret: false,
			platform: false,
			kind: "vault",
			description: doppler
				? `a secret in ${manager.name}: add /project/config unless a default or service token fixes them, then #NAME`
				: `a secret in ${manager.name}: type the path, then #key`,
			// Doppler's project and config are optional, so the open reference stops at the name.
			insert: `${REFERENCE_OPEN}${doppler ? DOPPLER_SCHEME : VAULT_SCHEME}:${manager.name}${doppler ? "" : "/"}`,
		});
	}
	const others = services
		.filter((service) => service.id !== excludeServiceId)
		.sort((a, b) => a.slug.localeCompare(b.slug));
	for (const service of others) {
		const platform = new Set<string>(PLATFORM_REFERENCE_KEYS);
		const exportKeys = new Set((service.exports ?? []).map((e) => e.key));
		const own = service.variables
			.filter(
				(variable) =>
					!platform.has(variable.key) && !exportKeys.has(variable.key),
			)
			.sort((a, b) => a.key.localeCompare(b.key));
		for (const variable of own) {
			out.push({
				slug: service.slug,
				serviceName: service.name,
				key: variable.key,
				isSecret: variable.isSecret,
				platform: false,
				kind: "variable",
				insert: referenceFor(service.slug, variable.key),
			});
		}
		for (const exported of [...(service.exports ?? [])].sort((a, b) =>
			a.key.localeCompare(b.key),
		)) {
			out.push({
				slug: service.slug,
				serviceName: service.name,
				key: exported.key,
				isSecret: exported.secret,
				platform: false,
				kind: "export",
				...(exported.description ? { description: exported.description } : {}),
				insert: referenceFor(service.slug, exported.key),
			});
		}
		for (const key of PLATFORM_REFERENCE_KEYS) {
			out.push({
				slug: service.slug,
				serviceName: service.name,
				key,
				isSecret: false,
				platform: true,
				kind: "platform",
				insert: referenceFor(service.slug, key),
			});
		}
	}
	if (extra.functions) {
		for (const fn of FUNCTION_SNIPPETS) {
			out.push({
				slug: "",
				serviceName: "functions",
				key: fn.name,
				isSecret: false,
				platform: false,
				kind: "function",
				description: fn.description,
				insert: `${REFERENCE_OPEN}${fn.insert}${REFERENCE_CLOSE}`,
			});
		}
	}
	return out;
}

/** 0 when `haystack` starts with `needle`, 1 when it contains it, -1 otherwise. */
function rank(haystack: string, needle: string): number {
	if (needle === "") return 0;
	const index = haystack.toLowerCase().indexOf(needle.toLowerCase());
	if (index < 0) return -1;
	return index === 0 ? 0 : 1;
}

/**
 * The candidates a typed partial leaves. Without a dot the partial narrows
 * the services (by slug or name); with one, the part before it picks the
 * service and the part after it narrows its keys. Better matches come first;
 * the candidates' own order breaks ties.
 */
export function filterCandidates(
	candidates: readonly ReferenceCandidate[],
	query: string,
	limit = 80,
): ReferenceCandidate[] {
	const trimmed = query.trim();
	// `vault:` / `doppler:` pick a secret manager by name; once the path starts, the person types on.
	const scheme = /^(vault|doppler):/i.exec(trimmed)?.[1]?.toLowerCase();
	if (scheme) {
		const rest = trimmed.slice(scheme.length + 1);
		if (rest.includes("/") || rest.includes("#")) return [];
		return candidates
			.map((candidate, at) => ({
				candidate,
				at,
				score: rank(candidate.key, rest),
			}))
			.filter(
				(entry) =>
					entry.candidate.kind === "vault" &&
					entry.candidate.slug === scheme &&
					entry.score >= 0,
			)
			.sort((a, b) => a.score - b.score || a.at - b.at)
			.slice(0, limit)
			.map((entry) => entry.candidate);
	}
	const dot = trimmed.indexOf(".");
	const servicePart = dot < 0 ? trimmed : trimmed.slice(0, dot);
	const keyPart = dot < 0 ? "" : trimmed.slice(dot + 1);

	const scored: { candidate: ReferenceCandidate; score: number; at: number }[] =
		[];
	candidates.forEach((candidate, at) => {
		// Bare forms (own keys, functions) are matched on the key alone.
		if (candidate.kind === "own" || candidate.kind === "function") {
			if (dot >= 0) return;
			const bare = rank(candidate.key, servicePart);
			if (bare >= 0) scored.push({ candidate, score: bare * 2, at });
			return;
		}
		let serviceScore: number;
		if (dot >= 0) {
			// "slug." has chosen its service: only that one stays.
			if (candidate.slug.toLowerCase() !== servicePart.toLowerCase()) return;
			serviceScore = 0;
		} else {
			const bySlug = rank(candidate.slug, servicePart);
			const byName = rank(candidate.serviceName, servicePart);
			serviceScore =
				bySlug < 0 ? byName : byName < 0 ? bySlug : Math.min(bySlug, byName);
		}
		if (serviceScore < 0) return;
		const keyScore = rank(candidate.key, keyPart);
		if (keyScore < 0) return;
		scored.push({ candidate, score: serviceScore * 2 + keyScore, at });
	});
	scored.sort((a, b) => a.score - b.score || a.at - b.at);
	return scored.slice(0, limit).map((entry) => entry.candidate);
}

/**
 * Replaces the typed partial (and a `}}` already waiting after the caret)
 * with the whole reference, and says where the caret goes: right after it.
 */
export function applyCompletion(
	text: string,
	token: ReferenceToken,
	candidate: ReferenceCandidate,
): { text: string; caret: number } {
	const tail = text.slice(token.end);
	const consumed = TRAILING.exec(tail)?.[0].length ?? 0;
	return {
		text:
			text.slice(0, token.start) +
			candidate.insert +
			text.slice(token.end + consumed),
		caret: token.start + candidate.insert.length,
	};
}
