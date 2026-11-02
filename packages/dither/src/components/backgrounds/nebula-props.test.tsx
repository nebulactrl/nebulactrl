import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { DitherNebula } from "./nebula";
import {
	installProbe,
	lastShader,
	lastUniforms,
	resetProbe,
	restoreProbe,
} from "./shader-probe";

// What `DitherNebula` hands the shader, read through a recording stand-in.
installProbe();
afterAll(restoreProbe);
beforeEach(resetProbe);

const DEFAULT_PALETTE = [
	"--dither-paper",
	"#3a36b0",
	"#6d6bff",
	"#ff5cc6",
	"#5fe3ff",
	"--dither-ink",
];

describe("DitherNebula shader props", () => {
	test("defaults: fine blue-noise cells, a slow clock, the six-colour palette", () => {
		render(<DitherNebula />);
		const call = lastShader();
		expect(call.palette).toEqual(DEFAULT_PALETTE);
		expect(call.cell).toBe(2);
		expect(call.speed).toBe(0.08);
		expect(call.matrix).toBe("blue-noise");
		expect(call.levels).toBe(256);
		expect(call.staticTime).toBe(12);
		expect(call.pointer).toBe(false);
		expect(call["aria-hidden"]).toBe(true);
		expect(call.field).toContain("float field(vec2 uv, float t)");
		expect(call.colorField).toContain("vec3 colorField(");
	});

	test("default uniforms: full gas, no mask, no focus and no clear boxes", () => {
		render(<DitherNebula />);
		expect(lastUniforms()).toEqual({
			nbIntensity: 1,
			nbDensity: 0.6,
			nbWarp: 1,
			nbStars: 1,
			nbMask: 0,
			nbMaskSize: 0.5,
			nbScroll: 0,
			nbParallax: 0,
			nbFocus: [0.5, 0.5, 0, 0],
			nbFocusGate: [-1, 0],
			nbFocusFloor: [-1, -0.5],
			clearRect0: [0, 0, 0, 0],
			clearRect1: [0, 0, 0, 0],
			clearRect2: [0, 0, 0, 0],
			clearRect3: [0, 0, 0, 0],
			clearShape01: [0, 0, 0, 0],
			clearShape23: [0, 0, 0, 0],
			clearN: 0,
		});
	});

	test("a short palette keeps its colours and takes the rest from the defaults", () => {
		render(<DitherNebula palette={["black", "navy"]} />);
		expect(lastShader().palette).toEqual([
			"black",
			"navy",
			...DEFAULT_PALETTE.slice(2),
		]);
	});

	test("numeric knobs are clamped to their range", () => {
		render(
			<DitherNebula
				intensity={9}
				density={3}
				warp={5}
				stars={-1}
				maskSize={4}
				scroll={2}
				parallax={7}
			/>,
		);
		expect(lastUniforms()).toMatchObject({
			nbIntensity: 1.5,
			nbDensity: 1,
			nbWarp: 2,
			nbStars: 0,
			nbMaskSize: 1,
			nbScroll: 1,
			nbParallax: 2,
		});
		render(
			<DitherNebula
				intensity={-1}
				density={-1}
				warp={-1}
				stars={5}
				maskSize={-1}
				scroll={-1}
			/>,
		);
		expect(lastUniforms()).toMatchObject({
			nbIntensity: 0,
			nbDensity: 0,
			nbWarp: 0,
			nbStars: 2,
			nbMaskSize: 0,
			nbScroll: 0,
		});
		render(
			<DitherNebula
				intensity={0.7}
				density={0.2}
				warp={1.5}
				stars={1.2}
				maskSize={0.3}
				scroll={0.4}
				parallax={1.2}
			/>,
		);
		expect(lastUniforms()).toMatchObject({
			nbIntensity: 0.7,
			nbDensity: 0.2,
			nbWarp: 1.5,
			nbStars: 1.2,
			nbMaskSize: 0.3,
			nbScroll: 0.4,
			nbParallax: 1.2,
		});
	});

	test("mask names map to shader indices", () => {
		const index = { none: 0, center: 1, top: 2, bottom: 3, edges: 4 } as const;
		for (const [mask, value] of Object.entries(index)) {
			render(<DitherNebula mask={mask as keyof typeof index} />);
			expect(lastUniforms().nbMask, mask).toBe(value);
		}
	});

	test("parallax turns pointer tracking on", () => {
		render(<DitherNebula parallax={0.5} />);
		expect(lastShader().pointer).toBe(true);
		render(<DitherNebula parallax={0} />);
		expect(lastShader().pointer).toBe(false);
	});

	test("focus becomes a y-up ellipse, clamped, with room left in the gates", () => {
		render(<DitherNebula focus={{ x: 0.3, y: 0.25, rx: 0.4, ry: 0.2 }} />);
		expect(lastUniforms().nbFocus).toEqual([0.3, 0.75, 0.4, 0.2]);
		expect(lastUniforms().nbFocusGate).toEqual([-1, 0]);
		expect(lastUniforms().nbFocusFloor).toEqual([-1, -0.5]);
		render(<DitherNebula focus={{ x: 2, y: -1, rx: 9, ry: -3 }} />);
		expect(lastUniforms().nbFocus).toEqual([1, 1, 4, 0.001]);
	});

	test("`from` and `to` set the left to right gate", () => {
		render(
			<DitherNebula
				focus={{ x: 0.5, y: 0.5, rx: 1, ry: 1, from: 0.2, to: 0.6 }}
			/>,
		);
		expect(lastUniforms().nbFocusGate).toEqual([0.2, 0.6]);
		render(<DitherNebula focus={{ x: 0.5, y: 0.5, rx: 1, ry: 1, from: 0 }} />);
		expect(lastUniforms().nbFocusGate).toEqual([0, 0]);
	});

	test("`floor` and `floorFade` become y-up edges; a missing fade is the smallest one", () => {
		render(
			<DitherNebula
				focus={{ x: 0.5, y: 0.5, rx: 1, ry: 1, floor: 0.8, floorFade: 0.2 }}
			/>,
		);
		const [lo, hi] = lastUniforms().nbFocusFloor as [number, number];
		expect(lo).toBeCloseTo(0.2, 10);
		expect(hi).toBeCloseTo(0.4, 10);
		render(
			<DitherNebula focus={{ x: 0.5, y: 0.5, rx: 1, ry: 1, floor: 0.5 }} />,
		);
		const [lo2, hi2] = lastUniforms().nbFocusFloor as [number, number];
		expect(lo2).toBe(0.5);
		expect(hi2).toBeCloseTo(0.501, 10);
		render(
			<DitherNebula
				focus={{ x: 0.5, y: 0.5, rx: 1, ry: 1, floor: 0.5, floorFade: -1 }}
			/>,
		);
		expect(lastUniforms().nbFocusFloor).toEqual([0.5, 1 - (0.5 - 0.001)]);
	});

	test("clear boxes reach the shader in CSS px, up to four", () => {
		render(
			<DitherNebula
				clear={[
					{ x: 10, y: 20, w: 30, h: 40, radius: 4, feather: 8 },
					{ x: 1, y: 2, w: 3, h: 4, radius: 5, feather: 6 },
				]}
			/>,
		);
		expect(lastUniforms()).toMatchObject({
			clearRect0: [10, 20, 30, 40],
			clearRect1: [1, 2, 3, 4],
			clearRect2: [0, 0, 0, 0],
			clearShape01: [4, 8, 5, 6],
			clearN: 2,
		});
		const box = { x: 0, y: 0, w: 1, h: 1, radius: 0, feather: 0 };
		render(<DitherNebula clear={[box, box, box, box, box]} />);
		expect(lastUniforms().clearN).toBe(4);
	});

	test("engine props pass through; `matrix`, `n`, `levels` and `stillTime` override the defaults", () => {
		const fallback = <b>custom</b>;
		render(
			<DitherNebula
				cell={5}
				speed={0.5}
				fps={12}
				animate={false}
				shape="dot"
				className="bg-z"
				style={{ opacity: 0.2 }}
				fallback={fallback}
				n={4}
				levels={7}
				stillTime={3}
			/>,
		);
		const call = lastShader();
		expect(call).toMatchObject({
			cell: 5,
			speed: 0.5,
			fps: 12,
			animate: false,
			shape: "dot",
			className: "bg-z",
			matrix: "bayer4",
			levels: 7,
			staticTime: 3,
		});
		expect(call.style).toEqual({ opacity: 0.2 });
		expect(call.fallback).toBe(fallback);
		render(<DitherNebula matrix="bayer8" n={2} />);
		expect(lastShader().matrix).toBe("bayer8");
		expect(lastShader().levels).toBe(65);
	});

	test("without a custom fallback the shader gets the nebula's own", () => {
		render(<DitherNebula />);
		const { container } = render(<>{lastShader().fallback}</>);
		expect(container.querySelector("div.absolute.inset-0")).toHaveStyle({
			maskComposite: "intersect",
		});
		expect(
			(container.firstElementChild as HTMLElement).style.backgroundImage,
		).toContain("radial-gradient");
	});
});
