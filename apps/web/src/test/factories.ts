/**
 * Typed fixture factories for the schemas of the API (openapi/openapi.json).
 *
 *     make("ServiceView")                                  // a running service named "web"
 *     make("ServiceView", { name: "api", config: { port: 8080 } })
 *     make("PageServer", { items: [make("Server", { name: "node-2" })] })
 *     problem(404, { code: "not_found" })                  // an error reply for stubApi
 *
 * `make(name, overrides)` returns the schema's generated client type (the
 * `ApiSchemas` map, src/test/api-schemas.gen.ts), and `overrides` has to be
 * that type's shape, partially: nested objects are deep-merged into the
 * default (a nested override states only what differs), arrays and scalars
 * replace it, and a key set to `undefined` removes the member (how a test
 * drops an optional field the default has; a required one then fails the check).
 *
 * A default has two layers, both valid against the schema:
 *  1. Structural: generated from the schema for ANY name, required members
 *     only, the smallest legal value (see `build`).
 *  2. Deliberate: the hand-written table in factory-defaults.ts for the
 *     schemas tests use most, layered over the structural one (also where one
 *     schema nests another, so a service's `config` is the table's `Config`).
 *
 * Every result is checked against its schema before it is returned, so a
 * fixture cannot drift from what the server sends and a bad override fails at
 * the `make` call that wrote it.
 */
import type { ApiSchemas } from "./api-schemas.gen";
import { DEFAULTS, EPOCH } from "./factory-defaults";
import {
	componentPointer,
	type Schema,
	specDocument,
	validateAt,
} from "./json-schema";

export type SchemaName = keyof ApiSchemas;

/**
 * What `make` accepts for a `T`: its members, each deeply partial (arrays and
 * scalars whole), and `undefined` for the members that are optional.
 */
export type Overrides<T> = T extends readonly unknown[]
	? T
	: T extends object
		? {
				[K in keyof T]?:
					| Overrides<T[K]>
					| (undefined extends T[K] ? undefined : never);
			}
		: T;

const DATE = "2026-01-15";
const UUID = "00000000-0000-4000-8000-000000000000";
/** A `pattern` the empty string breaks: the first of these that fits is the default. */
const PATTERN_SAMPLES = ["a", "a1", "abc", "a-b", "a_b"];
/** A schema that requires itself with no way out is a spec bug; real ones stop long before this. */
const MAX_DEPTH = 24;

type Plain = Record<string, unknown>;

const isPlain = (value: unknown): value is Plain =>
	typeof value === "object" && value !== null && !Array.isArray(value);

const schemas = () => specDocument().components.schemas;

function schemaOf(name: string): Schema {
	const schema = schemas()[name];
	if (!schema) throw new Error(`make: the API has no schema "${name}"`);
	return schema;
}

/** "#/components/schemas/Name" -> "Name". */
function refName(ref: string): string {
	const prefix = "#/components/schemas/";
	if (!ref.startsWith(prefix)) throw new Error(`make: unsupported $ref ${ref}`);
	return ref.slice(prefix.length);
}

const isNull = (schema: Schema) => schema.type === "null";

/** The variants of a union (huma spells nullable objects `oneOf: [T, null]`). */
const variants = (schema: Schema) => schema.oneOf ?? schema.anyOf;

function typesOf(schema: Schema): string[] {
	if (schema.type === undefined) return [];
	return Array.isArray(schema.type) ? schema.type : [schema.type];
}

function scalar(schema: Schema, type: string): unknown {
	switch (type) {
		case "string":
			return text(schema);
		case "integer":
		case "number":
			return Math.min(
				Math.max(0, schema.minimum ?? 0),
				schema.maximum ?? Number.POSITIVE_INFINITY,
			);
		case "boolean":
			return false;
		default:
			return null;
	}
}

function text(schema: Schema): string {
	switch (schema.format) {
		case "date-time":
			return EPOCH;
		case "date":
			return DATE;
		case "uuid":
			return UUID;
		case "email":
			return "user@example.com";
		case "uri":
		case "url":
			return "https://example.com";
		default:
	}
	const minLength = schema.minLength ?? 0;
	const fits = (candidate: string) =>
		candidate.length >= minLength &&
		candidate.length <= (schema.maxLength ?? Number.POSITIVE_INFINITY) &&
		(schema.pattern === undefined ||
			new RegExp(schema.pattern, "u").test(candidate));
	const empty = "x".repeat(minLength);
	if (fits(empty)) return empty;
	const sample = PATTERN_SAMPLES.find(fits);
	if (sample === undefined) {
		throw new Error(`make: no default for a string matching ${schema.pattern}`);
	}
	return sample;
}

/**
 * The structural default of `schema`: the smallest legal value, required
 * members only. A nullable value is null, except an array (a Go nil slice
 * reads as null on the wire, a test wants the empty list); an enum is its
 * first value; a string is "" (or the shortest that fits); numbers are 0 (or
 * the bound); dates and ids are fixed constants.
 */
function build(schema: Schema, depth: number): unknown {
	if (depth > MAX_DEPTH) {
		throw new Error("make: a schema requires itself with no way out");
	}
	if (schema.$ref !== undefined) return baseOf(refName(schema.$ref), depth + 1);
	if (schema.const !== undefined) return schema.const;
	if (schema.allOf) {
		return schema.allOf.reduce<unknown>(
			(acc, part) => mergeInto(part, acc, build(part, depth + 1)),
			{},
		);
	}
	const union = variants(schema);
	if (union) {
		if (union.some(isNull)) return null;
		const first = union[0];
		return first ? build(first, depth + 1) : null;
	}
	if (schema.enum) return schema.enum.find((o) => o !== null) ?? schema.enum[0];

	const types = typesOf(schema);
	const nullable = types.includes("null");
	const type = types.find((t) => t !== "null");
	if (type === "array") {
		const items = schema.items;
		const length = items ? (schema.minItems ?? 0) : 0;
		return Array.from({ length }, () =>
			items ? build(items, depth + 1) : null,
		);
	}
	if (nullable) return null;
	if (type === "object" || schema.properties || schema.required) {
		const out: Plain = {};
		for (const name of schema.required ?? []) {
			const property = schema.properties?.[name];
			if (property) out[name] = build(property, depth + 1);
		}
		return out;
	}
	return type === undefined ? null : scalar(schema, type);
}

/** The object schemas a schema stands for: through $ref, allOf and the non-null variants of a union. */
function objectSchemas(schema: Schema): Schema[] {
	if (schema.$ref !== undefined)
		return objectSchemas(schemas()[refName(schema.$ref)] ?? {});
	if (schema.allOf) return schema.allOf.flatMap(objectSchemas);
	const union = variants(schema);
	if (union) return union.filter((v) => !isNull(v)).flatMap(objectSchemas);
	return [schema];
}

/** The schema of member `key` of an object schema. */
function memberSchema(schema: Schema, key: string): Schema | undefined {
	for (const part of objectSchemas(schema)) {
		const property = part.properties?.[key];
		if (property) return property;
		if (typeof part.additionalProperties === "object")
			return part.additionalProperties;
	}
	return undefined;
}

/**
 * `over` merged into `base` as the schema reads it: where both are objects
 * their members merge recursively; anything else in `over` replaces; a member
 * `over` sets to undefined is removed. Where `base` is not an object (a null
 * default) and `over` is, `over` merges into the object the schema stands for.
 */
function mergeInto(schema: Schema, base: unknown, over: unknown): unknown {
	if (over === undefined) return base;
	if (!isPlain(over)) return over;
	const out: Plain = isPlain(base) ? { ...base } : objectBase(schema);
	for (const [key, value] of Object.entries(over)) {
		if (value === undefined) {
			delete out[key];
			continue;
		}
		const member = memberSchema(schema, key);
		out[key] = member ? mergeInto(member, out[key], value) : value;
	}
	return out;
}

/** The default of the object a nullable or union schema stands for. */
function objectBase(schema: Schema): Plain {
	const [first] = objectSchemas(schema);
	const made = first ? build(first, 0) : {};
	return isPlain(made) ? made : {};
}

/** The table as a lookup by schema name. */
const table: Record<string, unknown> = DEFAULTS;
const bases = new Map<string, unknown>();

/** The default of the schema `name`: structural, with the table's values on top. */
function baseOf(name: string, depth: number): unknown {
	let base = bases.get(name);
	if (base === undefined) {
		const schema = schemaOf(name);
		base = mergeInto(schema, build(schema, depth), table[name]);
		bases.set(name, base);
	}
	return structuredClone(base);
}

/**
 * A `name` the way the API sends it: the table's default for it (else the
 * structural one) with `overrides` merged in. Throws, naming the JSON path,
 * when the result breaks the schema.
 */
export function make<K extends SchemaName>(
	name: K,
	overrides: Overrides<ApiSchemas[K]> = {} as Overrides<ApiSchemas[K]>,
): ApiSchemas[K] {
	const made = mergeInto(schemaOf(name), baseOf(name, 0), overrides);
	// What travels on the wire: an undefined member is gone.
	const problems = validateAt(
		componentPointer(name),
		JSON.parse(JSON.stringify(made)),
	);
	if (problems.length > 0) {
		throw new Error(
			`make("${name}") breaks the schema:\n  ${problems.join("\n  ")}`,
		);
	}
	return made as ApiSchemas[K];
}

/**
 * A canned error reply, the problem document the API answers any error status
 * with: `problem(404, { code: "not_found", detail: "No such service" })` is
 * `{ status: 404, body: make("Error", { status: 404, ... }) }`.
 */
export function problem(
	status: number,
	overrides: Overrides<ApiSchemas["Error"]> = {},
) {
	return { status, body: make("Error", { ...overrides, status }) };
}
