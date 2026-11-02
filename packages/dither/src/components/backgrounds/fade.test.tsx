import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { DitherFade } from "./fade";
import {
	installProbe,
	lastShader,
	lastUniforms,
	resetProbe,
	restoreProbe,
} from "./shader-probe";

// The animated fade hands a drifting field to `DitherShader`; the still fade
// is plain CSS. A recording stand-in for the shader shows what the animated
// one asks for.
installProbe();
afterAll(restoreProbe);
beforeEach(resetProbe);

function fade(ui: React.ReactElement): HTMLElement {
	const { container } = render(ui);
	const el = container.firstElementChild;
	if (!(el instanceof HTMLElement)) throw new Error("nothing rendered");
	return el;
}

/** The SVG the mask image carries, decoded. */
function maskSvg(el: HTMLElement): string {
	const match = /data:image\/svg\+xml,([^"']+)/.exec(el.style.maskImage);
	if (!match?.[1]) throw new Error(`no mask image in ${el.style.maskImage}`);
	return decodeURIComponent(match[1]);
}

const bandCount = (el: HTMLElement) =>
	maskSvg(el).match(/<rect /g)?.length ?? 0;

describe("still fade", () => {
	test("defaults: a decorative bottom edge 160px tall in paper colour", () => {
		const el = fade(<DitherFade />);
		expect(el.tagName).toBe("DIV");
		expect(el).toHaveAttribute("aria-hidden", "true");
		expect(el).toHaveClass(
			"pointer-events-none",
			"absolute",
			"inset-x-0",
			"bottom-0",
		);
		expect(el).toHaveClass("forced-colors:hidden");
		expect(el.style.height).toBe("160px");
		expect(el.style.width).toBe("");
		expect(el.style.backgroundColor).toBe("var(--dither-paper)");
		expect(el.querySelector("canvas")).toBeNull();
	});

	test("the mask is a bayer4 ramp at the default 3px cell, anchored to the bottom edge", () => {
		const el = fade(<DitherFade />);
		expect(el.style.maskSize).toBe("12px 160px");
		expect(el.style.maskRepeat).toBe("repeat-x");
		expect(el.style.maskPosition).toBe("0 100%");
		expect(bandCount(el)).toBe(16);
	});

	test.each([
		["top", ["inset-x-0", "top-0"], "height", "12px 160px", "repeat-x", "0 0"],
		[
			"bottom",
			["inset-x-0", "bottom-0"],
			"height",
			"12px 160px",
			"repeat-x",
			"0 100%",
		],
		["left", ["inset-y-0", "left-0"], "width", "160px 12px", "repeat-y", "0 0"],
		[
			"right",
			["inset-y-0", "right-0"],
			"width",
			"160px 12px",
			"repeat-y",
			"100% 0",
		],
	] as const)(
		"%s edge: placement, sized %s, mask %s %s %s",
		(side, classes, dimension, size, repeat, position) => {
			const el = fade(<DitherFade side={side} />);
			expect(el).toHaveClass(...classes);
			expect(el.style[dimension]).toBe("160px");
			expect(el.style[dimension === "width" ? "height" : "width"]).toBe("");
			expect(el.style.maskSize).toBe(size);
			expect(el.style.maskRepeat).toBe(repeat);
			expect(el.style.maskPosition).toBe(position);
		},
	);

	test("size takes a number of px or any CSS length", () => {
		expect(fade(<DitherFade size={64} />).style.height).toBe("64px");
		const el = fade(<DitherFade side="left" size="30%" />);
		expect(el.style.width).toBe("30%");
		// A percentage length ramps over the whole box, so the image spans all of it.
		expect(el.style.maskSize).toBe("100% 12px");
	});

	test("a token or a CSS colour fills the edge", () => {
		expect(fade(<DitherFade color="blue" />).style.backgroundColor).toBe(
			"var(--dither-blue)",
		);
		expect(fade(<DitherFade color="--surface" />).style.backgroundColor).toBe(
			"var(--surface)",
		);
		expect(fade(<DitherFade color="#112233" />).style.backgroundColor).toBe(
			"#112233",
		);
	});

	test("the matrix sets the tile size and the number of bands; `matrix` beats `n`", () => {
		const bayer2 = fade(<DitherFade n={2} cell={2} />);
		expect(bayer2.style.maskSize).toBe("4px 160px");
		expect(bandCount(bayer2)).toBe(4);
		const bayer8 = fade(<DitherFade matrix="bayer8" cell={2} />);
		expect(bayer8.style.maskSize).toBe("16px 160px");
		expect(bandCount(bayer8)).toBe(16);
		const both = fade(<DitherFade matrix="bayer8" n={2} />);
		expect(both.style.maskSize).toBe("24px 160px");
		expect(maskSvg(both)).not.toBe(maskSvg(bayer2));
	});

	test("blue noise is a 16x16 tile with sixteen bands", () => {
		const el = fade(<DitherFade matrix="blue-noise" cell={1} />);
		expect(el.style.maskSize).toBe("16px 160px");
		expect(bandCount(el)).toBe(16);
	});

	test("className merges with the defaults and wins conflicts; style overrides the size", () => {
		const el = fade(
			<DitherFade
				className="z-10 bottom-4"
				style={{ height: 40, opacity: 0.5 }}
			/>,
		);
		expect(el).toHaveClass("z-10", "bottom-4", "pointer-events-none");
		expect(el).not.toHaveClass("bottom-0");
		expect(el.style.height).toBe("40px");
		expect(el.style.opacity).toBe("0.5");
	});

	test("never reaches the shader", () => {
		fade(<DitherFade />);
		expect(() => lastShader()).toThrow("no shader was rendered");
	});
});

describe("animated fade", () => {
	test("hands a drifting field and a transparent to colour ramp to the shader", () => {
		render(<DitherFade animate color="blue" />);
		const call = lastShader();
		expect(call.palette).toEqual(["transparent", "blue"]);
		expect(call.field).toContain("float field(vec2 uv, float t)");
		expect(call.matrix).toBe("bayer4");
		expect(call.staticTime).toBe(2);
		expect(call["aria-hidden"]).toBe(true);
		expect(call.cell).toBeUndefined();
	});

	test("the drift runs towards the page edge of the chosen side", () => {
		const dir = (side: "top" | "bottom" | "left" | "right") => {
			render(<DitherFade animate side={side} />);
			return lastUniforms().fdDir;
		};
		expect(dir("bottom")).toEqual([0, -1]);
		expect(dir("top")).toEqual([0, 1]);
		expect(dir("left")).toEqual([-1, 0]);
		expect(dir("right")).toEqual([1, 0]);
	});

	test("sizes and places the shader's box like the still fade", () => {
		render(
			<DitherFade
				animate
				side="right"
				size={90}
				className="z-5"
				style={{ opacity: 0.3 }}
			/>,
		);
		const call = lastShader();
		expect(call.className).toContain("inset-y-0");
		expect(call.className).toContain("right-0");
		expect(call.className).toContain("z-5");
		expect(call.className).toContain("pointer-events-none");
		expect(call.style).toEqual({ width: 90, opacity: 0.3 });
	});

	test("vertical sides set the height", () => {
		render(<DitherFade animate side="top" size="20%" />);
		expect(lastShader().style).toEqual({ height: "20%" });
	});

	test("matrix, n and cell pass through", () => {
		render(<DitherFade animate n={8} cell={5} />);
		expect(lastShader().matrix).toBe("bayer8");
		expect(lastShader().cell).toBe(5);
		render(<DitherFade animate n={8} matrix="blue-noise" />);
		expect(lastShader().matrix).toBe("blue-noise");
	});
});
