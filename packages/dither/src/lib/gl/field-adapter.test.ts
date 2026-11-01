import { describe, expect, test } from "bun:test";
import {
	adaptFieldSources,
	assembleFieldFragment,
	DITHER_FRAGMENT,
} from "./shader-source";

const OURS = "float field(vec2 uv, float t) { return uv.x; }";
const THREE = `float field(vec2 uv, vec2 res, float t) {
  float aspect = res.x / res.y;
  return uv.y * aspect;
}`;

describe("adaptFieldSources", () => {
	test("leaves our two-argument signature untouched", () => {
		const out = adaptFieldSources(
			OURS,
			"vec3 colorField(vec2 uv, float t, float tone) { return vec3(tone); }",
		);
		expect(out.field).toBe(OURS);
		expect(out.colorField).toContain(
			"colorField(vec2 uv, float t, float tone)",
		);
		expect(out.colorField).not.toContain("nc_colorField3");
	});

	test("wraps the three-argument field and passes the grid size", () => {
		const { field } = adaptFieldSources(THREE, undefined);
		expect(field).toContain("float nc_field3(vec2 uv, vec2 res, float t)");
		expect(field).toContain(
			"float field(vec2 uv, float t) { return nc_field3(uv, u_res, t); }",
		);
		// The user's body is kept as written.
		expect(field).toContain("float aspect = res.x / res.y;");
	});

	test("wraps a three-argument colorField, vec3 or vec4", () => {
		const rgb = adaptFieldSources(
			OURS,
			"vec3 colorField(vec2 uv, vec2 res, float t) { return spectrum(uv.x); }",
		);
		expect(rgb.colorField).toContain(
			"vec3 nc_colorField3(vec2 uv, vec2 res, float t)",
		);
		expect(rgb.colorField).toContain("return nc_colorField3(uv, u_res, t); }");
		const rgba = adaptFieldSources(
			OURS,
			"vec4 colorField(vec2 uv, vec2 res, float t) { return vec4(1.0); }",
		);
		expect(rgba.colorField).toContain("vec4 nc_colorField3(");
		expect(rgba.colorField).toContain("nc_colorField3(uv, u_res, t).rgb; }");
	});

	test("tolerates whitespace and other parameter names", () => {
		const { field } = adaptFieldSources(
			"float  field( vec2 p,vec2 r ,float time ){ return p.x; }",
			undefined,
		);
		expect(field).toContain("nc_field3");
	});
});

describe("fragment sources", () => {
	test("the field program defines exactly one field() entry point", () => {
		const source = assembleFieldFragment({
			field: THREE,
			colorField: undefined,
			uniforms: [],
			textures: [],
		}).source;
		expect(source.match(/float field\(vec2 uv, float t\)/g)?.length).toBe(1);
		expect(source).toContain("field(uv, u_time)");
	});

	test("the dither program can sample the blue-noise tile", () => {
		expect(DITHER_FRAGMENT).toContain("uniform sampler2D u_blue;");
		expect(DITHER_FRAGMENT).toContain("texelFetch(u_blue");
	});
});
