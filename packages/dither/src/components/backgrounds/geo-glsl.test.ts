import { describe, expect, test } from "bun:test";
import { DEFAULT_BOUNDS } from "./geo";
import {
	ARC_SAMPLES,
	dotMapArcsGlsl,
	dotMapMarkersGlsl,
	globeArcsGlsl,
	globeMarkersGlsl,
	MAX_ARCS,
	MAX_MARKERS,
	markerWeight,
	sceneKey,
} from "./geo-glsl";
import { glslFloat, glslVec2 } from "./glsl";

const LONDON = [51.51, -0.13] as const;
const TOKYO = [35.68, 139.69] as const;
const SYDNEY = [-33.87, 151.21] as const;

describe("glslFloat", () => {
	test("always has a decimal point and no exponent", () => {
		expect(glslFloat(1)).toBe("1.0");
		expect(glslFloat(0.5)).toBe("0.5");
		expect(glslFloat(-2.25)).toBe("-2.25");
		expect(glslFloat(1e-9)).toBe("0.0");
		expect(glslFloat(123456.789)).toBe("123456.789");
	});

	test("non-finite numbers become zero", () => {
		expect(glslFloat(Number.NaN)).toBe("0.0");
		expect(glslFloat(Number.POSITIVE_INFINITY)).toBe("0.0");
	});

	test("vec2 joins two literals", () => {
		expect(glslVec2(1, 0.25)).toBe("vec2(1.0, 0.25)");
	});
});

describe("globe GLSL", () => {
	test("one marker call per marker", () => {
		const code = globeMarkersGlsl([{ at: LONDON }, { at: TOKYO }]);
		expect(code.match(/gbMarker\(/g)).toHaveLength(2);
	});

	test("no markers still defines the function", () => {
		expect(globeMarkersGlsl([])).toContain("float gbMarkers(");
	});

	test("an arc bakes ARC_SAMPLES points and a function", () => {
		const code = globeArcsGlsl([{ from: LONDON, to: TOKYO }]);
		expect(code).toContain(`const vec3 gbA0[${ARC_SAMPLES}]`);
		expect(code.match(/vec3\(/g)?.length).toBeGreaterThanOrEqual(ARC_SAMPLES);
		expect(code).toContain("float gbArc0(");
		expect(code).toContain("gbArc0(p, R, t)");
	});

	test("arcs are capped and no arcs still defines gbArcs", () => {
		const many = Array.from({ length: MAX_ARCS + 4 }, () => ({
			from: LONDON,
			to: SYDNEY,
		}));
		expect(globeArcsGlsl(many).match(/float gbArc\d+\(/g)).toHaveLength(
			MAX_ARCS,
		);
		expect(globeArcsGlsl([])).toContain("float gbArcs(");
	});

	test("markers are capped", () => {
		const many = Array.from({ length: MAX_MARKERS + 5 }, () => ({
			at: LONDON,
		}));
		expect(globeMarkersGlsl(many).match(/gbMarker\(/g)).toHaveLength(
			MAX_MARKERS,
		);
	});

	test("never emits NaN or exponent literals", () => {
		const code = globeArcsGlsl([{ from: LONDON, to: LONDON }]);
		expect(code).not.toContain("NaN");
		expect(code).not.toMatch(/\de[+-]?\d/);
	});
});

describe("dot map GLSL", () => {
	test("markers land inside the unit square for default bounds", () => {
		const code = dotMapMarkersGlsl([{ at: LONDON }], DEFAULT_BOUNDS);
		const match = code.match(/dmMarker\(dotUv, vec2\(([-\d.]+), ([-\d.]+)\)/);
		const u = Number(match?.[1]);
		const v = Number(match?.[2]);
		expect(u).toBeGreaterThan(0);
		expect(u).toBeLessThan(1);
		expect(v).toBeGreaterThan(0);
		expect(v).toBeLessThan(1);
	});

	test("arcs bake a polyline and a bounding box early-out", () => {
		const code = dotMapArcsGlsl(
			[{ from: LONDON, to: TOKYO }],
			DEFAULT_BOUNDS,
			2.68,
			0.0168,
		);
		expect(code).toContain(`const vec2 dmA0[${ARC_SAMPLES}]`);
		expect(code).toContain("return 0.0;");
		expect(code).toContain("dmArc0(vec2(dotUv.x * 2.68, dotUv.y), t)");
	});
});

describe("marker weight", () => {
	test("no weight is the full look", () => {
		expect(markerWeight(undefined)).toEqual([1, 1, 1]);
		expect(markerWeight(Number.NaN)).toEqual([1, 1, 1]);
	});

	test("grows size, reach and brightness with the weight, inside 0..1", () => {
		const [c0, r0, l0] = markerWeight(0);
		const [c1, r1, l1] = markerWeight(1);
		const [cHalf] = markerWeight(0.5);
		expect(c0).toBe(1);
		expect(c1).toBeGreaterThan(cHalf);
		expect(cHalf).toBeGreaterThan(c0);
		expect(r1).toBeGreaterThan(r0);
		expect(l1).toBe(1);
		expect(l0).toBeGreaterThanOrEqual(0.6);
		expect(markerWeight(5)).toEqual(markerWeight(1));
		expect(markerWeight(-3)).toEqual(markerWeight(0));
	});

	test("is baked per marker into the dot map, and unweighted markers pass 1,1,1", () => {
		const code = dotMapMarkersGlsl(
			[{ at: LONDON }, { at: TOKYO, weight: 1 }],
			DEFAULT_BOUNDS,
		);
		expect(code).toContain("vec3(1.0, 1.0, 1.0));");
		expect(code).toContain("vec3(2.2, 1.0, 1.0));");
	});

	test("changes the scene key", () => {
		expect(sceneKey([{ at: LONDON, weight: 0.2 }], [])).not.toBe(
			sceneKey([{ at: LONDON, weight: 0.8 }], []),
		);
	});
});

describe("per-item inks", () => {
	test("a marker and an arc record their ramp position", () => {
		const m = globeMarkersGlsl([{ at: LONDON, ink: 0.5 }]);
		expect(m).toContain("gbMk = 0.5;");
		const a = globeArcsGlsl([{ from: LONDON, to: TOKYO, ink: 1 }]);
		expect(a).toContain("gbAk = 1.0;");
		const dm = dotMapMarkersGlsl([{ at: LONDON, ink: 0.5 }], DEFAULT_BOUNDS);
		expect(dm).toContain("dmMk = 0.5;");
	});

	test("ink changes the scene key", () => {
		expect(sceneKey([{ at: LONDON, ink: 0.5 }], [])).not.toBe(
			sceneKey([{ at: LONDON, ink: 1 }], []),
		);
	});
});

describe("sceneKey", () => {
	test("changes with content, not identity", () => {
		const a = sceneKey([{ at: LONDON }], [{ from: LONDON, to: TOKYO }]);
		const b = sceneKey(
			[{ at: [51.51, -0.13] }],
			[{ from: [51.51, -0.13], to: [35.68, 139.69] }],
		);
		expect(a).toBe(b);
		expect(sceneKey([{ at: TOKYO }], [])).not.toBe(
			sceneKey([{ at: LONDON }], []),
		);
	});
});
