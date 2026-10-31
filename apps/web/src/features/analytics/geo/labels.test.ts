import { describe, expect, test } from "bun:test";
import {
	type LabelMarker,
	labelCap,
	type PlacedLabel,
	placeLabels,
} from "./labels";

/** 6px a character, 16px tall: easy to reason about in the cases below. */
const measure = (label: string) => ({ width: label.length * 6, height: 16 });

function marker(
	key: string,
	count: number,
	x: number,
	y: number,
	label = key,
): LabelMarker {
	return { key, label, count, x, y };
}

const overlap = (a: PlacedLabel, b: PlacedLabel) =>
	a.left < b.left + b.width &&
	a.left + a.width > b.left &&
	a.top < b.top + b.height &&
	a.top + a.height > b.top;

describe("placeLabels", () => {
	test("no two labels overlap, however crowded the markers", () => {
		// Frankfurt, Berlin, Paris and friends: eight cities within 60px.
		const crowd = [
			marker("frankfurt", 90, 300, 200, "Frankfurt, DE"),
			marker("berlin", 80, 330, 190, "Berlin, DE"),
			marker("paris", 70, 270, 215, "Paris, FR"),
			marker("velizy", 60, 268, 220, "Vélizy-Villacoublay, FR"),
			marker("andorra", 50, 262, 250, "Andorra la Vella, AD"),
			marker("london", 40, 255, 185, "London, GB"),
			marker("amsterdam", 30, 290, 180, "Amsterdam, NL"),
			marker("zurich", 20, 305, 235, "Zürich, CH"),
		];
		const { placed, hidden } = placeLabels(
			crowd,
			{ width: 600, height: 400 },
			measure,
		);
		expect(placed.length).toBeGreaterThan(2);
		for (const [i, a] of placed.entries()) {
			for (const b of placed.slice(i + 1)) expect(overlap(a, b)).toBe(false);
		}
		// Every marker is either labelled or handed to its tooltip, never both.
		expect(placed.length + hidden.length).toBe(crowd.length);
		expect(placed.map((p) => p.key).filter((k) => hidden.includes(k))).toEqual(
			[],
		);
	});

	test("a label never covers another marker", () => {
		const { placed } = placeLabels(
			[
				marker("a", 9, 100, 100, "Alpha"),
				// Sits exactly where Alpha's right-hand label would run.
				marker("b", 1, 125, 100, "Bravo"),
			],
			{ width: 400, height: 200 },
			measure,
		);
		const a = placed.find((p) => p.key === "a");
		expect(a).toBeDefined();
		expect(a?.side).not.toBe("right");
	});

	test("the busiest place wins a contested spot, whatever the input order", () => {
		// A 100×30 box leaves each marker only its right-hand spot.
		const box = { width: 100, height: 30 };
		const low = marker("low", 3, 30, 14, "Lowtown");
		const high = marker("high", 9, 30, 14, "Hightown");
		for (const input of [
			[low, high],
			[high, low],
		]) {
			const { placed, hidden } = placeLabels(input, box, measure);
			expect(placed.map((p) => p.key)).toEqual(["high"]);
			expect(hidden).toEqual(["low"]);
		}
	});

	test("labels come out busiest first, and ties keep input order", () => {
		const { placed } = placeLabels(
			[
				marker("c", 1, 50, 50),
				marker("a", 5, 50, 150),
				marker("b", 5, 50, 250),
			],
			{ width: 400, height: 400 },
			measure,
		);
		expect(placed.map((p) => p.key)).toEqual(["a", "b", "c"]);
	});

	test("falls back to the left at the right edge", () => {
		const { placed } = placeLabels(
			[marker("tokyo", 1, 190, 100, "Tokyo")],
			{ width: 200, height: 200 },
			measure,
		);
		expect(placed[0]?.side).toBe("left");
		expect(placed[0]?.left).toBe(190 - 10 - 30);
	});

	test("falls back above when neither side has room", () => {
		const { placed } = placeLabels(
			[marker("m", 1, 50, 100, "Mid")],
			{ width: 100, height: 200 },
			() => ({ width: 90, height: 16 }),
		);
		expect(placed[0]?.side).toBe("top");
		expect(placed[0]?.top).toBe(100 - 10 - 16);
	});

	test("falls back below when there is no room above either", () => {
		const { placed } = placeLabels(
			[marker("m", 1, 100, 8, "Mid")],
			{ width: 200, height: 200 },
			() => ({ width: 120, height: 16 }),
		);
		expect(placed[0]?.side).toBe("bottom");
	});

	test("a spot taken by a busier label sends the next marker to another side", () => {
		const { placed } = placeLabels(
			[
				marker("busy", 9, 50, 100, "Busytown"),
				marker("near", 1, 125, 100, "Near"),
			],
			{ width: 150, height: 200 },
			measure,
		);
		expect(placed.find((p) => p.key === "busy")?.side).toBe("right");
		// Right would leave the box and left is under Busytown's label: above.
		expect(placed.find((p) => p.key === "near")?.side).toBe("top");
	});

	test("max caps the labels and the rest are hidden", () => {
		const row = [1, 2, 3, 4, 5].map((n) => marker(`m${n}`, n, n * 70, 100));
		const { placed, hidden } = placeLabels(
			row,
			{ width: 400, height: 200 },
			measure,
			{ max: 2 },
		);
		expect(placed.map((p) => p.key)).toEqual(["m5", "m4"]);
		expect(hidden.sort()).toEqual(["m1", "m2", "m3"]);
	});

	test("markers outside the box are in neither list", () => {
		const { placed, hidden } = placeLabels(
			[marker("in", 1, 50, 50), marker("out", 9, 500, 50)],
			{ width: 200, height: 100 },
			measure,
		);
		expect(placed.map((p) => p.key)).toEqual(["in"]);
		expect(hidden).toEqual([]);
	});
});

describe("labelCap", () => {
	const box = { width: 800, height: 600 };
	test("fewer labels the more of the world a view shows", () => {
		expect(labelCap(134, box)).toBeLessThan(labelCap(60, box));
		expect(labelCap(60, box)).toBeLessThan(labelCap(30, box));
		expect(labelCap(30, box)).toBeLessThan(labelCap(10, box));
	});
	test("a small box gets fewer, but at least four", () => {
		expect(labelCap(134, { width: 420, height: 156 })).toBe(4);
		expect(labelCap(10, { width: 100, height: 50 })).toBe(4);
		// A phone-width world map still names its four busiest places.
		expect(labelCap(134, { width: 358, height: 144 })).toBe(4);
	});
});
