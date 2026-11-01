import { describe, expect, test } from "bun:test";
import { ENGINE_CATALOG } from "#/features/databases/engines.fixture";
import {
	capacityOf,
	parsePort,
	replicasEligible,
	suggestName,
	visibleEntries,
} from "./lib";

describe("suggestName", () => {
	test("takes an image's repository name without registry, tag or digest", () => {
		expect(
			suggestName({
				kind: "image",
				image: "traefik/whoami:latest",
				gitUrl: "",
			}),
		).toBe("whoami");
		expect(
			suggestName({
				kind: "image",
				image: "ghcr.io/acme/api-gateway@sha256:abc",
				gitUrl: "",
			}),
		).toBe("api-gateway");
		expect(
			suggestName({
				kind: "image",
				image: "localhost:5000/web:1.2",
				gitUrl: "",
			}),
		).toBe("web");
		expect(
			suggestName({ kind: "image", image: "postgres:17", gitUrl: "" }),
		).toBe("postgres");
	});
	test("takes a repository's name without .git or trailing slash", () => {
		expect(
			suggestName({
				kind: "git",
				image: "",
				gitUrl: "https://github.com/acme/Shop.git",
			}),
		).toBe("shop");
		expect(
			suggestName({
				kind: "git",
				image: "",
				gitUrl: "https://github.com/acme/shop/",
			}),
		).toBe("shop");
		expect(
			suggestName({ kind: "git", image: "", gitUrl: "git://host/app.git?x=1" }),
		).toBe("app");
	});
	test("is empty until there is a source", () => {
		expect(suggestName({ kind: "image", image: "  ", gitUrl: "x" })).toBe("");
		expect(suggestName({ kind: "git", image: "x", gitUrl: "" })).toBe("");
	});
});

describe("capacityOf", () => {
	test("is null before the first heartbeat sample", () => {
		expect(capacityOf({ latest: null })).toBeNull();
	});
	test("turns a sample into percentages, clamped", () => {
		const sample = {
			cpuPercent: 140,
			memUsed: 2,
			memTotal: 8,
			diskUsed: 0,
			diskTotal: 0,
			sampledAt: "2026-01-01T00:00:00Z",
		};
		expect(capacityOf({ latest: sample })).toEqual({
			cpu: 100,
			mem: 25,
			disk: null,
		});
	});
});

describe("parsePort", () => {
	test("empty is undefined, a valid port is a number, anything else NaN", () => {
		expect(parsePort("  ")).toBeUndefined();
		expect(parsePort("8080")).toBe(8080);
		expect(Number.isNaN(parsePort("0"))).toBe(true);
		expect(Number.isNaN(parsePort("70000"))).toBe(true);
		expect(Number.isNaN(parsePort("80.5"))).toBe(true);
	});
});

describe("replicasEligible", () => {
	test("needs a port", () => {
		expect(replicasEligible({ port: 80 })).toBe(true);
		expect(replicasEligible({ port: undefined })).toBe(false);
		expect(replicasEligible({ port: Number.NaN })).toBe(false);
	});
});

const ids = (query: string, submenu: "database" | null = null) =>
	visibleEntries(query, submenu, ENGINE_CATALOG).map((entry) => entry.id);

describe("visibleEntries", () => {
	test("the root list hides engines behind Database", () => {
		const root = ids("");
		expect(root).toContain("database");
		expect(root.some((id) => id.startsWith("engine:"))).toBe(false);
	});
	test("inside Database it lists the catalog's engines, Valkey first", () => {
		expect(ids("", "database")).toEqual([
			"engine:valkey",
			"engine:postgres",
			"engine:mysql",
			"engine:mariadb",
			"engine:redis",
			"engine:mongodb",
			"engine:clickhouse",
		]);
	});
	test("Database has no engines until the catalog loads", () => {
		expect(visibleEntries("", "database", [])).toEqual([]);
		expect(visibleEntries("post", null, [])).toEqual([]);
	});
	test("a query reaches engines directly and drops the redundant parent", () => {
		expect(ids("post")).toEqual(["engine:postgres"]);
		expect(ids("sql")).toEqual(
			expect.arrayContaining([
				"engine:postgres",
				"engine:mysql",
				"engine:mariadb",
			]),
		);
	});
	test("key-value engines answer valkey, redis, cache and kv", () => {
		expect(ids("valkey")).toEqual(["engine:valkey"]);
		// Valkey describes itself as Redis-compatible.
		expect(ids("redis")).toEqual(["engine:valkey", "engine:redis"]);
		expect(ids("cache")).toEqual(["engine:valkey", "engine:redis"]);
		expect(ids("kv")).toEqual(["engine:valkey", "engine:redis"]);
	});
	test("document, analytics and PostGIS searches", () => {
		expect(ids("mongo")).toEqual(["engine:mongodb"]);
		expect(ids("nosql")).toEqual(["engine:mongodb"]);
		expect(ids("analytics")).toEqual(["engine:clickhouse"]);
		expect(ids("postgis")).toEqual(["engine:postgres"]);
	});
	test("source-available engines carry a licence chip", () => {
		const chips = visibleEntries("", "database", ENGINE_CATALOG)
			.filter((entry) => entry.chip)
			.map((entry) => entry.id);
		expect(chips).toEqual(["engine:redis", "engine:mongodb"]);
	});
	test("engine entries carry the catalog id for the dialog", () => {
		const entry = visibleEntries("mongo", null, ENGINE_CATALOG)[0];
		expect(entry?.engine).toBe("mongodb");
	});
	test("Database itself still matches its own name", () => {
		expect(ids("database")).toContain("database");
	});
	test("matches synonyms", () => {
		expect(ids("github")).toEqual(["git"]);
		expect(ids("nothing-here")).toEqual([]);
	});
});
