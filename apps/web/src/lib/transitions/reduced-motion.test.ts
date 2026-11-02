import { afterEach, describe, expect, test } from "bun:test";
import { prefersReducedMotion } from "./reduced-motion";

const realMatchMedia = window.matchMedia;

afterEach(() => {
	window.matchMedia = realMatchMedia;
});

function stub(matches: boolean): string[] {
	const asked: string[] = [];
	window.matchMedia = ((query: string) => {
		asked.push(query);
		return { matches };
	}) as unknown as typeof window.matchMedia;
	return asked;
}

describe("prefersReducedMotion", () => {
	test("reads the reduced-motion preference at the moment it is called", () => {
		const asked = stub(true);
		expect(prefersReducedMotion()).toBe(true);
		expect(asked).toEqual(["(prefers-reduced-motion: reduce)"]);
		stub(false);
		expect(prefersReducedMotion()).toBe(false);
	});

	test("is false where matchMedia does not exist", () => {
		// @ts-expect-error simulating a browser without matchMedia
		window.matchMedia = undefined;
		expect(prefersReducedMotion()).toBe(false);
	});
});
