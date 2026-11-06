import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { act, render } from "@testing-library/react";
import type { BackgroundEntry, KnobValues } from "./registry";
import { BACKGROUNDS_1 } from "./registry-1";
import { BACKGROUNDS_2 } from "./registry-2";
import {
	frame,
	installProbe,
	lastShader,
	lastUniforms,
	resetProbe,
	restoreProbe,
	shaderCalls,
} from "./shader-probe";

// The gallery registry: each entry's knobs and how its `render` turns knob
// values into a background. Backgrounds are observed through a recording
// stand-in for the shader.
installProbe();
afterAll(restoreProbe);
beforeEach(resetProbe);

const ALL: readonly BackgroundEntry[] = [...BACKGROUNDS_1, ...BACKGROUNDS_2];
const PAPER = "--dither-paper";
const INK = "--dither-ink";
const DEG = Math.PI / 180;

function entry(id: string): BackgroundEntry {
	const found = ALL.find((e) => e.id === id);
	if (!found) throw new Error(`no entry ${id}`);
	return found;
}

function defaultsOf(e: BackgroundEntry): KnobValues {
	return Object.fromEntries(e.knobs.map((k) => [k.key, k.value]));
}

function show(id: string, values: KnobValues) {
	return render(<>{entry(id).render(values)}</>);
}

/** Renders an entry with `over` on top of its knob defaults. */
function shown(id: string, over: KnobValues = {}) {
	const view = show(id, { ...defaultsOf(entry(id)), ...over });
	return view;
}

describe("registry shape", () => {
	test("lists every background once, in two parts", () => {
		expect(BACKGROUNDS_1.map((e) => e.id)).toEqual([
			"gradient",
			"glow",
			"mesh-gradient",
			"aurora",
			"noise-field",
			"waves",
			"plasma",
			"beams",
			"silk",
			"smoke",
			"contours",
			"ripples",
		]);
		expect(BACKGROUNDS_2.map((e) => e.id)).toEqual([
			"ridgelines",
			"metaballs",
			"tunnel",
			"retro-grid",
			"eclipse",
			"horizon",
			"starfield",
			"nebula",
			"halftone",
			"globe",
			"dot-map",
			"orb",
			"fade",
		]);
		expect(new Set(ALL.map((e) => e.id)).size).toBe(ALL.length);
		for (const e of ALL) expect(e.name.length, e.id).toBeGreaterThan(0);
	});

	test.each(ALL.map((e) => [e.id, e] as const))(
		"%s: knobs are well formed",
		(_id, e) => {
			expect(new Set(e.knobs.map((k) => k.key)).size).toBe(e.knobs.length);
			for (const knob of e.knobs) {
				expect(knob.label.length, knob.key).toBeGreaterThan(0);
				if (knob.type === "range") {
					expect(knob.min, knob.key).toBeLessThan(knob.max);
					expect(knob.step, knob.key).toBeGreaterThan(0);
					expect(knob.value, knob.key).toBeGreaterThanOrEqual(knob.min);
					expect(knob.value, knob.key).toBeLessThanOrEqual(knob.max);
				} else if (knob.type === "select") {
					expect(knob.options, knob.key).toContain(knob.value);
				} else {
					expect(typeof knob.value, knob.key).toBe("boolean");
				}
			}
			// Every entry offers the shared matrix knob.
			const matrix = e.knobs.find((k) => k.key === "matrix");
			expect(matrix?.type).toBe("select");
			expect(matrix?.value).toBe("default");
		},
	);

	test.each(ALL.filter((e) => e.id !== "fade").map((e) => [e.id, e] as const))(
		"%s: the knob defaults draw what the render fallbacks draw",
		(id, e) => {
			const { unmount } = show(id, {});
			const bare = lastShader();
			const bareUniforms = lastUniforms();
			unmount();
			resetProbe();
			show(id, defaultsOf(e));
			const withKnobs = lastShader();
			expect(lastUniforms()).toEqual(bareUniforms);
			expect(withKnobs.palette).toEqual(bare.palette);
			expect(withKnobs.cell).toBe(bare.cell);
			expect(withKnobs.speed).toBe(bare.speed);
			expect(withKnobs.matrix).toBe(bare.matrix);
		},
	);

	test.each(ALL.filter((e) => e.id !== "fade").map((e) => [e.id, e] as const))(
		"%s: the cell and speed knobs reach the shader",
		(id, e) => {
			const cell = e.knobs.find((k) => k.key === "cell");
			const speed = e.knobs.find((k) => k.key === "speed");
			const over: Record<string, number> = {};
			if (cell) over.cell = 7;
			if (speed) over.speed = 2.5;
			shown(id, over);
			if (cell) expect(lastShader().cell).toBe(7);
			// The gallery's globe calls its speed knob `spin`.
			if (speed) expect(lastShader().speed).toBe(2.5);
		},
	);

	test.each(
		ALL.filter((e) => e.id !== "fade" && e.id !== "globe").map(
			(e) => [e.id] as const,
		),
	)("%s: backgrounds fill their frame", (id) => {
		shown(id);
		const className = lastShader().className ?? "";
		expect(className).toContain("absolute");
		expect(className).toContain("inset-0");
	});

	test.each([
		["bayer2", "bayer2"],
		["bayer4", "bayer4"],
		["bayer8", "bayer8"],
		["blue-noise", "blue-noise"],
	])("the matrix knob %s reaches every background", (knob, expected) => {
		for (const e of ALL) {
			if (e.id === "fade") continue;
			resetProbe();
			shown(e.id, { matrix: knob });
			expect(lastShader().matrix, e.id).toBe(expected as never);
		}
	});

	test('matrix "default" keeps each background\'s own choice', () => {
		shown("mesh-gradient", { matrix: "default" });
		expect(lastShader().matrix).toBe("blue-noise");
		shown("glow", { matrix: "default" });
		expect(lastShader().matrix).toBe("bayer8");
	});
});

describe("registry part 1", () => {
	test("gradient passes its knobs", () => {
		shown("gradient", {
			mode: "conic",
			angle: 90,
			centerX: 0.2,
			centerY: 0.8,
			radius: 0.5,
			from: 0.1,
			to: 0.9,
			noise: 0.3,
		});
		expect(lastUniforms()).toEqual({
			grMode: 2,
			grAngle: 90 * DEG,
			grCenter: [0.2, 0.8],
			grRadius: 0.5,
			grFrom: 0.1,
			grTo: 0.9,
			grNoise: 0.3,
		});
	});

	test("glow passes position, radius and intensity", () => {
		shown("glow", { position: "bottom-right", radius: 1, intensity: 0.2 });
		expect(lastUniforms()).toEqual({
			glPos: [1.05, 1.05],
			glRadius: 1,
			glIntensity: 0.2,
		});
	});

	test("mesh gradient keeps to 3, 4 or 5 blobs", () => {
		for (const [asked, blobs] of [
			[3, 3],
			[4, 4],
			[5, 5],
			[1, 4],
			[7, 4],
		] as const) {
			shown("mesh-gradient", { blobs: asked, size: 0.9 });
			expect(lastUniforms(), String(asked)).toEqual({
				mgBlobs: blobs,
				mgSize: 0.9,
			});
		}
	});

	test("aurora: the tip colour toggle adds a chart-2 stop", () => {
		shown("aurora", { twoTone: true });
		expect(lastShader().palette).toEqual([PAPER, "--chart-2", INK]);
		shown("aurora", { twoTone: false });
		expect(lastShader().palette).toEqual([PAPER, INK]);
	});

	test("aurora passes its knobs, direction and mask", () => {
		shown("aurora", {
			curtains: 5,
			height: 0.1,
			tilt: -20,
			drift: 1,
			direction: "down",
			mask: "top",
			rays: 3,
			glow: 2,
		});
		expect(lastUniforms()).toMatchObject({
			auCurtains: 5,
			auHeight: 0.1,
			auTilt: -20 * DEG,
			auDrift: 1,
			auDown: 1,
			auMask: 2,
			auRays: 3,
			auGlow: 2,
		});
	});

	test("noise field: the highlight toggle adds a chart-2 stop", () => {
		shown("noise-field", { highlight: true });
		expect(lastShader().palette).toEqual([PAPER, INK, "--chart-2"]);
		shown("noise-field", { highlight: false });
		expect(lastShader().palette).toEqual([PAPER, INK]);
	});

	test("noise field passes its knobs, variant and mask", () => {
		shown("noise-field", {
			variant: "marble",
			mask: "edges",
			maskSize: 0.8,
			invert: true,
			scale: 2,
			warp: 3,
			detail: 2,
			bands: 5,
			flow: 0,
			angle: 180,
		});
		expect(lastUniforms()).toMatchObject({
			nfVariant: 3,
			nfMask: 4,
			nfMaskSize: 0.8,
			nfInvert: 1,
			nfScale: 2,
			nfWarp: 3,
			nfDetail: 2,
			nfBands: 5,
			nfFlow: 0,
			nfAngle: Math.PI,
		});
	});

	test("noise field starts with a centre mask in the gallery", () => {
		shown("noise-field");
		expect(lastUniforms().nfMask).toBe(1);
	});

	test("waves, plasma, beams, silk, smoke and contours pass their knobs", () => {
		shown("waves", { variant: "lines", horizon: 0.3, layers: 3, amplitude: 2 });
		expect(lastUniforms()).toEqual({
			wvVariant: 1,
			wvHorizon: 0.3,
			wvLayers: 3,
			wvAmp: 2,
		});
		shown("plasma", { scale: 2, warp: 0.8, bands: 3, intensity: 0.5 });
		expect(lastUniforms()).toEqual({
			plScale: 2,
			plWarp: 0.8,
			plBands: 3,
			plIntensity: 0.5,
		});
		shown("beams", {
			originX: 0.5,
			originY: 0.1,
			angle: 90,
			spread: 40,
			rays: 4,
			sweep: 10,
			reach: 0.6,
		});
		expect(lastUniforms()).toEqual({
			bmOrigin: [0.5, 0.1],
			bmAngle: 90 * DEG,
			bmSpread: 40 * DEG,
			bmRays: 4,
			bmSweep: 10 * DEG,
			bmReach: 0.6,
		});
		shown("silk", { folds: 9, angle: 45, sheen: 0.2, intensity: 0.7 });
		expect(lastUniforms()).toEqual({
			skFolds: 9,
			skAngle: 45 * DEG,
			skSheen: 0.2,
			skIntensity: 0.7,
		});
		shown("smoke", { scale: 2, height: 0.4, wind: -0.5, density: 1.2 });
		expect(lastUniforms()).toEqual({
			smScale: 2,
			smHeight: 0.4,
			smWind: -0.5,
			smDensity: 1.2,
		});
		shown("contours", { lines: 20, scale: 2, index: 3, relief: 0.9 });
		expect(lastUniforms()).toEqual({
			ctLines: 20,
			ctScale: 2,
			ctIndex: 3,
			ctRelief: 0.9,
		});
	});

	test("contours' cell knob starts at 2", () => {
		expect(entry("contours").knobs.find((k) => k.key === "cell")?.value).toBe(
			2,
		);
		shown("contours");
		expect(lastShader().cell).toBe(2);
	});

	test("ripples: the sources knob picks how many of four preset points", () => {
		shown("ripples", { sources: 1 });
		expect(lastUniforms()).toMatchObject({ rpCount: 1, rpS0: [0.28, 0.38] });
		shown("ripples", { sources: 3 });
		expect(lastUniforms()).toMatchObject({ rpCount: 3, rpS2: [0.5, 0.15] });
		shown("ripples", { sources: 4, rings: 5, decay: 2, intensity: 0.4 });
		expect(lastUniforms()).toMatchObject({
			rpCount: 4,
			rpS3: [0.2, 0.85],
			rpRings: 5,
			rpDecay: 2,
			rpIntensity: 0.4,
		});
	});
});

describe("registry part 2", () => {
	test("ridgelines, metaballs, tunnel pass their knobs", () => {
		shown("ridgelines", { lines: 10, amplitude: 2, spread: 0.8, shade: 0.4 });
		expect(lastUniforms()).toEqual({
			rlLines: 10,
			rlAmp: 2,
			rlSpread: 0.8,
			rlShade: 0.4,
		});
		shown("metaballs", { variant: "rings", count: 3, size: 1.5 });
		expect(lastUniforms()).toEqual({ mbCount: 3, mbSize: 1.5, mbRings: 1 });
		shown("tunnel", {
			profile: "square",
			rings: 2,
			spokes: 20,
			centerX: 0.3,
			centerY: 0.6,
			sway: 0.5,
			fog: 2,
		});
		expect(lastUniforms()).toEqual({
			tnSquare: 1,
			tnRings: 2,
			tnSpokes: 20,
			tnCenter: [0.3, 0.6],
			tnSway: 0.5,
			tnFog: 2,
		});
	});

	test("retro grid: the sunset toggle swaps in four warm colours", () => {
		shown("retro-grid", { sunset: false });
		expect(lastShader().palette?.[0]).toBe(PAPER);
		expect(lastShader().palette).toHaveLength(4);
		shown("retro-grid", { sunset: true, horizon: 0.5, sun: 0.2, columns: 8 });
		expect(lastShader().palette).toEqual([
			"oklch(0.18 0.05 300)",
			"oklch(0.4 0.17 330)",
			"oklch(0.64 0.2 10)",
			"oklch(0.86 0.15 75)",
		]);
		expect(lastUniforms()).toEqual({
			rgHorizon: 0.5,
			rgSun: 0.2,
			rgColumns: 8,
		});
	});

	test("eclipse, horizon and starfield pass their knobs", () => {
		shown("eclipse", {
			centerX: 0.1,
			centerY: 0.2,
			size: 0.3,
			corona: 0.9,
			light: 45,
			pulse: 0.1,
		});
		expect(lastUniforms()).toEqual({
			ecCenter: [0.1, 0.2],
			ecSize: 0.3,
			ecCorona: 0.9,
			ecLight: 45,
			ecPulse: 0.1,
		});
		shown("horizon", {
			horizon: 0.4,
			curve: 0.2,
			glow: 0.5,
			aurora: 0.3,
			stars: 2,
		});
		expect(lastUniforms()).toEqual({
			hzHorizon: 0.4,
			hzCurve: 0.2,
			hzGlow: 0.5,
			hzAurora: 0.3,
			hzStars: 2,
		});
		shown("starfield", {
			density: 1.5,
			twinkle: 0.1,
			angle: 90,
			drift: 3,
			nebula: 0.9,
		});
		expect(lastUniforms()).toMatchObject({
			sfDensity: 1.5,
			sfTwinkle: 0.1,
			sfAngle: 90,
			sfDrift: 3,
			sfNebula: 0.9,
		});
	});

	test("nebula passes its knobs and starts slow", () => {
		expect(entry("nebula").knobs.find((k) => k.key === "speed")?.value).toBe(
			0.08,
		);
		shown("nebula");
		expect(lastShader().speed).toBe(0.08);
		shown("nebula", {
			intensity: 1.2,
			density: 0.3,
			warp: 2,
			stars: 0.5,
			mask: "bottom",
			parallax: 1,
		});
		expect(lastUniforms()).toMatchObject({
			nbIntensity: 1.2,
			nbDensity: 0.3,
			nbWarp: 2,
			nbStars: 0.5,
			nbMask: 3,
			nbParallax: 1,
		});
		expect(lastShader().pointer).toBe(true);
	});

	test("halftone: the inks knob picks one, two or three series colours", () => {
		shown("halftone", { inks: "1" });
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastShader().colorField).toBeUndefined();
		shown("halftone", { inks: "2" });
		expect(lastShader().palette).toEqual([
			PAPER,
			"--dither-series-1",
			"--dither-series-2",
		]);
		expect(lastShader().colorField).toBeDefined();
		shown("halftone", { inks: "3" });
		expect(lastShader().palette).toEqual([
			PAPER,
			"--dither-series-1",
			"--dither-series-2",
			"--dither-series-3",
		]);
		expect(lastUniforms().htInks).toBe(3);
	});

	test("halftone passes its knobs", () => {
		shown("halftone", {
			source: "linear",
			centerX: 0.2,
			centerY: 0.3,
			radius: 0.5,
			direction: 90,
			size: 16,
			angle: 30,
			intensity: 0.5,
			scale: 2,
			drift: 0,
		});
		expect(lastUniforms()).toEqual({
			htInks: 1,
			htSrc: 2,
			htCenter: [0.2, 0.7],
			htRadius: 0.5,
			htDir: Math.PI / 2,
			htSize: 16,
			htAngle: 30,
			htIntensity: 0.5,
			htScale: 2,
			htDrift: 0,
		});
	});

	test("orb: the highlight toggle adds a colour-mixed third stop and the specular spot", () => {
		shown("orb", { highlight: true });
		expect(lastShader().palette).toEqual([
			PAPER,
			INK,
			`color-mix(in oklch, var(${INK}) 30%, white)`,
		]);
		expect(lastUniforms()).toMatchObject({ obTop: 0.5, obSpec: 1 });
		shown("orb", {
			highlight: false,
			size: 0.4,
			morph: 0.5,
			lean: 0,
			glow: 0.9,
		});
		expect(lastShader().palette).toEqual([PAPER, INK]);
		expect(lastUniforms()).toEqual({
			obSize: 0.4,
			obMorph: 0.5,
			obLean: 0,
			obGlow: 0.9,
			obTop: 1,
			obSpec: 0,
		});
	});
});

describe("registry globe", () => {
	test("renders a draggable globe with the gallery's accessible name and five city markers", () => {
		const { container } = shown("globe");
		const host = container.firstElementChild as HTMLElement;
		expect(host).toHaveAttribute("role", "img");
		expect(host).toHaveAttribute(
			"aria-label",
			"World globe with offices in five cities. Drag it or use the arrow keys to turn it.",
		);
		expect(host).toHaveClass("absolute", "inset-0");
		const field = lastShader().field;
		expect(field.match(/v = gbMarker\(p,/g)).toHaveLength(5);
		expect(field.match(/float gbArc\d+\(/g)).toHaveLength(3);
		expect(lastShader().palette).toEqual([PAPER, INK, "orange"]);
		expect(lastUniforms()).toMatchObject({
			gbLon: 10,
			gbTilt: 18,
			gbLight: 300,
			gbOcean: 0.18,
			gbAtmo: 1,
		});
	});

	test("toggles remove markers and arcs", () => {
		shown("globe", { markers: false });
		expect(lastShader().field.match(/v = gbMarker\(p,/g)).toBeNull();
		expect(lastShader().field.match(/float gbArc\d+\(/g)).toHaveLength(3);
		shown("globe", { arcs: false });
		expect(lastShader().field.match(/v = gbMarker\(p,/g)).toHaveLength(5);
		expect(lastShader().field.match(/float gbArc\d+\(/g)).toBeNull();
	});

	test("per-item colours colour four of five markers and two arcs", () => {
		shown("globe", { colours: true, accent: "blue" });
		const call = lastShader();
		expect(call.palette).toEqual([
			PAPER,
			INK,
			"blue",
			"green",
			"pink",
			"purple",
		]);
		expect(call.colorField).toBeDefined();
		expect(call.field.match(/float gbArc\d+\(/g)).toHaveLength(2);
		expect(lastUniforms().gbColoured).toBe(1);
	});

	test("accent, spin, start, tilt, light, ocean, atmosphere and draggable pass through", () => {
		const { container } = shown("globe", {
			accent: "pink",
			spin: 12,
			start: -90,
			tilt: 40,
			light: 120,
			ocean: 0.5,
			atmosphere: false,
			draggable: false,
			cell: 4,
		});
		const host = container.firstElementChild as HTMLElement;
		expect(host).toHaveAttribute("aria-hidden", "true");
		expect(lastShader().palette).toEqual([PAPER, INK, "pink"]);
		expect(lastShader().cell).toBe(4);
		expect(lastUniforms()).toMatchObject({
			gbLon: -90,
			gbTilt: 40,
			gbLight: 120,
			gbOcean: 0.5,
			gbAtmo: 0,
		});
	});

	test("the spin knob turns the globe by that many degrees per second", () => {
		shown("globe", { spin: 12 });
		act(() => frame.run?.(1, 0.5));
		expect(lastUniforms().gbLon).toBeCloseTo(10 - 6, 10);
		shown("globe", { spin: -12 });
		act(() => frame.run?.(1, 0.5));
		expect(lastUniforms().gbLon).toBeCloseTo(10 + 6, 10);
	});

	test("the accent select offers every dither colour and ink", () => {
		const accent = entry("globe").knobs.find((k) => k.key === "accent");
		expect(accent?.type).toBe("select");
		if (accent?.type !== "select") return;
		expect(accent.options).toContain("ink");
		expect(accent.options).toContain("blue");
		expect(accent.options.at(-1)).toBe("ink");
	});
});

describe("registry dot map", () => {
	test("renders a world map with five labelled cities", () => {
		const { container, getByText } = shown("dot-map");
		const wrapper = container.firstElementChild as HTMLElement;
		expect(wrapper).toHaveClass(
			"absolute",
			"inset-0",
			"flex",
			"items-center",
			"justify-center",
		);
		const map = wrapper.firstElementChild as HTMLElement;
		expect(map).toHaveAttribute(
			"aria-label",
			"World map with offices in five cities, London, New York, Tokyo, Sydney, São Paulo",
		);
		for (const city of ["London", "New York", "Tokyo", "Sydney", "São Paulo"]) {
			expect(getByText(city)).toBeInTheDocument();
		}
		expect(lastShader().field.match(/float dmArc\d+\(/g)).toHaveLength(3);
		// The map's accent knob names ink itself.
		expect(lastShader().palette).toEqual(["transparent", "grey", "ink"]);
	});

	test("toggles remove markers and arcs", () => {
		const { container } = shown("dot-map", { markers: false, arcs: false });
		expect(container.querySelectorAll("span")).toHaveLength(0);
		expect(lastShader().field.match(/m = dmMarker\(/g)).toBeNull();
		expect(lastShader().field.match(/float dmArc\d+\(/g)).toBeNull();
	});

	test("weights scale the markers, the heaviest keeping the full look", () => {
		shown("dot-map", { weights: true });
		expect(lastShader().field).toContain("vec3(2.2, 1.0, 1.0)");
		expect(lastShader().field).toContain("vec3(1.0, 0.45, 0.6)");
	});

	test("per-item colours win over weights and add four colours", () => {
		shown("dot-map", { colours: true, weights: true, accent: "red" });
		const call = lastShader();
		expect(call.palette).toEqual([
			"transparent",
			"grey",
			"red",
			"blue",
			"green",
			"pink",
			"purple",
		]);
		expect(call.field).not.toContain("vec3(2.2, 1.0, 1.0)");
		expect(call.field.match(/float dmArc\d+\(/g)).toHaveLength(3);
	});

	test("columns, dots, halo, ocean pass through", () => {
		shown("dot-map", {
			columns: 80,
			dots: "round",
			halo: 5,
			ocean: 0.3,
			cell: 2,
		});
		expect(lastUniforms()).toMatchObject({
			dmCols: 80,
			dmRound: 1,
			dmHalo: 5,
			dmOcean: 0.3,
		});
		expect(lastShader().cell).toBe(2);
	});
});

describe("registry fade", () => {
	test("shows sample content under a still fade", () => {
		const { container, getByText } = shown("fade");
		expect(getByText("Content that fades into the page")).toBeInTheDocument();
		const root = container.firstElementChild as HTMLElement;
		expect(root).toHaveClass("absolute", "inset-0", "overflow-hidden");
		const edge = root.querySelector(".pointer-events-none") as HTMLElement;
		expect(edge).toHaveClass("inset-x-0", "bottom-0");
		expect(edge.style.height).toBe("200px");
		expect(edge.style.maskSize).toBe("8px 200px");
		expect(shaderCalls).toHaveLength(0);
	});

	test("side, size and cell pass through", () => {
		const { container } = shown("fade", { side: "left", size: 100, cell: 2 });
		const edge = container.querySelector(".inset-y-0.left-0") as HTMLElement;
		expect(edge.style.width).toBe("100px");
		expect(edge.style.maskSize).toBe("100px 8px");
	});

	test("the drift toggle switches to the animated shader", () => {
		shown("fade", { animate: true, side: "top" });
		expect(lastUniforms().fdDir).toEqual([0, 1]);
		expect(lastShader().palette).toEqual(["transparent", PAPER]);
	});

	test("the matrix knob shapes the still fade", () => {
		const { container } = shown("fade", { matrix: "bayer8", cell: 2 });
		const edge = container.querySelector(
			".pointer-events-none.absolute.inset-x-0",
		) as HTMLElement;
		expect(edge.style.maskSize).toBe("16px 200px");
	});
});
