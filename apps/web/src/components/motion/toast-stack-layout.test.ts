import { describe, expect, test } from "bun:test";
import {
	DEFAULT_HEIGHT,
	STACK_GAP,
	STACK_PEEK,
	toastStackLayout,
} from "./toast-stack-layout";

const toasts = [
	{ id: "front" },
	{ id: "second" },
	{ id: "third" },
	{ id: "fourth" },
];

describe("toastStackLayout", () => {
	test("collapsed, the toasts behind a wrapped front toast take its height and still peek out", () => {
		const { slots, totalHeight } = toastStackLayout(
			toasts,
			{ front: 120, second: 58, third: 200, fourth: 58 },
			false,
		);

		expect(slots.map((s) => s.forcedHeight)).toEqual([
			undefined,
			120,
			120,
			120,
		]);
		// Each card shrinks from its bottom edge, so a card's top sits its lift
		// plus its scaled height above the stack's bottom. The gap between two
		// neighbouring tops is the visible peek, and it must stay what the
		// design shows for an unwrapped, DEFAULT_HEIGHT toast.
		const top = (i: number) =>
			-(slots[i]?.y ?? 0) + 120 * (slots[i]?.scale ?? 1);
		const designedPeek = STACK_PEEK - DEFAULT_HEIGHT * 0.05;
		expect(top(1) - top(0)).toBeCloseTo(designedPeek);
		expect(top(2) - top(1)).toBeCloseTo(designedPeek);
		expect(slots.map((s) => s.opacity)).toEqual([1, 1, 1, 0]);
		expect(totalHeight).toBe(120 + 2 * STACK_PEEK);
	});

	test("expanded, every toast keeps its own measured height", () => {
		const { slots, totalHeight } = toastStackLayout(
			toasts.slice(0, 3),
			{ front: 120, second: 58, third: 200 },
			true,
		);

		expect(slots.map((s) => s.forcedHeight)).toEqual([
			undefined,
			undefined,
			undefined,
		]);
		expect(slots.map((s) => s.y)).toEqual([
			0,
			-(120 + STACK_GAP),
			-(120 + 58 + 2 * STACK_GAP),
		]);
		expect(totalHeight).toBe(120 + 58 + 200 + 2 * STACK_GAP);
	});

	test("unmeasured toasts count as one line of title and description, laid out as designed", () => {
		const { slots, totalHeight } = toastStackLayout(
			toasts.slice(0, 2),
			{},
			false,
		);

		expect(slots[1]?.forcedHeight).toBe(DEFAULT_HEIGHT);
		expect(slots[1]?.y).toBe(-STACK_PEEK);
		expect(totalHeight).toBe(DEFAULT_HEIGHT + STACK_PEEK);
	});

	test("an empty stack reserves no space", () => {
		expect(toastStackLayout([], {}, false).totalHeight).toBe(0);
		expect(toastStackLayout([], {}, true).totalHeight).toBe(0);
	});
});
