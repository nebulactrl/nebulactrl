import { afterEach, describe, expect, spyOn, test } from "bun:test";
import {
	bodyFor,
	preloadOverview,
	preloadRemainingTabs,
	ROUTE_TAB_BODY,
	TAB_BODY,
} from "./tab-bodies";
import { INSPECTOR_TABS } from "./tab-ids";

describe("bodyFor", () => {
	test("gives every tab of an app its own body", () => {
		for (const tab of INSPECTOR_TABS) {
			expect(bodyFor({ kind: "app" }, tab)).toBe(TAB_BODY[tab]);
		}
		expect(new Set(INSPECTOR_TABS.map((tab) => TAB_BODY[tab])).size).toBe(
			INSPECTOR_TABS.length,
		);
	});

	test("a route has its own Overview and Settings and shares Deployments", () => {
		expect(bodyFor({ kind: "route" }, "overview")).toBe(
			ROUTE_TAB_BODY.overview as never,
		);
		expect(bodyFor({ kind: "route" }, "settings")).toBe(
			ROUTE_TAB_BODY.settings as never,
		);
		expect(bodyFor({ kind: "route" }, "deployments")).toBe(
			TAB_BODY.deployments,
		);
		expect(bodyFor({ kind: "route" }, "overview")).not.toBe(TAB_BODY.overview);
	});

	test("a tab a route does not have falls back to its Overview", () => {
		expect(bodyFor({ kind: "route" }, "logs")).toBe(
			ROUTE_TAB_BODY.overview as never,
		);
	});
});

const everyBody = [
	...Object.values(TAB_BODY),
	...Object.values(ROUTE_TAB_BODY),
];

const installed: { mockRestore(): void }[] = [];

/** Replaces every body's chunk loader, so no tab module is really imported. */
function stubPreloads(
	reply: () => Promise<never> = () => Promise.resolve({} as never),
) {
	const spies = new Map(
		[...new Set(everyBody)].map((body) => [
			body,
			spyOn(body, "preload").mockImplementation(reply),
		]),
	);
	installed.push(...spies.values());
	return spies;
}

const calledBodies = (spies: ReturnType<typeof stubPreloads>) =>
	[...spies]
		.filter(([, spy]) => spy.mock.calls.length > 0)
		.map(([body]) => body);

afterEach(() => {
	for (const spy of installed.splice(0)) spy.mockRestore();
});

describe("preloadOverview", () => {
	test("an app starts downloading its Overview and nothing else", () => {
		const spies = stubPreloads();
		preloadOverview({ kind: "app" });
		expect(calledBodies(spies)).toEqual([TAB_BODY.overview]);
	});

	test("a route starts downloading its own Overview, not the app's", () => {
		const spies = stubPreloads();
		preloadOverview({ kind: "route" });
		expect(calledBodies(spies)).toEqual([ROUTE_TAB_BODY.overview as never]);
	});

	test("a chunk that fails to download is ignored (the body retries on mount)", async () => {
		stubPreloads(() => Promise.reject(new Error("network")));
		expect(() => preloadOverview({ kind: "app" })).not.toThrow();
		await Promise.resolve();
	});
});

describe("preloadRemainingTabs", () => {
	test("an app downloads all eight tab bodies", () => {
		const spies = stubPreloads();
		preloadRemainingTabs({ kind: "database" });
		expect(new Set(calledBodies(spies))).toEqual(
			new Set(Object.values(TAB_BODY)),
		);
		expect(calledBodies(spies)).toHaveLength(8);
	});

	test("a route downloads only the three tabs it has", () => {
		const spies = stubPreloads();
		preloadRemainingTabs({ kind: "route" });
		expect(new Set(calledBodies(spies))).toEqual(
			new Set([
				ROUTE_TAB_BODY.overview,
				ROUTE_TAB_BODY.deployments,
				ROUTE_TAB_BODY.settings,
			] as never[]),
		);
	});

	test("failed downloads are ignored", async () => {
		stubPreloads(() => Promise.reject(new Error("offline")));
		expect(() => preloadRemainingTabs({ kind: "app" })).not.toThrow();
		await Promise.resolve();
	});
});
