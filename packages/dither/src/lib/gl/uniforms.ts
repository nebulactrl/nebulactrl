/**
 * User uniforms for DitherShader fields: a record of numbers and 2-4 tuples
 * becomes `uniform float|vec2|vec3|vec4` declarations and is uploaded without
 * allocating per frame.
 */

import { warnOnce } from "./dev";

export type UniformValue =
	| number
	| readonly [number, number]
	| readonly [number, number, number]
	| readonly [number, number, number, number];

export type UniformRecord = Readonly<Record<string, UniformValue>>;

export type UniformSize = 1 | 2 | 3 | 4;

export interface UniformSpec {
	name: string;
	size: UniformSize;
}

const IDENTIFIER = /^[A-Za-z_][A-Za-z0-9_]*$/;
/** Names the engine owns; user uniforms may not shadow them. */
const RESERVED_PREFIXES = ["u_", "gl_", "v_", "o_"];

/** Why `name` cannot be a user uniform, or `null` when it can. */
export function uniformNameProblem(name: string): string | null {
	if (!IDENTIFIER.test(name)) return "it is not a valid GLSL identifier";
	const prefix = RESERVED_PREFIXES.find((p) => name.startsWith(p));
	return prefix
		? `names starting with ${RESERVED_PREFIXES.join(", ")} belong to the engine`
		: null;
}

export function uniformSize(value: UniformValue): UniformSize {
	if (typeof value === "number") return 1;
	return value.length as UniformSize;
}

/**
 * Specs sorted by name so equal records always give equal keys. Invalid names
 * are skipped, with a one-time warning in development.
 */
export function describeUniforms(
	uniforms: UniformRecord | undefined,
): UniformSpec[] {
	if (!uniforms) return [];
	const specs: UniformSpec[] = [];
	for (const name of Object.keys(uniforms)) {
		const value = uniforms[name];
		if (value === undefined) continue;
		const problem = uniformNameProblem(name);
		if (problem) {
			warnOnce(
				`uniform:${name}`,
				`[dither] uniform "${name}" skipped: ${problem}`,
			);
			continue;
		}
		specs.push({ name, size: uniformSize(value) });
	}
	return specs.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
}

const GLSL_TYPE = ["", "float", "vec2", "vec3", "vec4"] as const;

export function uniformDeclarations(specs: readonly UniformSpec[]): string {
	let out = "";
	for (const spec of specs) {
		out += `uniform ${GLSL_TYPE[spec.size]} ${spec.name};\n`;
	}
	return out;
}

/** Changes only when a recompile is needed (names or arity), never for new values. */
export function uniformKey(specs: readonly UniformSpec[]): string {
	return specs.map((spec) => `${spec.name}:${spec.size}`).join(",");
}

/** Cheap content key for the values, so static dithers redraw when a prop changes. */
export function uniformValuesKey(uniforms: UniformRecord | undefined): string {
	if (!uniforms) return "";
	let key = "";
	for (const name of Object.keys(uniforms)) {
		const value = uniforms[name];
		if (value === undefined) continue;
		key += `${name}=${typeof value === "number" ? value : value.join(",")};`;
	}
	return key;
}

/** The slice of WebGL2 that uploading uniforms needs; lets tests pass a recorder. */
export interface UniformSink<Loc> {
	uniform1f(location: Loc, x: number): void;
	uniform2f(location: Loc, x: number, y: number): void;
	uniform3f(location: Loc, x: number, y: number, z: number): void;
	uniform4f(location: Loc, x: number, y: number, z: number, w: number): void;
}

/**
 * Upload `values` for `specs` into `locations` (same order as specs). Values
 * missing from the record, or whose arity changed since the spec was built,
 * are skipped. Allocation-free.
 */
export function applyUniforms<Loc>(
	sink: UniformSink<Loc>,
	specs: readonly UniformSpec[],
	locations: ReadonlyArray<Loc | null>,
	values: UniformRecord | undefined,
): void {
	if (!values) return;
	for (let i = 0; i < specs.length; i++) {
		const spec = specs[i];
		const loc = locations[i];
		const value = values[spec?.name ?? ""];
		if (!spec || loc == null || value === undefined) continue;
		if (typeof value === "number") {
			if (spec.size === 1) sink.uniform1f(loc, value);
			continue;
		}
		if (value.length !== spec.size) continue;
		if (value.length === 2) sink.uniform2f(loc, value[0], value[1]);
		else if (value.length === 3)
			sink.uniform3f(loc, value[0], value[1], value[2]);
		else sink.uniform4f(loc, value[0], value[1], value[2], value[3]);
	}
}
