import { describe, expect, test } from "bun:test";
import {
	createIndex,
	packSearch,
	searchDocs,
	tokenize,
	unpackSearch,
} from "./search";
import type { SearchRecord } from "./types";

const records: SearchRecord[] = [
	[
		"operations/variables",
		"",
		"Variables and secrets",
		"",
		"Applications",
		"Encrypted at rest. Reference another service.",
	],
	[
		"operations/variables",
		"syntax",
		"Variables and secrets",
		"Syntax",
		"Applications",
		"A value is plain text with expressions.",
	],
	[
		"operations/backups",
		"",
		"Backups",
		"",
		"Databases and backups",
		"Schedule database backups to an S3 bucket.",
	],
	[
		"operations/backups",
		"destinations",
		"Backups",
		"Destinations",
		"Databases and backups",
		"Add an S3 destination. Variables are not needed.",
	],
	[
		"security/sso-setup",
		"",
		"Single sign-on",
		"",
		"Teams and security",
		"Connect your OpenID Connect identity provider.",
	],
];

const index = createIndex(records);

describe("tokenize", () => {
	test("lower-cases and splits on punctuation", () => {
		expect(tokenize("DNS-01 wildcard (S3)")).toEqual([
			"dns",
			"01",
			"wildcard",
			"s3",
		]);
	});
});

describe("searchDocs", () => {
	test("a title match outranks a body match", () => {
		const hits = searchDocs(index, "variables");
		expect(hits[0]?.slug).toBe("operations/variables");
		expect(hits.map((h) => h.slug)).toContain("operations/backups");
	});

	test("matches word prefixes and requires every term", () => {
		expect(searchDocs(index, "backu s3").map((h) => h.slug)).toEqual([
			"operations/backups",
			"operations/backups",
		]);
		expect(searchDocs(index, "backups kubernetes")).toEqual([]);
	});

	test("a heading hit carries its anchor and a snippet", () => {
		const hit = searchDocs(index, "destinations")[0];
		expect(hit).toMatchObject({
			slug: "operations/backups",
			anchor: "destinations",
			heading: "Destinations",
		});
		expect(hit?.snippet).toContain("S3 destination");
	});

	test("limits hits per page and overall", () => {
		expect(searchDocs(index, "s3", { limit: 1 })).toHaveLength(1);
		expect(
			searchDocs(index, "variables", { perPage: 1 }).filter(
				(h) => h.slug === "operations/variables",
			),
		).toHaveLength(1);
	});

	test("an empty or symbol-only query finds nothing", () => {
		expect(searchDocs(index, "")).toEqual([]);
		expect(searchDocs(index, " -- ")).toEqual([]);
	});
});

describe("packSearch", () => {
	test("stores each page once and round-trips", () => {
		const packed = packSearch(records);
		expect(packed.pages).toHaveLength(3);
		expect(packed.rows).toHaveLength(records.length);
		expect(unpackSearch(packed)).toEqual(records);
	});
});
