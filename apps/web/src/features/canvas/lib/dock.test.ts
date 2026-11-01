import { describe, expect, test } from "bun:test";
import { type DockPlacement, dockPosition, revealShift } from "./dock";

const base: DockPlacement = {
	cardCentreX: 500,
	cardBottom: 300,
	dockWidth: 290,
	dockHeight: 40,
	gap: 14,
	margin: 12,
	viewportWidth: 1200,
	viewportHeight: 800,
};

describe("dockPosition", () => {
	test("sits centred under the card", () => {
		expect(dockPosition(base)).toEqual({ left: 355, top: 314 });
	});

	test("stays inside the left and right edges", () => {
		expect(dockPosition({ ...base, cardCentreX: 20 }).left).toBe(12);
		// The inspector's width is already off viewportWidth.
		expect(
			dockPosition({ ...base, cardCentreX: 1190, viewportWidth: 900 }).left,
		).toBe(900 - 290 - 12);
	});

	test("stays above the bottom edge and below the top one", () => {
		expect(dockPosition({ ...base, cardBottom: 790 }).top).toBe(800 - 40 - 12);
		expect(dockPosition({ ...base, cardBottom: -400 }).top).toBe(12);
	});

	test("a canvas narrower than the dock pins it to the margin", () => {
		expect(dockPosition({ ...base, viewportWidth: 200 }).left).toBe(12);
	});
});

describe("dockPosition around the zoom stack", () => {
	// The stack: x 12..46, y 598..788 on an 800px canvas.
	const keepOut = { left: 12, top: 598, right: 46, bottom: 788 };

	test("a dock clear of the stack stays where it was", () => {
		expect(dockPosition({ ...base, keepOut })).toEqual(dockPosition(base));
		// Low on the canvas but right of the stack.
		expect(
			dockPosition({ ...base, keepOut, cardBottom: 640, cardCentreX: 600 }),
		).toEqual({ left: 455, top: 654 });
	});

	test("a dock that would cover the stack slides past its right edge", () => {
		const placed = dockPosition({
			...base,
			keepOut,
			cardBottom: 640,
			cardCentreX: 20,
		});
		expect(placed).toEqual({ left: 58, top: 654 });
	});

	test("with no room to slide it hops above the stack instead", () => {
		const placed = dockPosition({
			...base,
			keepOut,
			cardBottom: 640,
			cardCentreX: 20,
			viewportWidth: 300,
		});
		expect(placed).toEqual({ left: 12, top: 546 });
	});

	test("a dock above the stack's top edge is left alone", () => {
		expect(
			dockPosition({ ...base, keepOut, cardBottom: 486, cardCentreX: 20 }),
		).toEqual({ left: 12, top: 500 });
	});

	test("a dock that fits nowhere stays at its clamped place", () => {
		// 130px tall, 300 wide: no room right of the stack or above it.
		const placed = dockPosition({
			...base,
			keepOut: { left: 12, top: 30, right: 46, bottom: 120 },
			cardBottom: 100,
			cardCentreX: 20,
			viewportWidth: 300,
			viewportHeight: 130,
		});
		expect(placed).toEqual({ left: 12, top: 78 });
	});
});

describe("revealShift", () => {
	const view = {
		viewWidth: 1400,
		viewHeight: 800,
		margin: 16,
		topMargin: 64,
		dockBottom: 420,
	};
	const card = { left: 900, top: 200, right: 1188, bottom: 370 };

	test("a card the inspector would cover pans left until it clears it", () => {
		const { dx, dy } = revealShift({ ...view, card, rightInset: 420 });
		// Free area ends at 1400 - 420 - 16 = 964.
		expect(card.right + dx).toBe(964);
		expect(dy).toBe(0);
	});

	test("a card already clear of the inspector stays", () => {
		expect(revealShift({ ...view, card, rightInset: 100 })).toEqual({
			dx: 0,
			dy: 0,
		});
	});

	test("a card wider than the free area keeps its left edge in view", () => {
		const { dx } = revealShift({ ...view, card, rightInset: 1200 });
		expect(card.left + dx).toBe(16);
	});

	test("the dock under a low card pans up, but never past the toolbars", () => {
		const low = { left: 100, top: 600, right: 388, bottom: 770 };
		const { dy } = revealShift({
			...view,
			card: low,
			dockBottom: 824,
			rightInset: 0,
		});
		expect(824 + dy).toBe(784);
		const tall = { left: 100, top: 70, right: 388, bottom: 900 };
		expect(
			revealShift({ ...view, card: tall, dockBottom: 954, rightInset: 0 }).dy,
		).toBe(64 - 70);
	});
});
