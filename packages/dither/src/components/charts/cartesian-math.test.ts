import { describe, expect, test } from "bun:test";
import {
	bandWidth,
	barSlotsInBand,
	buildScale,
	DIMMED_OPACITY,
	indexAtX,
	labelAnchor,
	labelIndices,
	opacityFor,
	type Plot,
	plotBox,
	readoutPosition,
	scrubKeyTarget,
	snapCellToDevice,
	xAt,
	yScaleFor,
} from "./cartesian-math";

const plot: Plot = { left: 10, right: 110, top: 8, bottom: 108 };

describe("buildScale", () => {
	test("includes zero and rounds outward", () => {
		const s = buildScale([3, 98]);
		expect(s.min).toBe(0);
		expect(s.max).toBe(100);
		expect(s.ticks).toEqual([0, 20, 40, 60, 80, 100]);
	});
	test("fits the data without zero", () => {
		const s = buildScale([150, 180], { zero: false });
		expect(s.min).toBe(150);
		expect(s.max).toBe(180);
	});
	test("an explicit domain wins over zero", () => {
		const s = buildScale([40, 60], { domain: [0, 180] });
		expect(s.min).toBe(0);
		expect(s.max).toBeGreaterThanOrEqual(180);
	});
	test("no finite data gives a unit scale", () => {
		const s = buildScale([Number.NaN]);
		expect(s.min).toBe(0);
		expect(s.max).toBe(1);
	});
});

describe("plotBox", () => {
	test("bare charts use the whole width", () => {
		const p = plotBox({
			width: 200,
			height: 100,
			axes: false,
			tickLabels: [],
			layout: "point",
		});
		expect(p).toEqual({ left: 0, right: 200, top: 3, bottom: 99 });
	});
	test("the left gutter follows the longest tick label", () => {
		const p = plotBox({
			width: 300,
			height: 120,
			axes: true,
			tickLabels: ["1.2K", "0"],
			layout: "point",
		});
		expect(p.left).toBe(Math.ceil(4 * 6.1) + 12);
		expect(p.right).toBe(292);
		expect(p.bottom).toBe(100);
	});
	test("band layouts keep the full right edge", () => {
		const p = plotBox({
			width: 300,
			height: 120,
			axes: true,
			tickLabels: ["0"],
			layout: "band",
		});
		expect(p.right).toBe(300);
	});
});

describe("x positions", () => {
	test("point layout spans the plot edge to edge", () => {
		expect(xAt(0, 5, plot, "point")).toBe(10);
		expect(xAt(4, 5, plot, "point")).toBe(110);
		expect(bandWidth(5, plot, "point")).toBe(25);
	});
	test("a single point sits in the middle", () => {
		expect(xAt(0, 1, plot, "point")).toBe(60);
	});
	test("band layout centres each datum in its slot", () => {
		expect(xAt(0, 4, plot, "band")).toBe(22.5);
		expect(xAt(3, 4, plot, "band")).toBe(97.5);
	});
});

describe("indexAtX", () => {
	test("point layout picks the nearest datum and clamps", () => {
		expect(indexAtX(10, 5, plot, "point")).toBe(0);
		expect(indexAtX(24, 5, plot, "point")).toBe(1);
		expect(indexAtX(500, 5, plot, "point")).toBe(4);
		expect(indexAtX(-50, 5, plot, "point")).toBe(0);
	});
	test("band layout picks the slot", () => {
		expect(indexAtX(34, 4, plot, "band")).toBe(0);
		expect(indexAtX(36, 4, plot, "band")).toBe(1);
	});
	test("no data", () => {
		expect(indexAtX(20, 0, plot, "point")).toBe(-1);
	});
});

describe("labelIndices", () => {
	test("thins to about one per 72px and keeps the last", () => {
		expect(labelIndices(10, 144)).toEqual([0, 5, 9]);
	});
	test("everything fits", () => {
		expect(labelIndices(3, 500)).toEqual([0, 1, 2]);
	});
	test("drops the one before the last when it would crowd it", () => {
		expect(labelIndices(11, 144)).toEqual([0, 10]);
	});
	test("empty", () => {
		expect(labelIndices(0, 100)).toEqual([]);
	});
	test("anchors keep the ends inside the chart", () => {
		expect(labelAnchor("point", 0, 5)).toBe("start");
		expect(labelAnchor("point", 4, 5)).toBe("end");
		expect(labelAnchor("point", 2, 5)).toBe("middle");
		expect(labelAnchor("band", 0, 5)).toBe("middle");
	});
});

describe("scrubKeyTarget", () => {
	test("arrows start from the ends when nothing is scrubbed", () => {
		expect(scrubKeyTarget("ArrowLeft", null, 5)).toBe(4);
		expect(scrubKeyTarget("ArrowRight", null, 5)).toBe(0);
	});
	test("arrows clamp", () => {
		expect(scrubKeyTarget("ArrowRight", 4, 5)).toBe(4);
		expect(scrubKeyTarget("ArrowLeft", 0, 5)).toBe(0);
	});
	test("home, end, pages", () => {
		expect(scrubKeyTarget("Home", 3, 5)).toBe(0);
		expect(scrubKeyTarget("End", 1, 5)).toBe(4);
		expect(scrubKeyTarget("PageDown", 10, 50)).toBe(15);
		expect(scrubKeyTarget("PageUp", null, 50)).toBe(44);
	});
	test("escape clears; other keys are not ours", () => {
		expect(scrubKeyTarget("Escape", 2, 5)).toBeNull();
		expect(scrubKeyTarget("a", 2, 5)).toBeUndefined();
		expect(scrubKeyTarget("ArrowRight", null, 0)).toBeUndefined();
	});
});

describe("readoutPosition", () => {
	const box = { w: 80, h: 40 };
	test("centres above the marker", () => {
		expect(
			readoutPosition({ x: 100, y: 100, width: 300, top: 8 }, box),
		).toEqual({
			left: 60,
			top: 48,
		});
	});
	test("clamps inside the chart", () => {
		expect(
			readoutPosition({ x: 10, y: 100, width: 300, top: 8 }, box).left,
		).toBe(0);
		expect(
			readoutPosition({ x: 295, y: 100, width: 300, top: 8 }, box).left,
		).toBe(220);
	});
	test("sits beside the crosshair when there is no room above", () => {
		expect(readoutPosition({ x: 100, y: 30, width: 300, top: 8 }, box)).toEqual(
			{
				left: 112,
				top: 8,
			},
		);
		expect(readoutPosition({ x: 280, y: 30, width: 300, top: 8 }, box)).toEqual(
			{
				left: 188,
				top: 8,
			},
		);
	});
});

describe("spotlight and bars", () => {
	test("opacityFor dims everything but the focus", () => {
		expect(opacityFor(null, 1)).toBe(1);
		expect(opacityFor(1, 1)).toBe(1);
		expect(opacityFor(1, 0)).toBe(DIMMED_OPACITY);
	});
	test("grouped bars sit side by side, centred", () => {
		expect(barSlotsInBand(50, 40, 2, "grouped")).toEqual([
			{ x: 34, w: 15 },
			{ x: 51, w: 15 },
		]);
	});
	test("stacked series share one slot", () => {
		const slots = barSlotsInBand(50, 40, 2, "stacked");
		expect(slots).toEqual([
			{ x: 34, w: 32 },
			{ x: 34, w: 32 },
		]);
	});
	test("bars are capped at 36px", () => {
		expect(barSlotsInBand(100, 200, 1, "grouped")).toEqual([{ x: 82, w: 36 }]);
	});
});

describe("yScaleFor", () => {
	test("min at the bottom, max at the top", () => {
		const y = yScaleFor(plot, { min: 0, max: 100, ticks: [0, 100] });
		expect(y(0)).toBe(108);
		expect(y(100)).toBe(8);
		expect(y(50)).toBe(58);
	});
});

describe("snapCellToDevice", () => {
	test("rounds to whole device pixels", () => {
		expect(snapCellToDevice(3, 1)).toBe(3);
		expect(snapCellToDevice(2.5, 2)).toBe(2.5);
		expect(snapCellToDevice(2.3, 1.5)).toBe(2);
	});
	test("is never below one device pixel", () => {
		expect(snapCellToDevice(0.1, 2)).toBe(0.5);
		expect(snapCellToDevice(0.1, 0)).toBe(1);
	});
});
