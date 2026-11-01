import { describe, expect, test } from "bun:test";
import {
	hasBackupsTab,
	INSPECTOR_TABS,
	parseTab,
	resolveTab,
	TAB_CHORD_KEY,
	tabForChordKey,
	tabsFor,
} from "./tab-ids";

describe("tab chords", () => {
	test("every tab has its own key", () => {
		const keys = INSPECTOR_TABS.map((tab) => TAB_CHORD_KEY[tab]);
		expect(new Set(keys).size).toBe(INSPECTOR_TABS.length);
	});
	test("g then the key picks the tab, in either case", () => {
		expect(tabForChordKey("o")).toBe("overview");
		expect(tabForChordKey("D")).toBe("deployments");
		expect(tabForChordKey("s")).toBe("settings");
		expect(tabForChordKey("x")).toBeUndefined();
	});
});

describe("parseTab", () => {
	test("falls back to overview", () => {
		expect(parseTab("logs")).toBe("logs");
		expect(parseTab("nope")).toBe("overview");
		expect(parseTab(undefined)).toBe("overview");
	});
});

describe("tabsFor", () => {
	const managed = {
		kind: "database",
		config: { database: { engine: "postgres" } },
	};
	const plainDatabase = { kind: "database", config: {} };
	const app = { kind: "app", config: {} };
	const withVolume = { kind: "app", config: {}, volumes: [{ name: "data" }] };

	test("Backups belongs to managed databases and services with a volume", () => {
		expect(tabsFor(managed)).toContain("backups");
		expect(tabsFor(plainDatabase)).not.toContain("backups");
		expect(tabsFor(app)).not.toContain("backups");
		expect(tabsFor({ ...app, volumes: [] })).not.toContain("backups");
		expect(tabsFor(withVolume)).toContain("backups");
	});
	test("a database or a route never takes the volume tab", () => {
		const volumes = [{ name: "data" }];
		expect(tabsFor({ ...plainDatabase, volumes })).not.toContain("backups");
		expect(tabsFor({ kind: "route", config: {}, volumes })).not.toContain(
			"backups",
		);
		expect(hasBackupsTab({ ...managed, volumes })).toBe(true);
	});
	test("a tab the service lacks resolves to Overview", () => {
		expect(resolveTab("backups", managed)).toBe("backups");
		expect(resolveTab("backups", withVolume)).toBe("backups");
		expect(resolveTab("backups", app)).toBe("overview");
		expect(resolveTab("logs", app)).toBe("logs");
		expect(tabForChordKey("b")).toBe("backups");
	});
});

describe("tabsFor a Compose stack", () => {
	const stack = { kind: "stack", config: {}, volumes: [{ name: "data" }] };
	test("shows Overview, Deployments, Variables, Metrics, Logs and Settings", () => {
		expect(tabsFor(stack)).toEqual([
			"overview",
			"deployments",
			"variables",
			"metrics",
			"logs",
			"settings",
		]);
	});
	test("never takes Backups or Analytics, even with a volume", () => {
		expect(hasBackupsTab(stack)).toBe(false);
		expect(resolveTab("backups", stack)).toBe("overview");
		expect(resolveTab("analytics", stack)).toBe("overview");
		expect(resolveTab("logs", stack)).toBe("logs");
	});
});
