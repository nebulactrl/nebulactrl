import { describe, expect, test } from "bun:test";
import {
	type Box,
	cable,
	chooseSides,
	INTERNET_JACK,
	jackCentre,
} from "./cable";

describe("cable", () => {
	test("leaves a right jack heading right and enters a left jack from the left", () => {
		const c = cable(
			{ x: 0, y: 0, side: "right" },
			{ x: 200, y: 0, side: "left" },
		);
		const m = c.d.match(
			/M ([\d.-]+) ([\d.-]+) C ([\d.-]+) ([\d.-]+), ([\d.-]+) ([\d.-]+), ([\d.-]+) ([\d.-]+)/,
		);
		if (!m) throw new Error(`unexpected path ${c.d}`);
		const [, x0, y0, x1, , x2, , x3, y3] = m.map(Number) as number[];
		expect([x0, y0, x3, y3]).toEqual([0, 0, 200, 0]);
		expect(x1).toBeGreaterThan(0);
		expect(x2).toBeLessThan(200);
		expect(c.distance).toBe(200);
	});

	test("a horizontal cable sags below its ends, more the longer it is", () => {
		const short = cable(
			{ x: 0, y: 0, side: "right" },
			{ x: 100, y: 0, side: "left" },
		);
		const long = cable(
			{ x: 0, y: 0, side: "right" },
			{ x: 400, y: 0, side: "left" },
		);
		expect(short.mid.y).toBeGreaterThan(0);
		expect(long.mid.y).toBeGreaterThan(short.mid.y);
		// Halfway along a symmetric cable is halfway across.
		expect(long.mid.x).toBeCloseTo(200, 5);
	});

	test("the sag is capped", () => {
		const far = cable(
			{ x: 0, y: 0, side: "right" },
			{ x: 5000, y: 0, side: "left" },
		);
		// 0.75 of the 96px control offset.
		expect(far.mid.y).toBeCloseTo(72, 5);
	});

	test("a cable leaving a bottom jack does not sag extra", () => {
		const c = cable(
			{ x: 100, y: 0, side: "bottom" },
			{ x: 100, y: 200, side: "top" },
		);
		expect(c.mid.x).toBeCloseTo(100, 5);
		expect(c.mid.y).toBeCloseTo(100, 5);
	});

	test("the plug sits one radius inside the handle's reported edge", () => {
		expect(jackCentre({ x: 100, y: 50, side: "right" }, 8)).toEqual({
			x: 92,
			y: 50,
			side: "right",
		});
		expect(jackCentre({ x: 0, y: 50, side: "left" }, 8)).toEqual({
			x: 8,
			y: 50,
			side: "left",
		});
		expect(jackCentre({ x: 10, y: 0, side: "bottom" }, 8)).toEqual({
			x: 10,
			y: -8,
			side: "bottom",
		});
	});
});

const card = (x: number, y: number): Box => ({ x, y, w: 288, h: 176 });

describe("chooseSides", () => {
	test("a target to the right takes source right → target left", () => {
		expect(chooseSides(card(0, 0), card(400, 300))).toEqual({
			source: "right",
			target: "left",
		});
	});

	test("a target to the left takes source left → target right", () => {
		expect(chooseSides(card(800, 0), card(100, 20))).toEqual({
			source: "left",
			target: "right",
		});
	});

	test("needs real clearance between the cards to run across", () => {
		// 10px apart: not enough room, so it is treated as a stack.
		const sides = chooseSides(card(0, 0), card(298, 0));
		expect(sides.source).toBe(sides.target);
		expect(chooseSides(card(0, 0), card(312, 0))).toEqual({
			source: "right",
			target: "left",
		});
	});

	test("a vertical stack uses the side whose jacks are closer", () => {
		// Same column: a tie goes to the right.
		expect(chooseSides(card(100, 0), card(100, 300))).toEqual({
			source: "right",
			target: "right",
		});
		// Target narrower and shifted right: the right jacks are nearer.
		expect(
			chooseSides(card(100, 0), { x: 200, y: 300, w: 188, h: 176 }),
		).toEqual({
			source: "right",
			target: "right",
		});
		// Target wider and shifted left: the left jacks are nearer.
		expect(
			chooseSides(card(100, 0), { x: 90, y: 300, w: 330, h: 176 }),
		).toEqual({
			source: "left",
			target: "left",
		});
	});

	test("the Internet entry plugs into its own top jack, dropped into from straight above", () => {
		expect(INTERNET_JACK).toBe("top");
		// The cable ends on the jack's centre, one radius below the reported edge.
		const jack = jackCentre({ x: 400, y: 300, side: "top" });
		expect(jack).toEqual({ x: 400, y: 308, side: "top" });
		const c = cable({ x: 400, y: 100, side: "bottom" }, jack);
		// Straight above the jack the cable stays in its column: no sag, no sway.
		const [, c1x, , c2x] =
			c.d.match(/C ([\d.]+) ([\d.]+), ([\d.]+) ([\d.]+)/) ?? [];
		expect(Number(c1x)).toBe(400);
		expect(Number(c2x)).toBe(400);
		expect(c.mid.x).toBe(400);
	});

	test("same-side cables bow outward instead of crossing behind the cards", () => {
		const c = cable(
			{ x: 388, y: 26, side: "right" },
			{ x: 388, y: 326, side: "right" },
		);
		expect(c.mid.x).toBeGreaterThan(388);
	});
});
