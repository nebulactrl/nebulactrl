import { describe, expect, test } from "bun:test";
import {
	clampInspectorWidth,
	dockedWidthPx,
	INSPECTOR_DOCKED_CSS,
	INSPECTOR_MIN,
	isFullScreen,
	loadSavedWidth,
	MIN_CANVAS_FREE,
	resolveInspectorWidth,
	saveWidth,
	type WidthStorage,
	wideWidthPx,
	widthClassOf,
} from "./geometry";

function memory(): WidthStorage & { data: Map<string, string> } {
	const data = new Map<string, string>();
	return {
		data,
		getItem: (key) => data.get(key) ?? null,
		setItem: (key, value) => {
			data.set(key, value);
		},
	};
}

describe("widthClassOf", () => {
	test("Metrics, Analytics, Logs and Settings are wide", () => {
		for (const tab of ["metrics", "analytics", "logs", "settings"] as const)
			expect(widthClassOf(tab)).toBe("wide");
	});
	test("Overview, Deployments and Variables are docked", () => {
		for (const tab of ["overview", "deployments", "variables"] as const)
			expect(widthClassOf(tab)).toBe("docked");
	});
});

describe("dockedWidthPx", () => {
	test("follows 48vw between the 640 floor and the 920 ceiling", () => {
		expect(INSPECTOR_DOCKED_CSS).toBe("clamp(640px, 48vw, 920px)");
		expect(dockedWidthPx(1440)).toBeCloseTo(691.2);
		expect(dockedWidthPx(1280)).toBe(640);
		expect(dockedWidthPx(1800)).toBeCloseTo(864);
		expect(dockedWidthPx(3000)).toBe(920);
	});
	test("never exceeds 90% of a narrow viewport", () => {
		expect(dockedWidthPx(700)).toBeCloseTo(630);
	});
});

describe("wideWidthPx", () => {
	test("follows 66vw between the 640 floor and the 1120 ceiling", () => {
		expect(wideWidthPx(1440)).toBeCloseTo(950.4);
		expect(wideWidthPx(2400)).toBe(1120);
	});
	test("leaves 420px of canvas free: min(66vw, vw − 420px)", () => {
		expect(wideWidthPx(1200)).toBe(780);
		expect(MIN_CANVAS_FREE).toBe(420);
	});
	test("counts the free canvas from where the canvas starts", () => {
		// 1440 with a 232px sidebar: the canvas is 1208 wide, so 788 is the cap
		// (the unadjusted 950 would leave 258px).
		expect(wideWidthPx(1440, 232)).toBe(788);
		expect(1440 - 232 - wideWidthPx(1440, 232)).toBe(MIN_CANVAS_FREE);
	});
	test("never goes under the 640 floor, however narrow the room", () => {
		expect(wideWidthPx(1100, 232)).toBe(640);
	});
	test("never exceeds 90% of a narrow viewport", () => {
		expect(wideWidthPx(600)).toBe(540);
	});
});

describe("resolveInspectorWidth", () => {
	test("docked defaults to its CSS clamp, wide to its own", () => {
		expect(resolveInspectorWidth("docked", undefined, 1440)).toBe(691);
		expect(resolveInspectorWidth("docked", undefined, 2400)).toBe(920);
		expect(resolveInspectorWidth("wide", undefined, 1440)).toBe(950);
		expect(resolveInspectorWidth("wide", undefined, 1440, 232)).toBe(788);
	});
	test("a saved width wins, held to the viewport", () => {
		expect(resolveInspectorWidth("docked", 500, 1440)).toBe(500);
		expect(resolveInspectorWidth("wide", 5000, 1000)).toBe(900);
		expect(clampInspectorWidth(100, 1440)).toBe(INSPECTOR_MIN);
	});
});

describe("saved widths", () => {
	test("round-trip per class", () => {
		const storage = memory();
		saveWidth(storage, "wide", 777.4);
		expect(loadSavedWidth(storage, "wide")).toBe(777);
		expect(loadSavedWidth(storage, "docked")).toBeUndefined();
	});
	test("garbage and absent storage read as unset", () => {
		const storage = memory();
		storage.data.set("nebulactrl.inspector.width.v2.docked", "wide");
		expect(loadSavedWidth(storage, "docked")).toBeUndefined();
		storage.data.set("nebulactrl.inspector.width.v2.docked", "12");
		expect(loadSavedWidth(storage, "docked")).toBeUndefined();
		expect(loadSavedWidth(undefined, "docked")).toBeUndefined();
	});
	test("widths saved under the old key are ignored", () => {
		const storage = memory();
		storage.data.set("nebulactrl.inspector.width.docked", "420");
		storage.data.set("nebulactrl.inspector.width.wide", "700");
		expect(loadSavedWidth(storage, "docked")).toBeUndefined();
		expect(loadSavedWidth(storage, "wide")).toBeUndefined();
	});
});

describe("isFullScreen", () => {
	test("the panel covers the screen below 760px only", () => {
		expect(isFullScreen(390)).toBe(true);
		expect(isFullScreen(759)).toBe(true);
		expect(isFullScreen(760)).toBe(false);
	});
});
