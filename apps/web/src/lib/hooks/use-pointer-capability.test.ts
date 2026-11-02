import { afterEach, describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { useHoverCapable } from "./use-hover-capable";
import { useTouchCapable } from "./use-touch-capable";

const realMatchMedia = window.matchMedia;
const realTouchPoints = Object.getOwnPropertyDescriptor(
	Navigator.prototype,
	"maxTouchPoints",
);

class FakeQuery extends EventTarget {
	matches: boolean;
	constructor(matches: boolean) {
		super();
		this.matches = matches;
	}
	addEventListener = (type: string, listener: () => void) =>
		super.addEventListener(type, listener);
	removeEventListener = (type: string, listener: () => void) =>
		super.removeEventListener(type, listener);
	flip(matches: boolean) {
		this.matches = matches;
		this.dispatchEvent(new Event("change"));
	}
}

/** Answers every media query with `query`, recording what was asked. */
function stubMedia(query: FakeQuery): string[] {
	const asked: string[] = [];
	window.matchMedia = ((q: string) => {
		asked.push(q);
		return query;
	}) as unknown as typeof window.matchMedia;
	return asked;
}

function stubTouchPoints(points: number) {
	Object.defineProperty(navigator, "maxTouchPoints", {
		configurable: true,
		get: () => points,
	});
}

afterEach(() => {
	window.matchMedia = realMatchMedia;
	if (realTouchPoints) {
		Object.defineProperty(
			Navigator.prototype,
			"maxTouchPoints",
			realTouchPoints,
		);
	}
	Reflect.deleteProperty(navigator, "maxTouchPoints");
});

describe("useHoverCapable", () => {
	test("is true with a mouse or trackpad, and asks for a fine hovering pointer", () => {
		const asked = stubMedia(new FakeQuery(true));
		const { result } = renderHook(() => useHoverCapable());
		expect(result.current).toBe(true);
		expect(asked).toEqual(["(hover: hover) and (pointer: fine)"]);
	});

	test("is false on a touch-only device", () => {
		stubMedia(new FakeQuery(false));
		const { result } = renderHook(() => useHoverCapable());
		expect(result.current).toBe(false);
	});

	test("follows the pointer changing while mounted and stops after unmount", () => {
		const query = new FakeQuery(false);
		stubMedia(query);
		const { result, unmount } = renderHook(() => useHoverCapable());
		act(() => query.flip(true));
		expect(result.current).toBe(true);
		unmount();
		act(() => query.flip(false));
		expect(result.current).toBe(true);
	});

	test("is false where matchMedia does not exist", () => {
		// @ts-expect-error simulating an old browser
		window.matchMedia = undefined;
		const { result } = renderHook(() => useHoverCapable());
		expect(result.current).toBe(false);
	});
});

describe("useTouchCapable", () => {
	test("is true when any pointer is coarse", () => {
		stubTouchPoints(0);
		const asked = stubMedia(new FakeQuery(true));
		const { result } = renderHook(() => useTouchCapable());
		expect(result.current).toBe(true);
		expect(asked).toEqual(["(any-pointer: coarse)"]);
	});

	test("is true on an iPad that disguises its media queries but reports touch points", () => {
		stubMedia(new FakeQuery(false));
		stubTouchPoints(5);
		const { result } = renderHook(() => useTouchCapable());
		expect(result.current).toBe(true);
	});

	test("is false with neither a coarse pointer nor touch points", () => {
		stubMedia(new FakeQuery(false));
		stubTouchPoints(0);
		const { result } = renderHook(() => useTouchCapable());
		expect(result.current).toBe(false);
	});

	test("follows a pointer being attached while mounted", () => {
		stubTouchPoints(0);
		const query = new FakeQuery(false);
		stubMedia(query);
		const { result, unmount } = renderHook(() => useTouchCapable());
		act(() => query.flip(true));
		expect(result.current).toBe(true);
		unmount();
		act(() => query.flip(false));
		expect(result.current).toBe(true);
	});

	test("falls back to touch points where matchMedia does not exist", () => {
		// @ts-expect-error simulating an old browser
		window.matchMedia = undefined;
		stubTouchPoints(2);
		const { result } = renderHook(() => useTouchCapable());
		expect(result.current).toBe(true);
	});
});
