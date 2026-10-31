import type {
	ApiOperation,
	ApiParam,
	ApiProperty,
	ApiResponse,
	ApiSchema,
} from "../types.ts";

// The API reference is generated from openapi/openapi.json: grouped by tag,
// with schemas converted to a compact tree that the UI expands on demand. The
// spec is 1.4 MB; the browser only ever receives one tag's slice.

// biome-ignore lint/suspicious/noExplicitAny: the OpenAPI document is untyped JSON
type Json = any;

export interface ApiTagData {
	name: string;
	description: string;
	operations: ApiOperation[];
	defs: Record<string, ApiSchema>;
}

const METHODS = ["get", "post", "put", "patch", "delete"] as const;

class Converter {
	readonly defs: Record<string, ApiSchema> = {};
	private readonly known = new Set<string>();
	private readonly doc: Json;

	// No parameter properties: the web build loads this package through Node's
	// strip-only TypeScript mode, which supports erasable syntax only.
	constructor(doc: Json) {
		this.doc = doc;
	}

	/** Registers `name` (and everything it reaches) in `defs`. */
	private define(name: string) {
		if (this.known.has(name)) return;
		this.known.add(name);
		const raw = this.doc.components?.schemas?.[name];
		this.defs[name] = {};
		if (raw) this.defs[name] = this.convert(raw);
	}

	convert(schema: Json): ApiSchema {
		if (!schema || typeof schema !== "object") return {};
		if (typeof schema.$ref === "string") {
			const name = (schema.$ref as string).split("/").pop() as string;
			this.define(name);
			return { ref: name };
		}
		const out: ApiSchema = {};
		const types: string[] = Array.isArray(schema.type)
			? schema.type
			: schema.type
				? [schema.type]
				: [];
		if (types.includes("null")) out.nullable = true;
		const type = types.find((t) => t !== "null");
		if (type) out.type = type;
		if (schema.format) out.format = String(schema.format);
		const description =
			typeof schema.description === "string" ? schema.description.trim() : "";
		if (description) out.description = description;
		if (Array.isArray(schema.enum)) out.enum = schema.enum.map(String);
		if (schema.default !== undefined)
			out.default = JSON.stringify(schema.default);

		const members: Json[] = schema.oneOf ?? schema.anyOf ?? [];
		if (members.length > 0) {
			const variants: ApiSchema[] = [];
			for (const member of members) {
				const converted = this.convert(member);
				if (
					converted.type === undefined &&
					converted.nullable &&
					!converted.ref
				) {
					out.nullable = true;
					continue;
				}
				variants.push(converted);
			}
			if (variants.length > 0) out.variants = variants;
		}
		if (Array.isArray(schema.allOf)) {
			const merged = this.mergeAllOf(schema.allOf);
			if (merged.props) out.props = merged.props;
			out.type ??= "object";
		}

		if (schema.properties && typeof schema.properties === "object") {
			const required = new Set<string>(schema.required ?? []);
			out.props = Object.entries(schema.properties).map(
				([name, value]): ApiProperty => ({
					name,
					required: required.has(name),
					schema: this.convert(value),
				}),
			);
			out.type ??= "object";
		}
		if (
			schema.additionalProperties &&
			typeof schema.additionalProperties === "object"
		) {
			out.additional = this.convert(schema.additionalProperties);
		}
		if (schema.items) out.items = this.convert(schema.items);
		return out;
	}

	private mergeAllOf(parts: Json[]): { props?: ApiProperty[] } {
		const props: ApiProperty[] = [];
		for (const part of parts) {
			const resolved =
				typeof part.$ref === "string"
					? this.doc.components?.schemas?.[part.$ref.split("/").pop()]
					: part;
			if (!resolved) continue;
			const converted = this.convert(resolved);
			if (converted.props) props.push(...converted.props);
		}
		return props.length > 0 ? { props } : {};
	}
}

function typeLabel(schema: Json): string {
	const types: string[] = Array.isArray(schema?.type)
		? schema.type
		: schema?.type
			? [schema.type]
			: [];
	const type =
		types.find((t) => t !== "null") ??
		(schema?.$ref ? String(schema.$ref).split("/").pop() : "any");
	let label = String(type);
	if (type === "array" && schema?.items) label = `${typeLabel(schema.items)}[]`;
	if (schema?.format && type !== "array") label += ` (${schema.format})`;
	if (Array.isArray(schema?.enum)) label = `enum: ${schema.enum.join(" | ")}`;
	return label;
}

const ERROR_CODE = /\b(\d{3})\s+`([a-z][a-z0-9_]*)`/g;

/** Groups every operation by its first tag, in the order the spec declares tags. */
export function groupByTag(doc: Json): ApiTagData[] {
	const tagInfo = new Map<string, string>();
	for (const tag of doc.tags ?? [])
		tagInfo.set(tag.name, tag.description ?? "");
	const grouped = new Map<
		string,
		{ path: string; method: string; op: Json }[]
	>();
	for (const [path, item] of Object.entries<Json>(doc.paths ?? {})) {
		for (const method of METHODS) {
			const op = item?.[method];
			if (!op) continue;
			const tag: string = op.tags?.[0] ?? "other";
			const list = grouped.get(tag) ?? [];
			list.push({ path, method, op });
			grouped.set(tag, list);
		}
	}
	const names = [...grouped.keys()].sort((a, b) => a.localeCompare(b));
	return names.map((name) => {
		const converter = new Converter(doc);
		const entries = (grouped.get(name) ?? []).sort(
			(a, b) =>
				a.path.localeCompare(b.path) ||
				METHODS.indexOf(a.method as (typeof METHODS)[number]) -
					METHODS.indexOf(b.method as (typeof METHODS)[number]),
		);
		const operations = entries.map(({ path, method, op }) =>
			toOperation(doc, converter, path, method, op),
		);
		return {
			name,
			description: tagInfo.get(name) ?? "",
			operations,
			defs: converter.defs,
		};
	});
}

function toOperation(
	doc: Json,
	converter: Converter,
	path: string,
	method: string,
	op: Json,
): ApiOperation {
	const params: ApiParam[] = (op.parameters ?? []).map(
		(raw: Json): ApiParam => ({
			name: String(raw.name),
			in: raw.in,
			required: raw.in === "path" ? true : Boolean(raw.required),
			type: typeLabel(raw.schema ?? {}),
			description: String(
				raw.description ?? raw.schema?.description ?? "",
			).trim(),
		}),
	);

	const result: ApiOperation = {
		id: String(op.operationId ?? `${method}${path}`),
		method: method.toUpperCase() as ApiOperation["method"],
		path,
		summary: String(op.summary ?? "").trim(),
		description: String(op.description ?? "").trim(),
		public: isPublic(doc, op),
		params,
		responses: [],
		errorCodes: [],
	};

	const content = op.requestBody?.content;
	if (content && typeof content === "object") {
		const contentType = Object.keys(content)[0];
		if (contentType) {
			result.body = {
				contentType,
				required: Boolean(op.requestBody.required),
				schema: converter.convert(content[contentType]?.schema),
			};
		}
	}

	const responses: ApiResponse[] = [];
	for (const [status, raw] of Object.entries<Json>(op.responses ?? {})) {
		const response: ApiResponse = {
			status,
			description: String(raw.description ?? "").trim(),
		};
		const types = raw.content ? Object.keys(raw.content) : [];
		const first = types[0];
		if (first) {
			response.contentType = first;
			// Error bodies are all the same problem+json shape, documented once.
			if (!/^[45]/.test(status))
				response.schema = converter.convert(raw.content[first]?.schema);
		}
		responses.push(response);
	}
	result.responses = responses.sort((a, b) => a.status.localeCompare(b.status));

	const codes: { status: string; code: string }[] = [];
	for (const match of result.description.matchAll(ERROR_CODE)) {
		codes.push({ status: match[1] as string, code: match[2] as string });
	}
	result.errorCodes = codes;
	return result;
}

function isPublic(doc: Json, op: Json): boolean {
	const security = op.security ?? doc.security;
	if (!Array.isArray(security) || security.length === 0) return false;
	return security.some(
		(requirement: Json) => Object.keys(requirement ?? {}).length === 0,
	);
}
