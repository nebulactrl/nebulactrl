import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import type { BackgroundBaseProps } from "./common";
import { DitherEclipse } from "./eclipse";
import { DitherHalftone } from "./halftone";
import { DitherHorizon } from "./horizon";
import { DitherMetaballs } from "./metaballs";
import { DitherOrb } from "./orb";
import { DitherRetroGrid } from "./retro-grid";
import { DitherRidgelines } from "./ridgelines";
import {
	installProbe,
	lastShader,
	lastUniforms,
	motion,
	resetProbe,
	restoreProbe,
} from "./shader-probe";
import { DitherStarfield } from "./starfield";
import { DitherTunnel } from "./tunnel";

// The backgrounds built on `shaderBase` (horizon ... retro-grid). Observed
// through a recording stand-in for `DitherShader`.
installProbe();
afterAll(restoreProbe);
beforeEach(resetProbe);

const PAPER = "--dither-paper";
const INK = "--dither-ink";
const mix = (percent: number) =>
	`color-mix(in oklch, var(${INK}) ${percent}%, var(${PAPER}))`;

interface Family {
	name: string;
	render: (props: BackgroundBaseProps) => ReactElement;
	matrix: string;
	staticTime: number;
	/** Levels when none are given. */
	levels: number | undefined;
}

const FAMILIES: Family[] = [
	{
		name: "horizon",
		render: (p) => <DitherHorizon {...p} />,
		matrix: "bayer4",
		staticTime: 5,
		levels: undefined,
	},
	{
		name: "orb",
		render: (p) => <DitherOrb {...p} />,
		matrix: "bayer4",
		staticTime: 2,
		levels: undefined,
	},
	{
		name: "starfield",
		render: (p) => <DitherStarfield {...p} />,
		matrix: "blue-noise",
		staticTime: 4,
		levels: 33,
	},
	{
		name: "tunnel",
		render: (p) => <DitherTunnel {...p} shape={undefined} />,
		matrix: "bayer4",
		staticTime: 3,
		levels: undefined,
	},
	{
		name: "ridgelines",
		render: (p) => <DitherRidgelines {...p} />,
		matrix: "bayer4",
		staticTime: 6,
		levels: undefined,
	},
	{
		name: "eclipse",
		render: (p) => <DitherEclipse {...p} />,
		matrix: "bayer4",
		staticTime: 2,
		levels: undefined,
	},
	{
		name: "metaballs",
		render: (p) => <DitherMetaballs {...p} />,
		matrix: "bayer4",
		staticTime: 8,
		levels: undefined,
	},
	{
		name: "retro-grid",
		render: (p) => <DitherRetroGrid {...p} />,
		matrix: "bayer4",
		staticTime: 3,
		levels: undefined,
	},
];

describe.each(FAMILIES)("$name: shared shader contract", (family) => {
	test("starts from its own matrix, still moment and tone levels", () => {
		render(family.render({}));
		const call = lastShader();
		expect(call.matrix).toBe(family.matrix as never);
		expect(call.staticTime).toBe(family.staticTime);
		expect(call.levels).toBe(family.levels);
		expect(call["aria-hidden"]).toBe(true);
		expect(call.field).toContain("float field(vec2 uv, float t)");
	});

	test("`n` and `matrix` choose the threshold pattern, `matrix` wins", () => {
		render(family.render({ n: 2 }));
		expect(lastShader().matrix).toBe("bayer2");
		render(family.render({ n: 8, matrix: "blue-noise" }));
		expect(lastShader().matrix).toBe("blue-noise");
	});

	test("`stillTime` replaces the designed still moment", () => {
		render(family.render({ stillTime: 1.25 }));
		expect(lastShader().staticTime).toBe(1.25);
	});

	test("explicit levels win", () => {
		render(family.render({ levels: "stops" }));
		expect(lastShader().levels).toBe("stops");
		render(family.render({ levels: 4 }));
		expect(lastShader().levels).toBe(4);
	});

	test("forwards the engine props", () => {
		const fallback = <b>no gl</b>;
		render(
			family.render({
				className: "bg-y",
				cell: 6,
				fps: 9,
				animate: false,
				speed: 0.25,
				style: { opacity: 0.7 },
				fallback,
			}),
		);
		const call = lastShader();
		expect(call.className).toBe("bg-y");
		expect(call.cell).toBe(6);
		expect(call.fps).toBe(9);
		expect(call.animate).toBe(false);
		expect(call.speed).toBe(0.25);
		expect(call.style).toEqual({ opacity: 0.7 });
		expect(call.fallback).toBe(fallback);
	});

	test("every uniform is a finite number or number tuple the field reads", () => {
		render(family.render({}));
		const entries = Object.entries(lastUniforms());
		expect(entries.length).toBeGreaterThan(0);
		for (const [key, value] of entries) {
			const numbers = Array.isArray(value) ? value : [value];
			for (const n of numbers) expect(Number.isFinite(n), key).toBe(true);
			expect(lastShader().field, key).toContain(key);
		}
	});
});

describe("DitherHorizon", () => {
	test("defaults: sky to ink glow", () => {
		render(<DitherHorizon />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({
			hzHorizon: 0.28,
			hzCurve: 0.6,
			hzGlow: 1,
			hzAurora: 1,
			hzStars: 1,
		});
	});

	test("sky and glow colours build the ramp; an explicit palette wins", () => {
		render(<DitherHorizon skyColor="navy" glowColor="cyan" />);
		expect(lastShader().palette).toEqual(["navy", "cyan"]);
		render(
			<DitherHorizon
				skyColor="navy"
				glowColor="cyan"
				palette={["a", "b", "c"]}
			/>,
		);
		expect(lastShader().palette).toEqual(["a", "b", "c"]);
	});

	test("knobs are held to their range", () => {
		render(
			<DitherHorizon horizon={2} curve={-1} glow={3} aurora={-3} stars={9} />,
		);
		expect(lastUniforms()).toEqual({
			hzHorizon: 1,
			hzCurve: 0,
			hzGlow: 1,
			hzAurora: 0,
			hzStars: 3,
		});
		render(
			<DitherHorizon
				horizon={0.4}
				curve={0.2}
				glow={0.5}
				aurora={0.3}
				stars={2}
			/>,
		);
		expect(lastUniforms()).toEqual({
			hzHorizon: 0.4,
			hzCurve: 0.2,
			hzGlow: 0.5,
			hzAurora: 0.3,
			hzStars: 2,
		});
	});
});

describe("DitherOrb", () => {
	test("defaults: a lean-following orb with no highlight", () => {
		render(<DitherOrb />);
		const call = lastShader();
		expect(call.palette).toEqual([PAPER, INK]);
		expect(call.pointer).toBe(true);
		expect(call.animate).toBe(true);
		expect(lastUniforms()).toEqual({
			obSize: 0.26,
			obMorph: 1,
			obLean: 1,
			obGlow: 0.5,
			obTop: 1,
			obSpec: 0,
		});
	});

	test("a highlight adds a third stop and reserves the top of the ramp for the specular spot", () => {
		render(<DitherOrb background="black" color="gray" highlight="white" />);
		expect(lastShader().palette).toEqual(["black", "gray", "white"]);
		expect(lastUniforms()).toMatchObject({ obTop: 0.5, obSpec: 1 });
	});

	test("an explicit palette wins over colours and highlight", () => {
		render(<DitherOrb palette={["a", "b"]} highlight="white" />);
		expect(lastShader().palette).toEqual(["a", "b"]);
		expect(lastUniforms()).toMatchObject({ obTop: 0.5, obSpec: 1 });
	});

	test("size, morph, lean and glow are held to their range", () => {
		render(<DitherOrb size={0.9} morph={-2} lean={5} glow={4} />);
		expect(lastUniforms()).toMatchObject({
			obSize: 0.5,
			obMorph: 0,
			obLean: 2,
			obGlow: 1,
		});
		render(<DitherOrb size={0.01} glow={-1} lean={0.4} morph={1.5} />);
		expect(lastUniforms()).toMatchObject({
			obSize: 0.1,
			obGlow: 0,
			obLean: 0.4,
			obMorph: 1.5,
		});
	});

	test("lean 0 stops following the pointer", () => {
		render(<DitherOrb lean={0} />);
		expect(lastShader().pointer).toBe(false);
		expect(lastUniforms().obLean).toBe(0);
	});

	test("a still orb does not lean", () => {
		render(<DitherOrb animate={false} lean={1.5} />);
		expect(lastShader().pointer).toBe(false);
		expect(lastShader().animate).toBe(false);
		expect(lastUniforms().obLean).toBe(0);
	});

	test("reduced motion stops the lean even when animated", () => {
		motion.reduced = true;
		render(<DitherOrb lean={1.5} />);
		expect(lastShader().pointer).toBe(false);
		expect(lastUniforms().obLean).toBe(0);
	});
});

describe("DitherStarfield", () => {
	test("defaults: a three stop ramp, 33 tone steps and a mid-ramp star threshold", () => {
		render(<DitherStarfield />);
		expect(lastShader().palette).toEqual([PAPER, mix(55), INK]);
		expect(lastShader().levels).toBe(33);
		expect(lastUniforms()).toEqual({
			sfDensity: 1,
			sfTwinkle: 0.6,
			sfAngle: 270,
			sfDrift: 1.6,
			sfNebula: 0.5,
			sfLo: 0.5,
		});
	});

	test("tone steps follow the palette length: sixteen per segment plus one", () => {
		render(<DitherStarfield palette={["a", "b"]} />);
		expect(lastShader().levels).toBe(17);
		render(<DitherStarfield palette={["a", "b", "c", "d", "e"]} />);
		expect(lastShader().levels).toBe(65);
		render(<DitherStarfield palette={["a"]} />);
		expect(lastShader().levels).toBe(17);
	});

	test("sfLo marks where the last palette segment starts", () => {
		const lo = (palette: string[]) => {
			render(<DitherStarfield palette={palette} />);
			return lastUniforms().sfLo;
		};
		expect(lo(["a", "b"])).toBe(0.5);
		expect(lo(["a", "b", "c"])).toBe(0.5);
		expect(lo(["a", "b", "c", "d"])).toBeCloseTo(2 / 3, 10);
		expect(lo(["a", "b", "c", "d", "e"])).toBe(0.75);
	});

	test("explicit levels beat the palette-derived count", () => {
		render(<DitherStarfield levels={5} />);
		expect(lastShader().levels).toBe(5);
	});

	test("knobs are held to their range; angle and drift pass through", () => {
		render(
			<DitherStarfield
				density={5}
				twinkle={3}
				nebula={-1}
				angle={45}
				drift={4}
			/>,
		);
		expect(lastUniforms()).toMatchObject({
			sfDensity: 2,
			sfTwinkle: 1,
			sfNebula: 0,
			sfAngle: 45,
			sfDrift: 4,
		});
		render(<DitherStarfield density={-1} />);
		expect(lastUniforms().sfDensity).toBe(0);
	});
});

describe("DitherTunnel", () => {
	test("defaults: a round tunnel centred in the box", () => {
		render(<DitherTunnel />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastShader().shape).toBeUndefined();
		expect(lastUniforms()).toEqual({
			tnSquare: 0,
			tnRings: 1,
			tnSpokes: 12,
			tnCenter: [0.5, 0.5],
			tnSway: 1,
			tnFog: 1,
		});
	});

	test("profile picks the section, and `shape` circle/square is its alias", () => {
		render(<DitherTunnel profile="square" />);
		expect(lastUniforms().tnSquare).toBe(1);
		render(<DitherTunnel shape="square" />);
		expect(lastUniforms().tnSquare).toBe(1);
		expect(lastShader().shape).toBeUndefined();
		render(<DitherTunnel profile="square" shape="circle" />);
		expect(lastUniforms().tnSquare).toBe(0);
	});

	test('`shape="dot"` draws round cells and keeps the profile', () => {
		render(<DitherTunnel profile="square" shape="dot" />);
		expect(lastShader().shape).toBe("dot");
		expect(lastUniforms().tnSquare).toBe(1);
	});

	test("`cellShape` sets the cell shape and beats `shape`", () => {
		render(<DitherTunnel cellShape="square" shape="dot" />);
		expect(lastShader().shape).toBe("square");
		render(<DitherTunnel cellShape="dot" />);
		expect(lastShader().shape).toBe("dot");
		render(<DitherTunnel shape="circle" />);
		expect(lastShader().shape).toBeUndefined();
	});

	test("rings, spokes, sway and fog are bounded; the centre is copied", () => {
		const center = [0.2, 0.8] as const;
		render(
			<DitherTunnel rings={0} spokes={99} sway={-1} fog={0} center={center} />,
		);
		const uniforms = lastUniforms();
		expect(uniforms).toMatchObject({
			tnRings: 0.2,
			tnSpokes: 32,
			tnSway: 0,
			tnFog: 0.1,
		});
		expect(uniforms.tnCenter).toEqual([0.2, 0.8]);
		expect(uniforms.tnCenter).not.toBe(center);
		render(<DitherTunnel spokes={-4} rings={2} fog={2.5} sway={1.5} />);
		expect(lastUniforms()).toMatchObject({
			tnSpokes: 0,
			tnRings: 2,
			tnFog: 2.5,
			tnSway: 1.5,
		});
		render(<DitherTunnel spokes={5.6} />);
		expect(lastUniforms().tnSpokes).toBe(6);
	});

	test("palette is replaceable", () => {
		render(<DitherTunnel palette={["a", "b", "c"]} />);
		expect(lastShader().palette).toEqual(["a", "b", "c"]);
	});
});

describe("DitherRidgelines", () => {
	test("defaults", () => {
		render(<DitherRidgelines />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({
			rlLines: 32,
			rlAmp: 1,
			rlSpread: 0.5,
			rlShade: 0.25,
		});
	});

	test("knobs are rounded and held to their range", () => {
		render(<DitherRidgelines lines={2} amplitude={-1} spread={5} shade={2} />);
		expect(lastUniforms()).toEqual({
			rlLines: 6,
			rlAmp: 0,
			rlSpread: 1,
			rlShade: 0.5,
		});
		render(
			<DitherRidgelines lines={200} spread={0} shade={-1} amplitude={2.5} />,
		);
		expect(lastUniforms()).toEqual({
			rlLines: 64,
			rlAmp: 2.5,
			rlSpread: 0.1,
			rlShade: 0,
		});
		render(<DitherRidgelines lines={20.6} palette={["a", "b"]} />);
		expect(lastUniforms().rlLines).toBe(21);
		expect(lastShader().palette).toEqual(["a", "b"]);
	});
});

describe("DitherEclipse", () => {
	test("defaults: a dark disc rimmed in ink on paper", () => {
		render(<DitherEclipse />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({
			ecCenter: [0.5, 0.5],
			ecSize: 0.24,
			ecCorona: 0.5,
			ecLight: 315,
			ecPulse: 0.5,
		});
	});

	test("sky and rim colours build the ramp; an explicit palette wins", () => {
		render(<DitherEclipse skyColor="black" rimColor="gold" />);
		expect(lastShader().palette).toEqual(["black", "gold"]);
		render(
			<DitherEclipse
				skyColor="black"
				rimColor="gold"
				palette={["x", "y", "z"]}
			/>,
		);
		expect(lastShader().palette).toEqual(["x", "y", "z"]);
	});

	test("size has a floor, corona and pulse are clamped, light and centre pass through", () => {
		const center = [0.1, 0.9] as const;
		render(
			<DitherEclipse
				size={0}
				corona={4}
				pulse={-1}
				light={90}
				center={center}
			/>,
		);
		const uniforms = lastUniforms();
		expect(uniforms).toMatchObject({
			ecSize: 0.02,
			ecCorona: 1,
			ecPulse: 0,
			ecLight: 90,
		});
		expect(uniforms.ecCenter).toEqual([0.1, 0.9]);
		expect(uniforms.ecCenter).not.toBe(center);
		render(<DitherEclipse size={0.3} corona={0.2} pulse={0.9} />);
		expect(lastUniforms()).toMatchObject({
			ecSize: 0.3,
			ecCorona: 0.2,
			ecPulse: 0.9,
		});
	});
});

describe("DitherMetaballs", () => {
	test("defaults: five solid blobs", () => {
		render(<DitherMetaballs />);
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({ mbCount: 5, mbSize: 1, mbRings: 0 });
	});

	test("rings variant flips its switch", () => {
		render(<DitherMetaballs variant="rings" />);
		expect(lastUniforms().mbRings).toBe(1);
		render(<DitherMetaballs variant="solid" />);
		expect(lastUniforms().mbRings).toBe(0);
	});

	test("count is rounded and kept between 2 and 8, size has a floor", () => {
		render(<DitherMetaballs count={0} size={0} />);
		expect(lastUniforms()).toMatchObject({ mbCount: 2, mbSize: 0.1 });
		render(<DitherMetaballs count={40} size={1.5} />);
		expect(lastUniforms()).toMatchObject({ mbCount: 8, mbSize: 1.5 });
		render(<DitherMetaballs count={3.5} />);
		expect(lastUniforms().mbCount).toBe(4);
	});
});

describe("DitherRetroGrid", () => {
	test("defaults: a four stop ramp that follows the theme", () => {
		render(<DitherRetroGrid />);
		expect(lastShader().palette).toEqual([PAPER, mix(28), mix(62), INK]);
		expect(lastUniforms()).toEqual({
			rgHorizon: 0.42,
			rgSun: 0.4,
			rgColumns: 12,
		});
	});

	test("a custom palette replaces the ramp", () => {
		render(<DitherRetroGrid palette={["a", "b", "c", "d"]} />);
		expect(lastShader().palette).toEqual(["a", "b", "c", "d"]);
	});

	test("horizon, sun and columns are bounded", () => {
		render(<DitherRetroGrid horizon={0} sun={-1} columns={0} />);
		expect(lastUniforms()).toEqual({ rgHorizon: 0.05, rgSun: 0, rgColumns: 2 });
		render(<DitherRetroGrid horizon={2} sun={9} columns={30} />);
		expect(lastUniforms()).toEqual({
			rgHorizon: 0.95,
			rgSun: 1.5,
			rgColumns: 30,
		});
	});
});

describe("DitherHalftone", () => {
	test("defaults: one ink on paper, grain of one pixel, no colour field", () => {
		render(<DitherHalftone />);
		const call = lastShader();
		expect(call.palette).toEqual([PAPER, INK]);
		expect(call.cell).toBe(1);
		expect(call.colorField).toBeUndefined();
		expect(call.matrix).toBe("bayer4");
		expect(call.staticTime).toBe(5);
		expect(lastUniforms()).toEqual({
			htInks: 1,
			htSrc: 0,
			htCenter: [0.5, 0.5],
			htRadius: 1,
			htDir: 0,
			htSize: 10,
			htAngle: 45,
			htIntensity: 0.9,
			htScale: 1,
			htDrift: 1,
		});
	});

	test("`color` and `background` build the one-ink ramp", () => {
		render(<DitherHalftone color="red" background="cream" />);
		expect(lastShader().palette).toEqual(["cream", "red"]);
		expect(lastShader().colorField).toBeUndefined();
	});

	test("channels become several inks with a per-cell colour field", () => {
		render(
			<DitherHalftone
				channels={["cyan", "magenta", "yellow"]}
				background="white"
			/>,
		);
		const call = lastShader();
		expect(call.palette).toEqual(["white", "cyan", "magenta", "yellow"]);
		expect(call.colorField).toContain(
			"vec3 colorField(vec2 uv, float t, float tone)",
		);
		expect(lastUniforms().htInks).toBe(3);
	});

	test("two channels, and a blank third one is dropped", () => {
		render(<DitherHalftone channels={["cyan", "magenta", ""]} />);
		expect(lastShader().palette).toEqual([PAPER, "cyan", "magenta"]);
		expect(lastUniforms().htInks).toBe(2);
		render(<DitherHalftone channels={["cyan", "magenta"]} />);
		expect(lastUniforms().htInks).toBe(2);
	});

	test("a single real channel keeps the plain field", () => {
		render(<DitherHalftone channels={["cyan", ""]} />);
		expect(lastShader().palette).toEqual([PAPER, "cyan"]);
		expect(lastShader().colorField).toBeUndefined();
		expect(lastUniforms().htInks).toBe(1);
	});

	test("source picks the shader branch; the centre is flipped to y-up", () => {
		const src = { noise: 0, radial: 1, linear: 2 } as const;
		for (const [source, value] of Object.entries(src)) {
			render(<DitherHalftone source={source as keyof typeof src} />);
			expect(lastUniforms().htSrc, source).toBe(value);
		}
		render(<DitherHalftone center={[0.25, 0.1]} />);
		expect(lastUniforms().htCenter).toEqual([0.25, 0.9]);
	});

	test("direction in radians; radius, size, scale have floors; intensity is clamped", () => {
		render(
			<DitherHalftone
				direction={180}
				radius={0}
				size={0}
				scale={0}
				intensity={3}
				angle={15}
				drift={0}
			/>,
		);
		expect(lastUniforms()).toMatchObject({
			htDir: Math.PI,
			htRadius: 0.05,
			htSize: 2,
			htScale: 0.05,
			htIntensity: 1,
			htAngle: 15,
			htDrift: 0,
		});
		render(<DitherHalftone intensity={-1} radius={2} size={16} scale={2} />);
		expect(lastUniforms()).toMatchObject({
			htIntensity: 0,
			htRadius: 2,
			htSize: 16,
			htScale: 2,
		});
	});
});
