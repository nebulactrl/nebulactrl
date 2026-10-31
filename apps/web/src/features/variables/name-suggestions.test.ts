import { describe, expect, test } from "bun:test";
import {
	isReservedKey,
	type NameSuggestionInput,
	REASON_EXISTING,
	REASON_PLATFORM,
	suggestNames,
} from "./name-suggestions";
import { REFERENCE_OPEN } from "./reference-autocomplete";

// The product's own reference syntax, kept out of template literals' way.
const OPEN = "$" + "{{";

const base: NameSuggestionInput = {
	query: "",
	serviceKind: "app",
	services: [
		{
			slug: "postgres",
			name: "Postgres",
			exports: [
				{ key: "DATABASE_URL", description: "Connection string", secret: true },
				{ key: "DATABASE_HOST", secret: false },
			],
		},
		{
			slug: "redis",
			name: "Redis",
			exports: [{ key: "REDIS_URL", secret: true }],
		},
	],
	shared: [
		{ key: "SENTRY_DSN", layer: "project", secret: true },
		{ key: "REGION", layer: "environment" },
	],
	envExample: [
		{ key: "STRIPE_KEY", proposedSecret: true, needsInput: true },
		{ key: "APP_NAME", value: "demo" },
		{ key: "DATABASE_URL", needsInput: true },
	],
	existingKeys: ["LOG_LEVEL"],
};

const names = (input: Partial<NameSuggestionInput>) =>
	suggestNames({ ...base, ...input }).map((entry) => entry.name);

describe("suggestNames: exports", () => {
	test("typing `data` suggests DATABASE_URL from postgres with its reference", () => {
		const [first] = suggestNames({ ...base, query: "data" });
		expect(first).toMatchObject({
			name: "DATABASE_HOST",
			source: "export",
			from: "postgres",
		});
		const url = suggestNames({ ...base, query: "data" }).find(
			(entry) => entry.name === "DATABASE_URL",
		);
		expect(url).toMatchObject({
			source: "export",
			from: "postgres",
			value: `${OPEN}postgres.DATABASE_URL}}`,
			secret: true,
			description: "Connection string",
			match: { start: 0, end: 4 },
		});
	});

	test("an export beats the .env.example and common entries of the same name", () => {
		const found = suggestNames({ ...base, query: "DATABASE_URL" }).filter(
			(entry) => entry.name === "DATABASE_URL",
		);
		expect(found).toHaveLength(1);
		expect(found[0]?.source).toBe("export");
	});

	test("the service it comes from is searchable too", () => {
		expect(names({ query: "post" })).toEqual(["DATABASE_HOST", "DATABASE_URL"]);
	});

	test("two services exporting one name are both offered", () => {
		const found = suggestNames({
			...base,
			query: "URL",
			services: [
				{ slug: "a", name: "A", exports: [{ key: "URL", secret: false }] },
				{ slug: "b", name: "B", exports: [{ key: "URL", secret: false }] },
			],
		}).filter((entry) => entry.source === "export");
		expect(found.map((entry) => entry.from)).toEqual(["a", "b"]);
	});
});

describe("suggestNames: other sources", () => {
	test("a shared variable fills the name and the shared reference", () => {
		const [region] = suggestNames({ ...base, query: "reg" });
		expect(region).toMatchObject({
			name: "REGION",
			source: "shared",
			from: "environment",
			value: `${OPEN}shared.REGION}}`,
		});
	});

	test("a team variable fills the name and the team reference, after shared", () => {
		const entries = suggestNames({
			...base,
			query: "reg",
			team: [{ key: "REGION_TEAM", secret: true }],
		});
		expect(entries.map((e) => e.source)).toEqual(["shared", "team"]);
		expect(entries[1]).toMatchObject({
			name: "REGION_TEAM",
			source: "team",
			from: "team",
			secret: true,
			value: `${OPEN}team.REGION_TEAM}}`,
		});
	});

	test(".env.example keys fill the name, and the value only when proposed", () => {
		const entries = suggestNames({ ...base, query: "app_" });
		expect(entries[0]).toMatchObject({
			name: "APP_NAME",
			source: "env-example",
			value: "demo",
		});
		const stripe = suggestNames({ ...base, query: "stripe" })[0];
		expect(stripe?.source).toBe("env-example");
		expect(stripe?.value).toBeUndefined();
		expect(stripe?.secret).toBe(true);
	});

	test("common names follow the kind of service", () => {
		expect(names({ query: "jwt" })).toEqual(["JWT_SECRET"]);
		expect(names({ query: "jwt", serviceKind: "database" })).toEqual([]);
		expect(names({ query: "tz", serviceKind: "route" })).toEqual([]);
	});

	test("PORT is not a common name: it only shows up disabled", () => {
		const port = suggestNames({ ...base, query: "port" }).find(
			(entry) => entry.name === "PORT",
		);
		expect(port).toMatchObject({
			disabled: true,
			reason: REASON_PLATFORM,
			source: "platform",
		});
	});
});

describe("suggestNames: ranking", () => {
	test("a prefix beats a word start, which beats a substring", () => {
		const result = suggestNames({
			...base,
			query: "url",
			services: [],
			shared: [],
			envExample: [{ key: "MYURL" }, { key: "URLS" }, { key: "API_URL" }],
			serviceKind: "route",
		});
		expect(
			result.filter((entry) => !entry.disabled).map((entry) => entry.name),
		).toEqual(["URLS", "API_URL", "MYURL"]);
	});

	test("at equal match, sources come in order export, shared, .env.example, common", () => {
		const result = suggestNames({
			...base,
			query: "x",
			services: [
				{ slug: "s", name: "S", exports: [{ key: "X1", secret: false }] },
			],
			shared: [{ key: "X2" }],
			envExample: [{ key: "X3" }],
			existingKeys: [],
		});
		expect(result.map((entry) => entry.source)).toEqual([
			"export",
			"shared",
			"env-example",
		]);
	});

	test("matching ignores case and treats - and blanks as underscores", () => {
		expect(names({ query: "database-u" })).toContain("DATABASE_URL");
		expect(names({ query: " session s " })).toContain("SESSION_SECRET");
	});

	test("an empty query lists every usable offer, capped by the limit", () => {
		const all = suggestNames(base);
		expect(all.length).toBeGreaterThan(5);
		// An offer whose name is already set stays, disabled and last.
		const disabled = all.filter((entry) => entry.disabled);
		expect(disabled.map((entry) => entry.name)).toEqual(["LOG_LEVEL"]);
		expect(all.at(-1)?.name).toBe("LOG_LEVEL");
		expect(suggestNames({ ...base, limit: 3 })).toHaveLength(3);
		// Nothing already set or reserved is dragged in without a typed text.
		expect(all.some((entry) => entry.source === "platform")).toBe(false);
		expect(all.some((entry) => entry.source === "existing")).toBe(false);
	});

	test("a query nothing matches gives an empty list", () => {
		expect(names({ query: "zzzz" })).toEqual([]);
	});
});

describe("suggestNames: disabled entries", () => {
	test("a key already set is shown disabled with the reason, after usable ones", () => {
		const result = suggestNames({ ...base, query: "log" });
		expect(result).toHaveLength(1);
		expect(result[0]).toMatchObject({
			name: "LOG_LEVEL",
			disabled: true,
			reason: REASON_EXISTING,
		});
	});

	test("an offer whose name is already set is disabled, not hidden", () => {
		const result = suggestNames({
			...base,
			query: "database_u",
			existingKeys: ["DATABASE_URL"],
		});
		expect(result[0]).toMatchObject({
			name: "DATABASE_URL",
			disabled: true,
			reason: REASON_EXISTING,
			source: "export",
		});
	});

	test("disabled entries sort last whatever their match", () => {
		const result = suggestNames({
			...base,
			query: "log",
			existingKeys: ["LOG_LEVEL", "CATALOG"],
		});
		expect(result.map((entry) => entry.name)).toEqual(["LOG_LEVEL", "CATALOG"]);
		const mixed = suggestNames({
			...base,
			query: "l",
			existingKeys: ["LOG_LEVEL"],
		});
		const firstDisabled = mixed.findIndex((entry) => entry.disabled);
		expect(mixed.slice(firstDisabled).every((entry) => entry.disabled)).toBe(
			true,
		);
	});

	test("platform keys are listed disabled once the text points at them", () => {
		const result = suggestNames({ ...base, query: "nebula_priv" });
		expect(result).toEqual([
			expect.objectContaining({
				name: "NEBULA_PRIVATE_HOST",
				disabled: true,
				reason: REASON_PLATFORM,
			}),
		]);
	});

	test("an .env.example key that is reserved is disabled", () => {
		const result = suggestNames({
			...base,
			query: "nebula_x",
			envExample: [{ key: "NEBULA_X" }],
		});
		expect(result[0]).toMatchObject({ name: "NEBULA_X", disabled: true });
	});
});

describe("suggestNames: references", () => {
	test("a reference typed into the name suggests nothing", () => {
		expect(names({ query: `${REFERENCE_OPEN}pos` })).toEqual([]);
		expect(names({ query: `DB_${REFERENCE_OPEN}` })).toEqual([]);
	});
});

describe("isReservedKey", () => {
	test("PORT and NEBULA_* are reserved, nothing else", () => {
		expect(isReservedKey("PORT")).toBe(true);
		expect(isReservedKey("NEBULA_ANYTHING")).toBe(true);
		expect(isReservedKey("nebula_lower")).toBe(true);
		expect(isReservedKey("PORTAL")).toBe(false);
		expect(isReservedKey("MY_NEBULA_KEY")).toBe(false);
	});
});
