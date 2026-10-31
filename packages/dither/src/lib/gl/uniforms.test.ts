import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { resetLogOnce } from "./dev";
import {
	applyUniforms,
	describeUniforms,
	type UniformSink,
	uniformDeclarations,
	uniformKey,
	uniformNameProblem,
	uniformValuesKey,
} from "./uniforms";

describe("describeUniforms", () => {
	test("sizes and sorts by name", () => {
		const specs = describeUniforms({
			zoom: 2,
			angle: [1, 2],
			tint: [1, 2, 3],
			box: [1, 2, 3, 4],
		});
		expect(specs).toEqual([
			{ name: "angle", size: 2 },
			{ name: "box", size: 4 },
			{ name: "tint", size: 3 },
			{ name: "zoom", size: 1 },
		]);
	});

	test("skips names the engine owns or GLSL cannot use", () => {
		expect(uniformNameProblem("u_time")).not.toBeNull();
		expect(uniformNameProblem("gl_Thing")).not.toBeNull();
		expect(uniformNameProblem("1bad")).not.toBeNull();
		expect(uniformNameProblem("bad-name")).not.toBeNull();
		expect(uniformNameProblem("ripples")).toBeNull();
		expect(describeUniforms({ u_time: 1, ok: 2 })).toEqual([
			{ name: "ok", size: 1 },
		]);
	});

	test("undefined record is empty", () => {
		expect(describeUniforms(undefined)).toEqual([]);
	});
});

describe("describeUniforms warnings", () => {
	const warn = console.warn;
	let messages: string[] = [];

	beforeEach(() => {
		resetLogOnce();
		messages = [];
		console.warn = (message: string) => {
			messages.push(message);
		};
	});
	afterEach(() => {
		console.warn = warn;
	});

	test("names a dropped uniform once, however often the record is described", () => {
		for (let i = 0; i < 3; i++) {
			describeUniforms({ u_time: 1, "bad-name": 2, ok: 3 });
		}
		expect(messages).toHaveLength(2);
		expect(messages[0]).toContain('"u_time"');
		expect(messages[1]).toContain('"bad-name"');
	});

	test("valid uniforms and undefined values stay silent", () => {
		describeUniforms({ ok: 1, missing: undefined as unknown as number });
		expect(messages).toEqual([]);
	});

	test("says why a name is not allowed", () => {
		expect(uniformNameProblem("u_time")).toContain("engine");
		expect(uniformNameProblem("1x")).toContain("identifier");
		expect(uniformNameProblem("ripples")).toBeNull();
	});
});

describe("declarations and keys", () => {
	const specs = describeUniforms({
		a: 1,
		b: [0, 0],
		c: [0, 0, 0],
		d: [0, 0, 0, 0],
	});

	test("declares float and vec types", () => {
		expect(uniformDeclarations(specs)).toBe(
			"uniform float a;\nuniform vec2 b;\nuniform vec3 c;\nuniform vec4 d;\n",
		);
	});

	test("the compile key ignores values but not arity", () => {
		expect(uniformKey(specs)).toBe(
			uniformKey(
				describeUniforms({ a: 9, b: [5, 5], c: [1, 1, 1], d: [2, 2, 2, 2] }),
			),
		);
		expect(uniformKey(specs)).not.toBe(
			uniformKey(
				describeUniforms({ a: 1, b: [0, 0, 0], c: [0, 0, 0], d: [0, 0, 0, 0] }),
			),
		);
	});

	test("the values key changes with values", () => {
		expect(uniformValuesKey({ a: 1, b: [1, 2] })).not.toBe(
			uniformValuesKey({ a: 1, b: [1, 3] }),
		);
		expect(uniformValuesKey(undefined)).toBe("");
	});
});

describe("applyUniforms", () => {
	function recorder() {
		const calls: Array<[string, ...unknown[]]> = [];
		const sink: UniformSink<string> = {
			uniform1f: (loc, x) => void calls.push(["1f", loc, x]),
			uniform2f: (loc, x, y) => void calls.push(["2f", loc, x, y]),
			uniform3f: (loc, x, y, z) => void calls.push(["3f", loc, x, y, z]),
			uniform4f: (loc, x, y, z, w) => void calls.push(["4f", loc, x, y, z, w]),
		};
		return { calls, sink };
	}

	test("uploads each uniform with the call that matches its size", () => {
		const values = {
			a: 1.5,
			b: [1, 2] as const,
			c: [1, 2, 3] as const,
			d: [1, 2, 3, 4] as const,
		};
		const specs = describeUniforms(values);
		const { calls, sink } = recorder();
		applyUniforms(sink, specs, ["la", "lb", "lc", "ld"], values);
		expect(calls).toEqual([
			["1f", "la", 1.5],
			["2f", "lb", 1, 2],
			["3f", "lc", 1, 2, 3],
			["4f", "ld", 1, 2, 3, 4],
		]);
	});

	test("skips missing locations, missing values and changed arity", () => {
		const specs = describeUniforms({ a: 1, b: [1, 2], c: 3 });
		const { calls, sink } = recorder();
		applyUniforms(sink, specs, ["la", null, "lc"], {
			a: 7,
			b: [1, 2],
			c: [1, 2],
		});
		expect(calls).toEqual([["1f", "la", 7]]);
	});
});
