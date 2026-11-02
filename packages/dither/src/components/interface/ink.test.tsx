import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { renderHook } from "@testing-library/react";
import { resolveInk, useInkRgb } from "./ink";

afterEach(() => {
	document.documentElement.style.cssText = "";
});

describe("resolveInk", () => {
	test("reads a hex colour as 0-255 channels", () => {
		expect(resolveInk("#ff8000")).toEqual([255, 128, 0]);
	});

	test("leaves no probe element behind in the document", () => {
		const before = document.documentElement.childElementCount;
		resolveInk("#123456");
		expect(document.documentElement.childElementCount).toBe(before);
	});

	test("an unparseable colour reads as mid grey", () => {
		expect(resolveInk("not-a-colour")).toEqual([128, 128, 128]);
	});

	test("asks the browser's cascade for the computed colour", () => {
		const spy = spyOn(window, "getComputedStyle").mockReturnValue({
			color: "rgb(10, 20, 30)",
		} as CSSStyleDeclaration);
		try {
			resolveInk("var(--made-up)");
			expect(spy).toHaveBeenCalledTimes(1);
		} finally {
			spy.mockRestore();
		}
	});
});

describe("useInkRgb", () => {
	test("resolves an explicit hex colour", () => {
		const { result } = renderHook(() => useInkRgb("#00ff00"));
		expect(result.current).toEqual([0, 255, 0]);
	});

	test("follows a changed colour prop", () => {
		const { result, rerender } = renderHook(
			({ color }: { color: string }) => useInkRgb(color),
			{ initialProps: { color: "#ff0000" } },
		);
		expect(result.current).toEqual([255, 0, 0]);
		rerender({ color: "#0000ff" });
		expect(result.current).toEqual([0, 0, 255]);
	});

	test("returns a stable array while nothing it depends on changes", () => {
		const { result, rerender } = renderHook(() => useInkRgb("#00ff00"));
		const first = result.current;
		rerender();
		expect(result.current).toBe(first);
	});
});
