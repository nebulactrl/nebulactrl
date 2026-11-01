import { describe, expect, test } from "bun:test";
import { blurs, distances, durations, eases, exact, scales } from "./index.ts";

const css = await Bun.file(new URL("./motion.css", import.meta.url)).text();

/** The value of `--name: value;` in motion.css, without the trailing comment. */
function token(name: string): string {
	const match = new RegExp(`--${name}:\\s*([^;]+);`).exec(css);
	if (!match) throw new Error(`--${name} is missing from motion.css`);
	return (match[1] ?? "").trim();
}

const kebab = (key: string) =>
	key.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`);

describe("motion tokens", () => {
	test("durations match motion.css", () => {
		for (const [key, ms] of Object.entries(durations))
			expect(token(`duration-${kebab(key)}`)).toBe(`${ms}ms`);
	});

	test("eases match motion.css", () => {
		for (const [key, value] of Object.entries(eases))
			expect(token(`ease-${kebab(key)}`)).toBe(value);
	});

	test("distances, scales and blurs match motion.css", () => {
		for (const [key, px] of Object.entries(distances))
			expect(token(`distance-${key}`)).toBe(`${px}px`);
		for (const [key, value] of Object.entries(scales))
			expect(token(`scale-${key}`)).toBe(String(value));
		for (const [key, px] of Object.entries(blurs))
			expect(token(`blur-${key}`)).toBe(`${px}px`);
	});

	test("better-ui exact values match motion.css", () => {
		expect(token("press-scale")).toBe(String(exact.press.scale));
		expect(token("press-duration")).toBe(`${exact.press.duration}ms`);
		expect(token("key-travel")).toBe(`${exact.key.travel}px`);
		expect(token("key-press-scale")).toBe(String(exact.key.scale));
		expect(token("enter-duration")).toBe(`${exact.enter.duration}ms`);
		expect(token("enter-rise")).toBe(`${exact.enter.rise}px`);
		expect(token("enter-blur")).toBe(`${exact.enter.blur}px`);
		expect(token("enter-stagger")).toBe(`${exact.enter.stagger}ms`);
		expect(token("exit-duration")).toBe(`${exact.exit.duration}ms`);
		expect(token("exit-rise")).toBe(`${exact.exit.rise}px`);
		expect(token("icon-swap-duration")).toBe(`${exact.iconSwap.duration}ms`);
		expect(token("icon-swap-ease")).toBe(exact.iconSwap.ease);
		expect(token("icon-swap-scale-from")).toBe(
			String(exact.iconSwap.scaleFrom),
		);
	});

	test("press never goes below 0.96", () => {
		expect(exact.press.scale).toBe(0.96);
	});
});

const halftone = await Bun.file(
	new URL("./halftone.css", import.meta.url),
).text();

describe("halftone", () => {
	test("the ambient dials are declared once, here", () => {
		for (const name of [
			"strength",
			"dawn-peak",
			"corner-peak",
			"band-peak",
			"reach",
			"ink",
			"alpha-canvas",
			"alpha-page",
			"alpha-band",
		])
			expect(halftone).toMatch(new RegExp(`--ambient-${name}:`));
	});

	test("every host utility is defined, and the one layer draws them all", () => {
		for (const host of [
			"bg-ambient",
			"ht-dawn",
			"ht-arc",
			"ht-foot",
			"ht-sphere",
		])
			expect(halftone).toContain(`@utility ${host} {`);
		expect(halftone.match(/\.ht-layer\)::before \{/g)?.length).toBe(2);
	});

	test("the layer is hidden under forced colours", () => {
		expect(halftone).toMatch(
			/@media \(forced-colors: active\) \{[^}]*::before \{\s*display: none;/,
		);
	});
});
