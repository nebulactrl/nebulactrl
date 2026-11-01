import { describe, expect, test } from "bun:test";
import {
	assembleFieldFragment,
	DITHER_FRAGMENT,
	describeFieldCompileFailure,
	findPreludeRedefinitions,
	GLSL_NOISE,
	mapCompileLog,
} from "./shader-source";
import { describeUniforms } from "./uniforms";

const FIELD = "float field(vec2 uv, float t) { return uv.y; }";

describe("field stage source", () => {
	test("orders prelude, uniforms, helpers, then the user field", () => {
		const src = assembleFieldFragment({
			field: FIELD,
			colorField: undefined,
			uniforms: describeUniforms({ rpRings: 12, rpDrop: [0.5, 0.5] }),
			textures: ["landMask"],
		}).source;
		const order = [
			"#version 300 es",
			"uniform vec2 u_res;",
			"uniform vec2 rpDrop;",
			"uniform float rpRings;",
			"uniform sampler2D landMask;",
			"float hash21(",
			"vec4 paletteColor(",
			FIELD,
			"void main()",
		].map((needle) => src.indexOf(needle));
		expect(order.every((i) => i >= 0)).toBe(true);
		expect([...order].sort((a, b) => a - b)).toEqual(order);
	});

	test("without a colorField the cell colour is unused and tone is alpha", () => {
		const src = assembleFieldFragment({
			field: FIELD,
			colorField: undefined,
			uniforms: [],
			textures: [],
		}).source;
		expect(src).not.toContain("colorField(");
		expect(src).toContain("o_color = vec4(0.0, 0.0, 0.0, tone);");
	});

	test("a colorField is called with the tone and stored in rgb", () => {
		const color =
			"vec3 colorField(vec2 uv, float t, float tone) { return vec3(tone); }";
		const src = assembleFieldFragment({
			field: FIELD,
			colorField: color,
			uniforms: [],
			textures: [],
		}).source;
		expect(src).toContain(color);
		expect(src).toContain("colorField(uv, u_time, tone)");
		expect(src).toContain("o_color = vec4(c, tone);");
	});
});

describe("shared GLSL", () => {
	test("noise helpers the contract promises", () => {
		for (const name of [
			"hash21",
			"snoise",
			"fbm",
			"rot2",
			"sdCircle",
			"sdBox",
			"spectrum",
		]) {
			expect(GLSL_NOISE).toContain(`${name}(`);
		}
	});

	test("the dither stage declares the palette array at the shared size", () => {
		expect(DITHER_FRAGMENT).toContain("uniform vec4 u_pal[8];");
	});
});

describe("assembleFieldFragment", () => {
	const multiline = [
		"// the field",
		"float field(vec2 uv, float t) {",
		"  return missing;",
		"}",
	].join("\n");
	const color =
		"vec3 colorField(vec2 uv, float t, float tone) {\n  return bad;\n}";

	test("records the line where the field and the colorField start", () => {
		const fragment = assembleFieldFragment({
			field: multiline,
			colorField: color,
			uniforms: describeUniforms({ a: 1 }),
			textures: ["mask"],
		});
		const lines = fragment.source.split("\n");
		expect(lines[fragment.fieldStartLine - 1]).toBe("// the field");
		expect(lines[fragment.colorStartLine - 1]).toBe(
			"vec3 colorField(vec2 uv, float t, float tone) {",
		);
	});

	test("the line moves with the uniforms and samplers above it", () => {
		const bare = assembleFieldFragment({
			field: multiline,
			colorField: undefined,
			uniforms: [],
			textures: [],
		});
		const loaded = assembleFieldFragment({
			field: multiline,
			colorField: undefined,
			uniforms: describeUniforms({ a: 1, b: [1, 2] }),
			textures: ["mask", "other"],
		});
		// Two uniform lines and one extra sampler line.
		expect(loaded.fieldStartLine).toBe(bare.fieldStartLine + 3);
	});
});

describe("mapCompileLog", () => {
	const fieldSource =
		"float a;\nfloat field(vec2 uv, float t) {\n  return nope;\n}";
	const start = 120;

	test("places an error on the author's line", () => {
		const log = `ERROR: 0:${start + 2}: 'nope' : undeclared identifier\n`;
		expect(mapCompileLog(log, start, fieldSource)).toEqual([
			{
				line: 3,
				message: "'nope' : undeclared identifier",
				source: "  return nope;",
			},
		]);
	});

	test("drops errors in the engine's own code and warnings", () => {
		const log = [
			"ERROR: 0:7: 'x' : in the prelude",
			`WARNING: 0:${start}: 'y' : just a warning`,
			`ERROR: 0:${start + 4}: 'z' : past the field`,
			`ERROR: 0:${start - 1}: 'w' : just before it`,
		].join("\n");
		expect(mapCompileLog(log, start, fieldSource)).toEqual([]);
	});

	test("reads every error and ignores a NUL terminator", () => {
		const log = `ERROR: 0:${start}: 'a' : one\r\nERROR: 0:${start + 3}: 'b' : two\0`;
		const found = mapCompileLog(log, start, fieldSource);
		expect(found.map((d) => d.line)).toEqual([1, 4]);
		expect(found[1]?.message).toBe("'b' : two");
	});

	test("a long run of spaces after the line number is read in linear time", () => {
		const started = performance.now();
		const spaces = " ".repeat(200_000);
		expect(mapCompileLog(`ERROR:0:0:${spaces}`, 5, "x")).toEqual([]);
		expect(mapCompileLog(`ERROR:0:1:${spaces}`, 1, "x")).toEqual([
			{ line: 1, message: "", source: "x" },
		]);
		expect(mapCompileLog("ERROR: 0:1:   spaced   ", 1, "x")[0]?.message).toBe(
			"spaced",
		);
		expect(performance.now() - started).toBeLessThan(1000);
	});
});

describe("describeFieldCompileFailure", () => {
	const field = "float field(vec2 uv, float t) {\n  return missing;\n}";
	const color =
		"vec3 colorField(vec2 uv, float t, float tone) {\n  return bad;\n}";
	const fragment = assembleFieldFragment({
		field,
		colorField: color,
		uniforms: [],
		textures: [],
	});

	test("names the line and prints it", () => {
		const log = `ERROR: 0:${fragment.fieldStartLine + 1}: 'missing' : undeclared identifier`;
		expect(describeFieldCompileFailure(log, fragment)).toBe(
			"[dither] field failed to compile, line 2: 'missing' : undeclared identifier\n  return missing;",
		);
	});

	test("counts colorField lines from the colorField itself", () => {
		const log = `ERROR: 0:${fragment.colorStartLine + 1}: 'bad' : undeclared identifier`;
		expect(describeFieldCompileFailure(log, fragment)).toBe(
			"[dither] colorField failed to compile, line 2: 'bad' : undeclared identifier\n  return bad;",
		);
	});

	test("falls back to the raw log when no error is in the author's code", () => {
		const log = "ERROR: 0:3: 'x' : in the prelude";
		expect(describeFieldCompileFailure(log, fragment)).toBe(
			`[dither] shader compile failed\n${log}`,
		);
	});

	test("a three-argument field keeps its own line numbers", () => {
		const three = assembleFieldFragment({
			field: "float field(vec2 uv, vec2 res, float t) {\n  return nope;\n}",
			colorField: undefined,
			uniforms: [],
			textures: [],
		});
		const log = `ERROR: 0:${three.fieldStartLine + 1}: 'nope' : undeclared identifier`;
		expect(describeFieldCompileFailure(log, three)).toContain("line 2:");
	});
});

describe("findPreludeRedefinitions", () => {
	test("finds a field's own copy of a prelude function", () => {
		const field = `
float hash21(vec2 p) { return fract(sin(dot(p, vec2(12.9, 78.2))) * 43758.5); }
vec3 spectrum (float h) { return vec3(h); }
float field(vec2 uv, float t) { return hash21(uv) * fbm(uv); }`;
		expect(findPreludeRedefinitions(field)).toEqual(["hash21", "spectrum"]);
	});

	test("calls, comments and look-alike names are not redefinitions", () => {
		const field = `
// float fbm(vec2 p) { return 0.0; }
/* vec4 paletteColor(float x) { return vec4(x); } */
float fbmSoft(vec2 p) { return 0.0; }
float field(vec2 uv, float t) {
  float h = hash21(uv);
  return snoise(uv) + h;
}`;
		expect(findPreludeRedefinitions(field)).toEqual([]);
	});

	test("an unterminated comment opener stays text, a line comment after it still hides", () => {
		const field = `
/* float fbm(vec2 p) { return 0.0; }
// float hash21(vec2 p) { return 0.0; }
float snoise(vec2 p) { return 0.0; }`;
		expect(findPreludeRedefinitions(field)).toEqual(["fbm", "snoise"]);
	});

	test("many unterminated comment openers are scanned in linear time", () => {
		const started = performance.now();
		expect(findPreludeRedefinitions(`/*${"a/*".repeat(100_000)}`)).toEqual([]);
		expect(performance.now() - started).toBeLessThan(1000);
	});

	test("covers every function the prelude promises", () => {
		const all = ["hash21", "snoise", "fbm", "vnoise", "spectrum"];
		expect(findPreludeRedefinitions(GLSL_NOISE)).toEqual(
			expect.arrayContaining(all),
		);
		expect(
			findPreludeRedefinitions(
				"vec4 paletteColor(float x) { return vec4(x); }",
			),
		).toEqual(["paletteColor"]);
	});
});
