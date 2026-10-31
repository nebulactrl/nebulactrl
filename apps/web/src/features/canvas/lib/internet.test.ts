import { describe, expect, test } from "bun:test";
import { PLAIN_DOMAIN_RULE } from "#/api/models";
import { makeService } from "./fixtures";
import {
	INTERNET_HEIGHT,
	INTERNET_WIDTH,
	internetPosition,
	internetSummary,
} from "./internet";
import { NODE_WIDTH } from "./layout";

function withDomains(...hostnames: string[]) {
	return makeService({
		domains: hostnames.map((hostname, index) => ({
			...PLAIN_DOMAIN_RULE,
			id: `d-${hostname}`,
			hostname,
			targetPort: 80,
			component: "",
			process: "",
			generated: index === 0,
			status: "active" as const,
			statusMessage: null,
			dnsOk: null,
			dnsCheckedAt: null,
		})),
	});
}

describe("internetSummary", () => {
	test("nothing exposed draws nothing", () => {
		expect(internetSummary([makeService(), makeService()])).toBeNull();
		expect(internetSummary([])).toBeNull();
	});

	test("lists every hostname and the services that have one", () => {
		const web = withDomains("shop.acme.dev", "www.acme.dev");
		const db = makeService();
		const api = withDomains("api.acme.dev");
		const summary = internetSummary([web, db, api]);
		expect(summary?.domains).toEqual([
			"shop.acme.dev",
			"www.acme.dev",
			"api.acme.dev",
		]);
		expect(summary?.serviceIds).toEqual([web.id, api.id]);
	});
});

describe("internetPosition", () => {
	test("centres above the exposed services, clear of the topmost", () => {
		const at = internetPosition(
			{ nodes: { a: { x: 0, y: 200 }, b: { x: 600, y: 300 } } },
			["a", "b"],
		);
		const middle = (0 + 600 + NODE_WIDTH) / 2;
		expect(at.x + INTERNET_WIDTH / 2).toBe(middle);
		expect(at.y + INTERNET_HEIGHT).toBeLessThan(200);
	});

	test("ignores services without a place", () => {
		const at = internetPosition({ nodes: { a: { x: 100, y: 100 } } }, [
			"a",
			"missing",
		]);
		expect(at.x + INTERNET_WIDTH / 2).toBe(100 + NODE_WIDTH / 2);
	});

	test("falls back to the origin when nothing is placed", () => {
		const at = internetPosition({ nodes: {} }, ["x"]);
		expect(at.x).toBe(0);
		expect(at.y).toBeLessThan(0);
	});
});
