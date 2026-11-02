/**
 * The API contract (openapi/openapi.json at the repo root) as a JSON Schema
 * validator: the document is registered with ajv (draft 2020-12, which is what
 * OpenAPI 3.1 speaks) so every `$ref` into `#/components` resolves, and each
 * schema is compiled once, on first use, by its JSON pointer into the document.
 * stubApi and the test factories use it to keep canned replies, the bodies the
 * app sends and the fixtures honest against the contract.
 */
import { readFileSync } from "node:fs";
import { join } from "node:path";
import Ajv2020, {
	type ErrorObject,
	type ValidateFunction,
} from "ajv/dist/2020";
import addFormats from "ajv-formats";

export type Schema = {
	$ref?: string;
	type?: string | string[];
	properties?: Record<string, Schema>;
	required?: string[];
	additionalProperties?: boolean | Schema;
	items?: Schema;
	enum?: unknown[];
	const?: unknown;
	allOf?: Schema[];
	anyOf?: Schema[];
	oneOf?: Schema[];
	format?: string;
	pattern?: string;
	minLength?: number;
	maxLength?: number;
	minItems?: number;
	maxItems?: number;
	minimum?: number;
	maximum?: number;
};

interface Spec {
	paths: Record<string, Record<string, unknown>>;
	components: { schemas: Record<string, Schema> };
}

const SPEC_PATH = join(import.meta.dir, "../../../../openapi/openapi.json");
const SPEC_ID = "nebula:openapi";
const MAX_PROBLEMS = 5;

let loaded: { spec: Spec; ajv: Ajv2020 } | undefined;

/** The spec is 2 MB: parsed and registered with ajv once per process, on first use. */
function contract() {
	if (loaded) return loaded;
	const spec = JSON.parse(readFileSync(SPEC_PATH, "utf8")) as Spec;
	// The document is registered as a whole (not as a schema of its own: strict
	// mode off lets `paths`, `info` ... pass as unknown keywords) so that a
	// pointer into it names an operation's schema or a component alike.
	const ajv = new Ajv2020({
		strict: false,
		allErrors: true,
		verbose: true,
		validateSchema: false,
	});
	addFormats(ajv);
	// openapi-ts and huma spell sized numbers as formats; the type already says all there is to check.
	for (const name of ["int32", "int64", "float", "double"]) {
		ajv.addFormat(name, { type: "number", validate: () => true });
	}
	ajv.addSchema({ $id: SPEC_ID, ...spec });
	loaded = { spec, ajv };
	return loaded;
}

/** The parsed openapi.json. */
export function specDocument(): Spec {
	return contract().spec;
}

/** A JSON pointer token ("/services/{id}" -> "~1services~1%7Bid%7D"), URI-fragment safe. */
const token = (part: string) =>
	encodeURIComponent(part.replaceAll("~", "~0").replaceAll("/", "~1"));

/** The pointer of a named component schema. */
export function componentPointer(name: string): string {
	return `/components/schemas/${token(name)}`;
}

/** The pointer of the schema of an operation's `responses`/`requestBody` media type. */
export function operationPointer(
	path: string,
	method: string,
	...rest: string[]
): string {
	return `/paths/${token(path)}/${method.toLowerCase()}/${rest.map(token).join("/")}`;
}

const compiled = new Map<string, ValidateFunction>();

function validatorAt(pointer: string): ValidateFunction {
	const cached = compiled.get(pointer);
	if (cached) return cached;
	const validate = contract().ajv.getSchema(`${SPEC_ID}#${pointer}`);
	if (!validate) throw new Error(`json-schema: no schema at ${pointer}`);
	compiled.set(pointer, validate);
	return validate;
}

function describe(value: unknown): string {
	if (value === null) return "null";
	if (Array.isArray(value)) return "array";
	if (typeof value === "number" && Number.isInteger(value)) return "integer";
	return typeof value;
}

/** "/a/0/b" -> "$.a[0].b" */
function jsonPath(instancePath: string): string {
	let path = "$";
	for (const part of instancePath.split("/").slice(1)) {
		const key = part.replaceAll("~1", "/").replaceAll("~0", "~");
		path += /^\d+$/.test(key) ? `[${key}]` : `.${key}`;
	}
	return path;
}

function message(error: ErrorObject): string {
	const { params } = error;
	switch (error.keyword) {
		case "type":
			return `expected ${String(params.type).replaceAll(",", " or ")}, got ${describe(error.data)}`;
		case "required":
			return `missing required property "${String(params.missingProperty)}"`;
		case "additionalProperties":
			return `unexpected property "${String(params.additionalProperty)}"`;
		case "enum":
			return `${JSON.stringify(error.data)} is not one of ${params.allowedValues.map((o: unknown) => JSON.stringify(o)).join(", ")}`;
		case "const":
			return `expected ${JSON.stringify(params.allowedValue)}, got ${JSON.stringify(error.data)}`;
		case "format":
			return `${JSON.stringify(error.data)} is not a valid ${String(params.format)}`;
		default:
			return error.message ?? error.keyword;
	}
}

/**
 * Drops the noise a "T or null" union (how huma spells a nullable object)
 * adds: the failed null branch and the "matches none" summary around the
 * errors of the branch the value was meant for.
 */
function relevant(errors: ErrorObject[]): ErrorObject[] {
	const unions = errors.filter(
		(e) => e.keyword === "oneOf" || e.keyword === "anyOf",
	);
	const noise = (e: ErrorObject) => {
		if (e.keyword === "oneOf" || e.keyword === "anyOf") {
			const inner = errors.filter(
				(other) =>
					other !== e &&
					other.schemaPath.startsWith(`${e.schemaPath}/`) &&
					other.keyword !== "type",
			);
			return inner.length > 0 || errors.some((o) => isNullBranch(o, e));
		}
		return unions.some((u) => isNullBranch(e, u));
	};
	const kept = errors.filter((e) => !noise(e));
	return kept.length > 0 ? kept : errors;
}

/** A failed `type: "null"` branch of the union `union` (an anyOf/oneOf error). */
function isNullBranch(error: ErrorObject, union: ErrorObject): boolean {
	return (
		error.keyword === "type" &&
		error.params.type === "null" &&
		error.schemaPath.startsWith(`${union.schemaPath}/`)
	);
}

/**
 * The ways `value` breaks the schema at `pointer` (at most five), each as
 * "$.path: message"; empty when it conforms. `value` should be what travels on
 * the wire (the result of a JSON round trip), so undefined members are gone.
 */
export function validateAt(pointer: string, value: unknown): string[] {
	const validate = validatorAt(pointer);
	if (validate(value)) return [];
	const seen = new Set<string>();
	for (const error of relevant(validate.errors ?? [])) {
		seen.add(`${jsonPath(error.instancePath)}: ${message(error)}`);
		if (seen.size >= MAX_PROBLEMS) break;
	}
	return [...seen];
}
