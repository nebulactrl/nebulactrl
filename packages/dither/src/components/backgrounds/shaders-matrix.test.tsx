import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { DitherAurora } from "./aurora";
import { DitherBeams } from "./beams";
import { DitherContours } from "./contours";
import { DitherGlow, GLOW_POSITIONS } from "./glow";
import { DitherGradient } from "./gradient";
import { DitherMeshGradient } from "./mesh-gradient";
import { DitherNoiseField } from "./noise-field";
import { DitherPlasma } from "./plasma";
import { DitherRipples } from "./ripples";
import {
	installProbe,
	lastShader,
	lastUniforms,
	resetProbe,
	restoreProbe,
} from "./shader-probe";
import type { BackgroundProps } from "./shared";
import { DitherSilk } from "./silk";
import { DitherSmoke } from "./smoke";
import { DitherWaves } from "./waves";

// The twelve backgrounds that resolve `matrix`/`n`/`levels`/`stillTime`
// through `resolveMatrix` and forward everything else to the shader. They are
// observed through a recording stand-in for `DitherShader`.
installProbe();
afterAll(restoreProbe);
beforeEach(resetProbe);

const PAPER = "--dither-paper";
const INK = "--dither-ink";
const mix = (percent: number) =>
	`color-mix(in oklch, var(${INK}) ${percent}%, var(${PAPER}))`;
const DEG = Math.PI / 180;

type Common = BackgroundProps<object>;

interface Family {
	name: string;
	render: (props: Common) => ReactElement;
	/** Matrix when neither `matrix` nor `n` is given. */
	matrix: string;
	staticTime: number;
	/** Levels when none are given: "full" resolves to the matrix's tone count. */
	levels: number;
}

const FAMILIES: Family[] = [
	{
		name: "gradient",
		render: (p) => <DitherGradient {...p} />,
		matrix: "bayer4",
		staticTime: 0,
		levels: 17,
	},
	{
		name: "glow",
		render: (p) => <DitherGlow {...p} />,
		matrix: "bayer8",
		staticTime: 0,
		levels: 65,
	},
	{
		name: "mesh-gradient",
		render: (p) => <DitherMeshGradient {...p} />,
		matrix: "blue-noise",
		staticTime: 8,
		levels: 256,
	},
	{
		name: "aurora",
		render: (p) => <DitherAurora {...p} />,
		matrix: "bayer8",
		staticTime: 40,
		levels: 65,
	},
	{
		name: "noise-field",
		render: (p) => <DitherNoiseField {...p} />,
		matrix: "bayer8",
		staticTime: 0,
		levels: 65,
	},
	{
		name: "waves",
		render: (p) => <DitherWaves {...p} />,
		matrix: "bayer4",
		staticTime: 6,
		levels: 17,
	},
	{
		name: "plasma",
		render: (p) => <DitherPlasma {...p} />,
		matrix: "bayer4",
		staticTime: 5,
		levels: 17,
	},
	{
		name: "beams",
		render: (p) => <DitherBeams {...p} />,
		matrix: "bayer4",
		staticTime: 3,
		levels: 17,
	},
	{
		name: "silk",
		render: (p) => <DitherSilk {...p} />,
		matrix: "bayer8",
		staticTime: 6,
		levels: 65,
	},
	{
		name: "smoke",
		render: (p) => <DitherSmoke {...p} />,
		matrix: "bayer4",
		staticTime: 8,
		levels: 17,
	},
	{
		name: "contours",
		render: (p) => <DitherContours {...p} />,
		matrix: "bayer4",
		staticTime: 4,
		levels: 12,
	},
	{
		name: "ripples",
		render: (p) => <DitherRipples {...p} />,
		matrix: "bayer4",
		staticTime: 4,
		levels: 17,
	},
];

describe.each(FAMILIES)("$name: shared shader contract", (family) => {
	test("starts from its own matrix, still moment and tone levels", () => {
		render(family.render({}));
		const call = lastShader();
		expect(call.matrix).toBe(family.matrix as never);
		expect(call.staticTime).toBe(family.staticTime);
		expect(call.levels).toBe(family.levels);
		expect(call.n).toBeUndefined();
		expect(call.field).toContain("float field(vec2 uv, float t)");
	});

	test("`n` picks the Bayer matrix and the full tone count follows it", () => {
		render(family.render({ n: 2 }));
		expect(lastShader().matrix).toBe("bayer2");
		if (family.name !== "contours") expect(lastShader().levels).toBe(5);
		else expect(lastShader().levels).toBe(12);
		expect(lastShader().n).toBeUndefined();
	});

	test("`matrix` wins over `n`", () => {
		render(family.render({ matrix: "blue-noise", n: 8 }));
		expect(lastShader().matrix).toBe("blue-noise");
	});

	test("explicit levels win over the default, `stops` passes through", () => {
		render(family.render({ levels: 7 }));
		expect(lastShader().levels).toBe(7);
		render(family.render({ levels: "stops" }));
		expect(lastShader().levels).toBe("stops");
	});

	test("`stillTime` wins over `staticTime`, which wins over the default", () => {
		render(family.render({ staticTime: 9 }));
		expect(lastShader().staticTime).toBe(9);
		render(family.render({ staticTime: 9, stillTime: 2.5 }));
		expect(lastShader().staticTime).toBe(2.5);
		// `stillTime` is folded into `staticTime`, never forwarded.
		expect(lastShader().stillTime).toBeUndefined();
	});

	test("forwards the engine props untouched", () => {
		const fallback = <i data-testid="fb">no gl</i>;
		render(
			family.render({
				className: "bg-x",
				cell: 5,
				shape: "dot",
				fps: 12,
				animate: false,
				speed: 0.5,
				style: { opacity: 0.5 },
				fallback,
				"aria-hidden": false,
			}),
		);
		const call = lastShader();
		expect(call.className).toBe("bg-x");
		expect(call.cell).toBe(5);
		expect(call.shape).toBe("dot");
		expect(call.fps).toBe(12);
		expect(call.animate).toBe(false);
		expect(call.speed).toBe(0.5);
		expect(call.style).toEqual({ opacity: 0.5 });
		expect(call.fallback).toBe(fallback);
		expect(call["aria-hidden"]).toBe(false);
	});

	test("every uniform is a finite number or a number tuple", () => {
		render(family.render({}));
		const entries = Object.entries(lastUniforms());
		expect(entries.length).toBeGreaterThan(0);
		for (const [key, value] of entries) {
			const numbers = Array.isArray(value) ? value : [value];
			expect(numbers.length, key).toBeGreaterThan(0);
			for (const n of numbers) expect(Number.isFinite(n), key).toBe(true);
			// Every uniform the shader declares is read by the field.
			expect(lastShader().field, key).toContain(key);
		}
	});
});

describe("DitherGradient", () => {
	test("defaults: a linear paper to ink ramp pointing down", () => {
		render(<DitherGradient />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({
			grMode: 0,
			grAngle: 180 * DEG,
			grCenter: [0.5, 0.5],
			grRadius: 1,
			grFrom: 0,
			grTo: 1,
			grNoise: 0.12,
		});
	});

	test("mode picks the shader branch; conic starts at 0 degrees, the others at 180", () => {
		render(<DitherGradient mode="radial" />);
		expect(lastUniforms()).toMatchObject({ grMode: 1, grAngle: Math.PI });
		render(<DitherGradient mode="conic" />);
		expect(lastUniforms()).toMatchObject({ grMode: 2, grAngle: 0 });
	});

	test("an explicit angle is converted to radians, even 0 on a linear gradient", () => {
		render(<DitherGradient mode="conic" angle={90} />);
		expect(lastUniforms().grAngle).toBeCloseTo(Math.PI / 2, 10);
		render(<DitherGradient angle={0} />);
		expect(lastUniforms().grAngle).toBe(0);
	});

	test("centre, radius, range and noise reach the shader; palette is replaceable", () => {
		render(
			<DitherGradient
				center={[0.2, 0.9]}
				radius={0.4}
				from={0.1}
				to={0.8}
				noise={0.5}
				palette={["red", "blue"]}
			/>,
		);
		expect(lastShader().palette).toEqual(["red", "blue"]);
		expect(lastUniforms()).toMatchObject({
			grCenter: [0.2, 0.9],
			grRadius: 0.4,
			grFrom: 0.1,
			grTo: 0.8,
			grNoise: 0.5,
		});
	});
});

describe("DitherGlow", () => {
	test("defaults: a paper to ink glow centred above the top edge", () => {
		render(<DitherGlow />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({
			glPos: [0.5, -0.08],
			glRadius: 0.7,
			glIntensity: 0.75,
		});
	});

	test("names the nine anchor positions", () => {
		expect(GLOW_POSITIONS).toEqual([
			"top",
			"bottom",
			"left",
			"right",
			"center",
			"top-left",
			"top-right",
			"bottom-left",
			"bottom-right",
		]);
		const expected: Record<string, [number, number]> = {
			top: [0.5, -0.08],
			bottom: [0.5, 1.08],
			left: [-0.08, 0.5],
			right: [1.08, 0.5],
			center: [0.5, 0.5],
			"top-left": [-0.05, -0.05],
			"top-right": [1.05, -0.05],
			"bottom-left": [-0.05, 1.05],
			"bottom-right": [1.05, 1.05],
		};
		for (const position of GLOW_POSITIONS) {
			render(<DitherGlow position={position} />);
			expect(lastUniforms().glPos, position).toEqual(
				expected[position] as [number, number],
			);
		}
	});

	test("a [x, y] tuple places the glow anywhere", () => {
		render(
			<DitherGlow
				position={[0.25, 0.75]}
				radius={1.2}
				intensity={0.3}
				palette={["#000", "#fff"]}
			/>,
		);
		expect(lastUniforms()).toEqual({
			glPos: [0.25, 0.75],
			glRadius: 1.2,
			glIntensity: 0.3,
		});
		expect(lastShader().palette).toEqual(["#000", "#fff"]);
	});
});

describe("DitherMeshGradient", () => {
	test("defaults: four blobs on a ramp of paper then brightening ink tints", () => {
		render(<DitherMeshGradient />);
		expect(lastShader().palette).toEqual([
			PAPER,
			mix(47.5),
			mix(65),
			mix(82.5),
			mix(100),
		]);
		expect(lastUniforms()).toEqual({ mgBlobs: 4, mgSize: 0.6 });
	});

	test("the ramp has one stop per blob", () => {
		render(<DitherMeshGradient blobs={5} size={1} />);
		expect(lastShader().palette).toEqual([
			PAPER,
			mix(44),
			mix(58),
			mix(72),
			mix(86),
			mix(100),
		]);
		expect(lastUniforms()).toEqual({ mgBlobs: 5, mgSize: 1 });
		render(<DitherMeshGradient blobs={3} />);
		expect(lastShader().palette).toHaveLength(4);
		expect(lastShader().palette?.[3]).toBe(mix(100));
	});

	test("an explicit palette replaces the tint ramp", () => {
		render(<DitherMeshGradient palette={["#111", "#222"]} />);
		expect(lastShader().palette).toEqual(["#111", "#222"]);
	});
});

describe("DitherAurora", () => {
	test("defaults: three curtains rising from a paper to ink ramp", () => {
		render(<DitherAurora />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({
			auCurtains: 3,
			auHeight: 0.42,
			auSpread: 0.11,
			auSway: 1,
			auTurb: 0.6,
			auRays: 8,
			auLength: 0.45,
			auGlow: 0.9,
			auIntensity: 0.95,
			auShimmer: 0.2,
			auScale: 1,
			auTilt: 0,
			auDrift: 0,
			auDown: 0,
			auMask: 0,
		});
	});

	test("a tip colour adds a middle stop between background and colour", () => {
		render(<DitherAurora background="navy" color="lime" tipColor="white" />);
		expect(lastShader().palette).toEqual(["navy", "white", "lime"]);
	});

	test("without a tip colour the background and colour make a two-stop ramp", () => {
		render(<DitherAurora background="navy" color="lime" />);
		expect(lastShader().palette).toEqual(["navy", "lime"]);
	});

	test("an explicit palette wins over colour, tip and background", () => {
		render(
			<DitherAurora palette={["a", "b", "c"]} tipColor="white" color="lime" />,
		);
		expect(lastShader().palette).toEqual(["a", "b", "c"]);
	});

	test("direction, mask, tilt and every shape knob reach the shader", () => {
		render(
			<DitherAurora
				curtains={5}
				height={0.2}
				spread={0.2}
				sway={2}
				turbulence={1}
				rays={12}
				length={0.9}
				glow={1.5}
				intensity={0.5}
				shimmer={0.7}
				scale={2}
				tilt={30}
				drift={-1}
				direction="down"
				mask="edges"
			/>,
		);
		expect(lastUniforms()).toEqual({
			auCurtains: 5,
			auHeight: 0.2,
			auSpread: 0.2,
			auSway: 2,
			auTurb: 1,
			auRays: 12,
			auLength: 0.9,
			auGlow: 1.5,
			auIntensity: 0.5,
			auShimmer: 0.7,
			auScale: 2,
			auTilt: 30 * DEG,
			auDrift: -1,
			auDown: 1,
			auMask: 4,
		});
	});

	test("each mask name maps to its index", () => {
		const index = { none: 0, top: 2, bottom: 3, edges: 4 } as const;
		for (const [mask, value] of Object.entries(index)) {
			render(<DitherAurora mask={mask as keyof typeof index} />);
			expect(lastUniforms().auMask, mask).toBe(value);
		}
	});
});

describe("DitherNoiseField", () => {
	test("defaults: smoke from paper to ink with every knob at its designed value", () => {
		render(<DitherNoiseField />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({
			nfVariant: 0,
			nfScale: 1.3,
			nfWarp: 1.7,
			nfDetail: 4,
			nfContrast: 1.2,
			nfCoverage: 0,
			nfIntensity: 0.9,
			nfBands: 8,
			nfAngle: 90 * DEG,
			nfFlow: 1,
			nfInvert: 0,
			nfMask: 0,
			nfMaskSize: 0.5,
		});
	});

	test("a highlight adds a third stop", () => {
		render(
			<DitherNoiseField background="black" color="gray" highlight="gold" />,
		);
		expect(lastShader().palette).toEqual(["black", "gray", "gold"]);
		render(<DitherNoiseField palette={["x", "y"]} highlight="gold" />);
		expect(lastShader().palette).toEqual(["x", "y"]);
	});

	test("variant, mask and invert map to shader switches", () => {
		const variants = { smoke: 0, ridges: 1, contours: 2, marble: 3 } as const;
		for (const [variant, value] of Object.entries(variants)) {
			render(<DitherNoiseField variant={variant as keyof typeof variants} />);
			expect(lastUniforms().nfVariant, variant).toBe(value);
		}
		render(
			<DitherNoiseField invert mask="center" maskSize={0.9} angle={180} />,
		);
		expect(lastUniforms()).toMatchObject({
			nfInvert: 1,
			nfMask: 1,
			nfMaskSize: 0.9,
			nfAngle: Math.PI,
		});
	});

	test("numeric knobs pass through", () => {
		render(
			<DitherNoiseField
				scale={3}
				warp={0}
				detail={6}
				contrast={2}
				coverage={-0.25}
				intensity={0.4}
				bands={12}
				flow={2}
			/>,
		);
		expect(lastUniforms()).toMatchObject({
			nfScale: 3,
			nfWarp: 0,
			nfDetail: 6,
			nfContrast: 2,
			nfCoverage: -0.25,
			nfIntensity: 0.4,
			nfBands: 12,
			nfFlow: 2,
		});
	});
});

describe("DitherWaves", () => {
	test("ocean (default) uses a four stop ramp and layered sea uniforms", () => {
		render(<DitherWaves />);
		expect(lastShader().palette).toEqual([PAPER, mix(35), mix(65), INK]);
		expect(lastUniforms()).toEqual({
			wvVariant: 0,
			wvHorizon: 0.62,
			wvLayers: 6,
			wvAmp: 1,
		});
	});

	test("lines variant uses a two stop ramp and its own switch", () => {
		render(<DitherWaves variant="lines" horizon={0.3} amplitude={2} />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({
			wvVariant: 1,
			wvHorizon: 0.3,
			wvLayers: 6,
			wvAmp: 2,
		});
	});

	test("layers are rounded and held between 2 and 8", () => {
		const layers = (value: number) => {
			render(<DitherWaves layers={value} />);
			return lastUniforms().wvLayers;
		};
		expect(layers(1)).toBe(2);
		expect(layers(20)).toBe(8);
		expect(layers(4.6)).toBe(5);
		expect(layers(3.2)).toBe(3);
	});

	test("an explicit palette wins for either variant", () => {
		render(<DitherWaves variant="lines" palette={["a", "b"]} />);
		expect(lastShader().palette).toEqual(["a", "b"]);
	});
});

describe("DitherPlasma", () => {
	test("defaults ramp paper, chart-5, ink", () => {
		render(<DitherPlasma />);
		expect(lastShader().palette).toEqual([PAPER, "--chart-5", INK]);
		expect(lastUniforms()).toEqual({
			plScale: 1,
			plWarp: 0.3,
			plBands: 1.5,
			plIntensity: 1,
		});
	});

	test("knobs pass through", () => {
		render(
			<DitherPlasma
				scale={2}
				warp={0.9}
				bands={3}
				intensity={0.2}
				palette={["a", "b"]}
			/>,
		);
		expect(lastUniforms()).toEqual({
			plScale: 2,
			plWarp: 0.9,
			plBands: 3,
			plIntensity: 0.2,
		});
		expect(lastShader().palette).toEqual(["a", "b"]);
	});
});

describe("DitherBeams", () => {
	test("defaults: a beam from above the top-left corner", () => {
		render(<DitherBeams />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({
			bmOrigin: [0.18, -0.12],
			bmAngle: 160 * DEG,
			bmSpread: 80 * DEG,
			bmRays: 9,
			bmSweep: 6 * DEG,
			bmReach: 1,
		});
	});

	test("angles come in degrees and leave in radians; the origin is copied", () => {
		const origin = [0.9, 0.1] as const;
		render(
			<DitherBeams
				origin={origin}
				angle={90}
				spread={40}
				rays={4}
				sweep={0}
				reach={0.5}
			/>,
		);
		const uniforms = lastUniforms();
		expect(uniforms).toEqual({
			bmOrigin: [0.9, 0.1],
			bmAngle: Math.PI / 2,
			bmSpread: 40 * DEG,
			bmRays: 4,
			bmSweep: 0,
			bmReach: 0.5,
		});
		expect(uniforms.bmOrigin).not.toBe(origin);
	});
});

describe("DitherSilk", () => {
	test("defaults ramp four stops", () => {
		render(<DitherSilk />);
		expect(lastShader().palette).toEqual([PAPER, mix(25), mix(55), INK]);
		expect(lastUniforms()).toEqual({
			skFolds: 6,
			skAngle: 110 * DEG,
			skSheen: 0.6,
			skIntensity: 1,
		});
	});

	test("knobs pass through, angle in radians", () => {
		render(
			<DitherSilk
				folds={10}
				angle={180}
				sheen={0.1}
				intensity={0.5}
				palette={["a", "b"]}
			/>,
		);
		expect(lastUniforms()).toEqual({
			skFolds: 10,
			skAngle: Math.PI,
			skSheen: 0.1,
			skIntensity: 0.5,
		});
		expect(lastShader().palette).toEqual(["a", "b"]);
	});
});

describe("DitherSmoke", () => {
	test("defaults", () => {
		render(<DitherSmoke />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({
			smScale: 1,
			smHeight: 0.8,
			smWind: 0.2,
			smDensity: 1,
		});
	});

	test("knobs pass through, wind may blow either way", () => {
		render(
			<DitherSmoke
				scale={2}
				height={0.3}
				wind={-0.7}
				density={1.5}
				palette={["a", "b"]}
			/>,
		);
		expect(lastUniforms()).toEqual({
			smScale: 2,
			smHeight: 0.3,
			smWind: -0.7,
			smDensity: 1.5,
		});
		expect(lastShader().palette).toEqual(["a", "b"]);
	});
});

describe("DitherContours", () => {
	test("defaults ramp paper, a 45% ink tint, ink with twelve tone steps", () => {
		render(<DitherContours />);
		expect(lastShader().palette).toEqual([PAPER, mix(45), INK]);
		expect(lastShader().levels).toBe(12);
		expect(lastUniforms()).toEqual({
			ctLines: 14,
			ctScale: 1,
			ctIndex: 5,
			ctRelief: 0.5,
		});
	});

	test("knobs pass through", () => {
		render(
			<DitherContours
				lines={20}
				scale={2}
				index={0}
				relief={1}
				palette={["a", "b"]}
			/>,
		);
		expect(lastUniforms()).toEqual({
			ctLines: 20,
			ctScale: 2,
			ctIndex: 0,
			ctRelief: 1,
		});
		expect(lastShader().palette).toEqual(["a", "b"]);
	});

	test("n does not change the fixed twelve levels, but an explicit count wins", () => {
		render(<DitherContours n={8} />);
		expect(lastShader().matrix).toBe("bayer8");
		expect(lastShader().levels).toBe(12);
		render(<DitherContours levels="full" />);
		expect(lastShader().levels).toBe("full");
	});
});

describe("DitherRipples", () => {
	test("defaults: two sources, twelve rings", () => {
		render(<DitherRipples />);
		expect(lastShader().palette).toEqual([PAPER, mix(45), INK]);
		expect(lastUniforms()).toEqual({
			rpS0: [0.28, 0.38],
			rpS1: [0.74, 0.66],
			rpS2: [0.5, 0.5],
			rpS3: [0.5, 0.5],
			rpCount: 2,
			rpRings: 12,
			rpDecay: 1,
			rpIntensity: 1,
		});
	});

	test("at most four sources count; unused slots rest at the centre", () => {
		render(
			<DitherRipples
				sources={[
					[0.1, 0.1],
					[0.2, 0.2],
					[0.3, 0.3],
					[0.4, 0.4],
					[0.9, 0.9],
				]}
			/>,
		);
		expect(lastUniforms()).toMatchObject({
			rpS0: [0.1, 0.1],
			rpS1: [0.2, 0.2],
			rpS2: [0.3, 0.3],
			rpS3: [0.4, 0.4],
			rpCount: 4,
		});
		render(<DitherRipples sources={[[0.7, 0.2]]} />);
		expect(lastUniforms()).toMatchObject({
			rpS0: [0.7, 0.2],
			rpS1: [0.5, 0.5],
			rpCount: 1,
		});
	});

	test("no sources still draws one ring set from the centre", () => {
		render(<DitherRipples sources={[]} />);
		expect(lastUniforms()).toMatchObject({ rpS0: [0.5, 0.5], rpCount: 1 });
	});

	test("rings, decay, intensity and palette pass through", () => {
		render(
			<DitherRipples
				rings={5}
				decay={2.5}
				intensity={0.3}
				palette={["a", "b"]}
			/>,
		);
		expect(lastUniforms()).toMatchObject({
			rpRings: 5,
			rpDecay: 2.5,
			rpIntensity: 0.3,
		});
		expect(lastShader().palette).toEqual(["a", "b"]);
	});
});
