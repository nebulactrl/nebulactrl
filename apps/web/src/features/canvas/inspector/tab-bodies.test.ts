import { describe, expect, test } from "bun:test";
import { bodyFor, ROUTE_TAB_BODY, TAB_BODY } from "./tab-bodies";
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
