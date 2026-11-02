import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { AreaFill } from "./area-fill";

const top = [
	{ x: 0, y: 20 },
	{ x: 50, y: 10 },
	{ x: 100, y: 30 },
];

const paths = (container: HTMLElement) => [
	...container.querySelectorAll("g[clip-path] > path"),
];

const draw = (props: Partial<Parameters<typeof AreaFill>[0]> = {}) =>
	render(
		<svg>
			<title>t</title>
			<AreaFill top={top} bottom={100} color="#ff0000" floor={100} {...props} />
		</svg>,
	);

describe("AreaFill", () => {
	test("draws nothing for fewer than two points", () => {
		const { container } = render(
			<svg>
				<title>t</title>
				<AreaFill top={[{ x: 0, y: 1 }]} bottom={100} color="red" floor={100} />
			</svg>,
		);
		expect(container.querySelector("path")).toBeNull();
	});

	test("the clip region runs from the line down to a numeric baseline", () => {
		const { container } = draw();
		const clip = container.querySelector("clipPath path");
		const d = clip?.getAttribute("d") ?? "";
		expect(d.startsWith("M0 20")).toBe(true);
		expect(d).toContain("100 30");
		expect(d).toContain("100 100");
		expect(d).toContain("0 100");
	});

	test("a lower edge given as points bounds the region from below", () => {
		const { container } = draw({
			bottom: [
				{ x: 0, y: 60 },
				{ x: 50, y: 55 },
				{ x: 100, y: 70 },
			],
		});
		const d = container.querySelector("clipPath path")?.getAttribute("d") ?? "";
		expect(d).toContain("50 55");
		expect(d).not.toContain("100 100");
	});

	test("the gradient draws one dither band per tone, all clipped to the region", () => {
		const { container } = draw({ bands: 5 });
		const bands = paths(container);
		expect(bands).toHaveLength(5);
		const clipId = container.querySelector("clipPath")?.id;
		expect(container.querySelector("g[clip-path]")).toHaveAttribute(
			"clip-path",
			`url(#${clipId})`,
		);
		const fills = bands.map((p) => p.getAttribute("fill"));
		expect(new Set(fills).size).toBe(5);
		for (const f of fills) expect(f).toMatch(/^url\(#.+\)$/);
	});

	test("the last band reaches down to the floor", () => {
		const { container } = draw({ floor: 400, depth: 30 });
		const last = paths(container).at(-1)?.getAttribute("d") ?? "";
		expect(last).toContain("400");
	});

	test("dotted and hatched use a single flat pattern over the whole region", () => {
		const dotted = draw({ variant: "dotted" });
		expect(paths(dotted.container)).toHaveLength(1);
		const dottedFill = paths(dotted.container)[0]?.getAttribute("fill");
		dotted.unmount();
		const hatched = draw({ variant: "hatched" });
		const hatchedFill = paths(hatched.container)[0]?.getAttribute("fill");
		expect(dottedFill).toMatch(/^url\(#/);
		expect(hatchedFill).toMatch(/^url\(#/);
		expect(hatchedFill).not.toBe(dottedFill);
	});

	test("solid, forced-solid and the solid variant fall back to a translucent tint", () => {
		for (const props of [{ solid: true }, { variant: "solid" as const }]) {
			const { container, unmount } = draw(props);
			const body = paths(container);
			expect(body).toHaveLength(1);
			expect(body[0]?.getAttribute("fill")).toBe(
				"color-mix(in oklab, #ff0000 26%, transparent)",
			);
			unmount();
		}
	});

	test("bloom adds a blurred solid copy of the region; off adds none", () => {
		const off = draw();
		expect(off.container.querySelector("feGaussianBlur")).toBeNull();
		off.unmount();
		const { container } = draw({ bloom: "low" });
		expect(container.querySelector("feGaussianBlur")).not.toBeNull();
		expect(container.querySelector("g[filter] path")).toHaveAttribute(
			"fill",
			"#ff0000",
		);
	});

	test("is decorative: hidden from assistive tech", () => {
		const { container } = draw();
		expect(container.querySelector("svg > g")).toHaveAttribute(
			"aria-hidden",
			"true",
		);
	});
});
