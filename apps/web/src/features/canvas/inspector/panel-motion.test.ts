import { describe, expect, test } from "bun:test";
import {
	closedOffset,
	planSlide,
	SLIDE_PAST_EDGE,
	slideKeyframes,
} from "./panel-motion";

const closedFrame = (extent: number) => ({
	offset: closedOffset(extent, "x"),
	opacity: 0,
});
const openFrame = { offset: 0, opacity: 1 };

describe("closedOffset", () => {
	test("a right-hand panel rests its own width plus the gap past the edge", () => {
		expect(closedOffset(700, "x")).toBe(700 + SLIDE_PAST_EDGE);
	});

	test("a phone sheet rests one height below", () => {
		expect(closedOffset(800, "y")).toBe(800);
	});
});

describe("planSlide", () => {
	const base = { extent: 700, axis: "x" as const, reduced: false };

	test("opening runs 250ms from off screen to rest", () => {
		const plan = planSlide({ ...base, state: "open", from: closedFrame(700) });
		expect(plan.from).toEqual(closedFrame(700));
		expect(plan.to).toEqual(openFrame);
		expect(plan.duration).toBeCloseTo(0.25, 6);
	});

	test("closing is faster than opening: 150ms", () => {
		const plan = planSlide({ ...base, state: "closed", from: openFrame });
		expect(plan.to).toEqual(closedFrame(700));
		expect(plan.duration).toBeCloseTo(0.15, 6);
	});

	test("a close that interrupts the open starts where the panel is, in proportion", () => {
		const halfway = { offset: closedOffset(700, "x") / 2, opacity: 0.8 };
		const plan = planSlide({ ...base, state: "closed", from: halfway });
		expect(plan.from).toEqual(halfway);
		expect(plan.to.offset).toBe(closedOffset(700, "x"));
		expect(plan.duration).toBeCloseTo(0.15 * 0.8, 6);
	});

	test("an open that interrupts the close finishes the rest of the travel", () => {
		const almostGone = { offset: closedOffset(700, "x") * 0.9, opacity: 0.1 };
		const plan = planSlide({ ...base, state: "open", from: almostGone });
		expect(plan.from).toEqual(almostGone);
		expect(plan.duration).toBeCloseTo(0.25 * 0.9, 6);
	});

	test("a slide that is nearly done does not crawl", () => {
		const nearly = { offset: 2, opacity: 0.99 };
		const plan = planSlide({ ...base, state: "open", from: nearly });
		expect(plan.duration).toBeCloseTo(0.25 * 0.35, 6);
	});

	test("reduced motion cross-fades in place over 150ms, both ways", () => {
		const reduced = { ...base, reduced: true };
		const open = planSlide({
			...reduced,
			state: "open",
			from: closedFrame(700),
		});
		expect(open.from).toEqual({ offset: 0, opacity: 0 });
		expect(open.to).toEqual(openFrame);
		expect(open.duration).toBeCloseTo(0.15, 6);
		const close = planSlide({ ...reduced, state: "closed", from: openFrame });
		expect(close.to).toEqual({ offset: 0, opacity: 0 });
		expect(close.duration).toBeCloseTo(0.15, 6);
	});
});

describe("slideKeyframes", () => {
	test("animates only transform and opacity, with a full transform string", () => {
		const plan = planSlide({
			state: "open",
			from: closedFrame(700),
			extent: 700,
			axis: "x",
			reduced: false,
		});
		const frames = slideKeyframes(plan, "x", false);
		expect(Object.keys(frames).sort()).toEqual(["opacity", "transform"]);
		expect(frames.transform).toEqual(["translateX(724px)", "none"]);
		expect(frames.opacity).toEqual([0, 1]);
	});

	test("a phone sheet travels on y", () => {
		const plan = planSlide({
			state: "closed",
			from: openFrame,
			extent: 800,
			axis: "y",
			reduced: false,
		});
		expect(slideKeyframes(plan, "y", false).transform).toEqual([
			"translateY(0px)",
			"translateY(800px)",
		]);
	});

	test("reduced motion has no transform at all", () => {
		const plan = planSlide({
			state: "open",
			from: closedFrame(700),
			extent: 700,
			axis: "x",
			reduced: true,
		});
		expect(Object.keys(slideKeyframes(plan, "x", true))).toEqual(["opacity"]);
	});
});
