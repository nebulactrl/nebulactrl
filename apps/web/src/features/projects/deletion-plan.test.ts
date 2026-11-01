import { describe, expect, test } from "bun:test";
import {
	count,
	type DeletionPlan,
	describeDeletionPlan,
} from "./deletion-plan";

const EMPTY: DeletionPlan = {
	environments: [],
	services: 0,
	databases: 0,
	volumes: 0,
	domains: 0,
	previews: 0,
	statusPageComponents: 0,
	managedDnsRecords: 0,
};

describe("count", () => {
	test("pluralises by the number", () => {
		expect(count(0, "service")).toBe("0 services");
		expect(count(1, "service")).toBe("1 service");
		expect(count(2, "service")).toBe("2 services");
		expect(count(2, "entry", "entries")).toBe("2 entries");
	});
});

describe("describeDeletionPlan", () => {
	test("lists everything that goes, in reading order", () => {
		expect(
			describeDeletionPlan({
				environments: [
					{ name: "production", production: true },
					{ name: "staging", production: false },
				],
				services: 5,
				databases: 1,
				volumes: 3,
				domains: 2,
				previews: 1,
				statusPageComponents: 2,
				managedDnsRecords: 2,
			}),
		).toEqual([
			"2 environments (production, staging)",
			"5 services, 1 database",
			"3 volumes and their data",
			"2 domains and their Cloudflare records",
			"1 preview",
			"2 status page components, removed from their pages",
		]);
	});

	test("omits every count of zero", () => {
		expect(
			describeDeletionPlan({
				...EMPTY,
				environments: [{ name: "production", production: true }],
			}),
		).toEqual(["1 environment (production)"]);
		expect(describeDeletionPlan(EMPTY)).toEqual([]);
	});

	test("uses the singular for one of anything", () => {
		expect(
			describeDeletionPlan({
				...EMPTY,
				services: 1,
				volumes: 1,
				domains: 1,
				managedDnsRecords: 1,
				statusPageComponents: 1,
			}),
		).toEqual([
			"1 service",
			"1 volume and its data",
			"1 domain and their Cloudflare records",
			"1 status page component, removed from its page",
		]);
	});

	test("names services and databases separately when only one exists", () => {
		expect(describeDeletionPlan({ ...EMPTY, databases: 2 })).toEqual([
			"2 databases",
		]);
		expect(describeDeletionPlan({ ...EMPTY, services: 4 })).toEqual([
			"4 services",
		]);
	});

	test("leaves Cloudflare out when no record is managed", () => {
		expect(describeDeletionPlan({ ...EMPTY, domains: 2 })).toEqual([
			"2 domains",
		]);
		expect(describeDeletionPlan({ ...EMPTY, managedDnsRecords: 3 })).toEqual([
			"3 Cloudflare records",
		]);
	});
});

describe("describeDeletionPlan with no environment list", () => {
	test("treats a null list as none", () => {
		expect(describeDeletionPlan({ ...EMPTY, environments: null })).toEqual([]);
	});
});
