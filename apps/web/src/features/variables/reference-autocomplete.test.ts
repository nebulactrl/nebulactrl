import { describe, expect, test } from "bun:test";
import {
	applyCompletion,
	buildCandidates,
	filterCandidates,
	findOpenReference,
	PLATFORM_REFERENCE_KEYS,
	type ReferenceService,
	referenceFor,
} from "./reference-autocomplete";

// The product's own reference syntax, kept out of template literals' way.
const OPEN = "$" + "{{";

const services: ReferenceService[] = [
	{
		id: "svc-web",
		slug: "web",
		name: "Web",
		variables: [{ key: "SELF", isSecret: false }],
	},
	{
		id: "svc-pg",
		slug: "postgres",
		name: "Postgres",
		variables: [
			{ key: "POSTGRES_PASSWORD", isSecret: true },
			{ key: "DB_NAME", isSecret: false },
		],
	},
	{ id: "svc-redis", slug: "redis", name: "Redis Cache", variables: [] },
];

describe("findOpenReference", () => {
	test("finds an empty open token right after the brackets", () => {
		const text = `URL=${OPEN}`;
		expect(findOpenReference(text, text.length)).toEqual({
			start: 4,
			end: text.length,
			query: "",
		});
	});

	test("returns the typed partial up to the caret", () => {
		const text = `${OPEN}postgres.DB_NA`;
		expect(findOpenReference(text, text.length)?.query).toBe("postgres.DB_NA");
	});

	test("uses the caret, not the end of the text", () => {
		const text = `${OPEN}pos}} tail`;
		expect(findOpenReference(text, 6)).toEqual({
			start: 0,
			end: 6,
			query: "pos",
		});
	});

	test("is closed once a }} sits between the brackets and the caret", () => {
		const text = `${OPEN}postgres.HOST}} and `;
		expect(findOpenReference(text, text.length)).toBeNull();
	});

	test("is closed when the text has no brackets or the caret is before them", () => {
		expect(findOpenReference("plain", 5)).toBeNull();
		expect(findOpenReference(`a ${OPEN}x`, 1)).toBeNull();
		expect(findOpenReference("$", 1)).toBeNull();
	});

	test("picks the latest open token", () => {
		const text = `${OPEN}a.B}} ${OPEN}re`;
		expect(findOpenReference(text, text.length)).toEqual({
			start: 9,
			end: text.length,
			query: "re",
		});
	});

	test("rejects partials that cannot be a reference", () => {
		for (const text of [`${OPEN}a b`, `${OPEN}a\nb`, `${OPEN}a?b`]) {
			expect(findOpenReference(text, text.length)).toBeNull();
		}
	});

	test("tolerates blanks right after the brackets", () => {
		const text = `${OPEN} red`;
		expect(findOpenReference(text, text.length)?.query).toBe(" red");
	});
});

describe("buildCandidates", () => {
	const candidates = buildCandidates(services, "svc-web");

	test("leaves the current service out", () => {
		expect(candidates.some((c) => c.slug === "web")).toBe(false);
	});

	test("lists own keys sorted, then the platform keys, per service by slug", () => {
		const keys = candidates
			.filter((c) => c.slug === "postgres")
			.map((c) => c.key);
		expect(keys).toEqual([
			"DB_NAME",
			"POSTGRES_PASSWORD",
			...PLATFORM_REFERENCE_KEYS,
		]);
		expect([...new Set(candidates.map((c) => c.slug))]).toEqual([
			"postgres",
			"redis",
		]);
	});

	test("builds the reference and flags secrets and platform keys", () => {
		const secret = candidates.find((c) => c.key === "POSTGRES_PASSWORD");
		expect(secret?.insert).toBe(`${OPEN}postgres.POSTGRES_PASSWORD}}`);
		expect(secret?.isSecret).toBe(true);
		expect(secret?.platform).toBe(false);
		const host = candidates.find(
			(c) => c.slug === "redis" && c.key === "NEBULA_PRIVATE_HOST",
		);
		expect(host?.platform).toBe(true);
		expect(host?.insert).toBe(referenceFor("redis", "NEBULA_PRIVATE_HOST"));
	});

	test("never lists a user key that shadows a platform key twice", () => {
		const list = buildCandidates(
			[
				{
					id: "x",
					slug: "x",
					name: "X",
					variables: [{ key: "PORT", isSecret: false }],
				},
			],
			"other",
		);
		expect(list.filter((c) => c.key === "PORT")).toHaveLength(1);
	});

	test("is empty when the current service is alone", () => {
		expect(buildCandidates(services.slice(0, 1), "svc-web")).toEqual([]);
	});
});

describe("filterCandidates", () => {
	const candidates = buildCandidates(services, "svc-web");

	test("an empty query keeps everything", () => {
		expect(filterCandidates(candidates, "")).toHaveLength(candidates.length);
	});

	test("a slug prefix narrows to that service", () => {
		const list = filterCandidates(candidates, "post");
		expect(list.length).toBeGreaterThan(0);
		expect(list.every((c) => c.slug === "postgres")).toBe(true);
	});

	test("matches the service name too, prefix first", () => {
		expect(
			filterCandidates(candidates, "cache").every((c) => c.slug === "redis"),
		).toBe(true);
		expect(filterCandidates(candidates, "re")[0]?.slug).toBe("redis");
	});

	test("slug. lists only that service", () => {
		const list = filterCandidates(candidates, "postgres.");
		expect(list).toHaveLength(2 + PLATFORM_REFERENCE_KEYS.length);
		expect(list.every((c) => c.slug === "postgres")).toBe(true);
	});

	test("slug.KE narrows the keys, case-insensitively, prefix first", () => {
		const list = filterCandidates(candidates, "postgres.po");
		expect(list.map((c) => c.key)).toEqual(["POSTGRES_PASSWORD", "PORT"]);
	});

	test("a partial slug with a dot does not pick a different service", () => {
		expect(filterCandidates(candidates, "post.")).toEqual([]);
	});

	test("no match gives nothing, and the limit caps the list", () => {
		expect(filterCandidates(candidates, "nope")).toEqual([]);
		expect(filterCandidates(candidates, "", 3)).toHaveLength(3);
	});
});

describe("applyCompletion", () => {
	const candidates = buildCandidates(services, "svc-web");
	const host = candidates.find(
		(c) => c.slug === "postgres" && c.key === "NEBULA_PRIVATE_HOST",
	);
	if (!host) throw new Error("fixture");

	test("replaces the typed partial and puts the caret after the reference", () => {
		const text = `postgres://${OPEN}post`;
		const token = findOpenReference(text, text.length);
		if (!token) throw new Error("no token");
		const result = applyCompletion(text, token, host);
		expect(result.text).toBe(`postgres://${host.insert}`);
		expect(result.caret).toBe(result.text.length);
	});

	test("keeps the text after the caret", () => {
		const text = `${OPEN}pg:5432/db`;
		const token = findOpenReference(text, 5);
		if (!token) throw new Error("no token");
		const result = applyCompletion(text, token, host);
		expect(result.text).toBe(`${host.insert}:5432/db`);
		expect(result.caret).toBe(host.insert.length);
	});

	test("swallows a }} that was already waiting after the caret", () => {
		const text = `a=${OPEN}pos}} z`;
		const token = findOpenReference(text, 8);
		if (!token) throw new Error("no token");
		const result = applyCompletion(text, token, host);
		expect(result.text).toBe(`a=${host.insert} z`);
		expect(result.caret).toBe(2 + host.insert.length);
	});

	test("swallows the rest of a half-typed word that closes right after", () => {
		const text = `${OPEN}postgres.HOST}}`;
		const token = findOpenReference(text, 5);
		if (!token) throw new Error("no token");
		expect(applyCompletion(text, token, host).text).toBe(host.insert);
	});
});

describe("team variables and secret managers", () => {
	const extra = {
		team: [
			{ key: "REGION", isSecret: false },
			{ key: "API_KEY", isSecret: true },
		],
		vault: [
			{ name: "bao", kind: "vault" },
			{ name: "infisical", kind: "infisical" },
		],
	};
	const all = buildCandidates(services, "svc-web", extra);

	test("team variables are team.KEY references, sorted", () => {
		const team = all.filter((c) => c.kind === "team");
		expect(team.map((c) => c.insert)).toEqual([
			`${OPEN}team.API_KEY}}`,
			`${OPEN}team.REGION}}`,
		]);
		expect(team[0]?.isSecret).toBe(true);
		expect(team[0]?.layer).toBe("team");
	});

	test("a secret manager inserts the open vault: prefix, not a whole reference", () => {
		const vault = all.filter((c) => c.kind === "vault");
		expect(vault.map((c) => c.insert)).toEqual([
			`${OPEN}vault:bao/`,
			`${OPEN}vault:infisical/`,
		]);
	});

	test("the vault: prefix picks managers by name and stops at the path", () => {
		expect(filterCandidates(all, "vault:").map((c) => c.key)).toEqual([
			"bao",
			"infisical",
		]);
		expect(filterCandidates(all, "vault:in").map((c) => c.key)).toEqual([
			"infisical",
		]);
		expect(filterCandidates(all, "vault:bao/apps/shop#pa")).toEqual([]);
	});

	test("typing vau lists the managers like any namespace", () => {
		expect(filterCandidates(all, "vau").map((c) => c.kind)).toEqual([
			"vault",
			"vault",
		]);
		expect(filterCandidates(all, "team.R").map((c) => c.key)).toEqual([
			"REGION",
		]);
	});

	test("the open token follows a half-typed secret reference", () => {
		const text = `PW=${OPEN}vault:bao/apps/shop/db#pass`;
		expect(findOpenReference(text, text.length)?.query).toBe(
			"vault:bao/apps/shop/db#pass",
		);
	});

	test("picking a manager keeps the caret inside the open reference", () => {
		const text = `PW=${OPEN}va`;
		const token = findOpenReference(text, text.length);
		const bao = all.find((c) => c.kind === "vault" && c.key === "bao");
		if (!token || !bao) throw new Error("fixture");
		const done = applyCompletion(text, token, bao);
		expect(done.text).toBe(`PW=${OPEN}vault:bao/`);
		expect(done.caret).toBe(done.text.length);
	});
});

describe("Doppler managers", () => {
	const all = buildCandidates(services, "svc-web", {
		vault: [
			{ name: "bao", kind: "vault" },
			{ name: "dp", kind: "doppler" },
		],
	});

	test("a Doppler manager inserts the open doppler: prefix without a slash", () => {
		const doppler = all.filter(
			(c) => c.kind === "vault" && c.slug === "doppler",
		);
		expect(doppler.map((c) => c.insert)).toEqual([`${OPEN}doppler:dp`]);
		expect(doppler[0]?.description).toContain("/project/config");
	});

	test("each scheme prefix lists only its own managers and stops at the path or key", () => {
		expect(filterCandidates(all, "doppler:").map((c) => c.key)).toEqual(["dp"]);
		expect(filterCandidates(all, "vault:").map((c) => c.key)).toEqual(["bao"]);
		expect(filterCandidates(all, "doppler:d").map((c) => c.key)).toEqual([
			"dp",
		]);
		expect(filterCandidates(all, "doppler:dp/backend/prd")).toEqual([]);
		expect(filterCandidates(all, "doppler:dp#KE")).toEqual([]);
	});

	test("picking a Doppler manager keeps the caret inside the open reference", () => {
		const text = `PW=${OPEN}dop`;
		const token = findOpenReference(text, text.length);
		const dp = all.find((c) => c.kind === "vault" && c.key === "dp");
		if (!token || !dp) throw new Error("fixture");
		const done = applyCompletion(text, token, dp);
		expect(done.text).toBe(`PW=${OPEN}doppler:dp`);
		expect(done.caret).toBe(done.text.length);
	});
});
