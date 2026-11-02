/**
 * The API contract (openapi/openapi.json at the repo root) as stubApi sees it:
 * which operation a route key or request names, and what bodies it may carry.
 */

import { operationPointer, specDocument, validateAt } from "./json-schema";

interface Media {
	schema?: unknown;
}
interface Response {
	content?: Record<string, Media>;
}
interface RequestBody {
	required?: boolean;
	content?: Record<string, Media>;
}
interface OperationObject {
	requestBody?: RequestBody;
	responses?: Record<string, Response>;
}

export interface Operation {
	/** "GET /services/{serviceId}": the path as the spec writes it. */
	label: string;
	/** The path as the spec writes it: "/services/{serviceId}". */
	path: string;
	/** Lower case: "get". */
	method: string;
	segments: string[];
	object: OperationObject;
}

const HTTP_METHODS = new Set([
	"get",
	"put",
	"post",
	"delete",
	"options",
	"head",
	"patch",
]);
const JSON_TYPES = ["application/json", "application/problem+json"];

let loaded: Map<string, Operation[]> | undefined;

/** The operations of the spec by upper-case method, indexed once per process. */
function contract() {
	if (loaded) return loaded;
	const spec = specDocument();
	const operations = new Map<string, Operation[]>();
	for (const [path, item] of Object.entries(spec.paths)) {
		for (const [method, object] of Object.entries(item)) {
			if (!HTTP_METHODS.has(method) || !object) continue;
			const key = method.toUpperCase();
			const list = operations.get(key) ?? [];
			list.push({
				label: `${key} ${path}`,
				path,
				method,
				segments: path.split("/"),
				object: object as OperationObject,
			});
			operations.set(key, list);
		}
	}
	loaded = operations;
	return loaded;
}

/**
 * The operation a "METHOD /path" names. `path` is below /api/v1 and may spell
 * a parameter as ":name" or as a concrete value ("/services/svc-1"); a static
 * segment of the spec must be spelled exactly. Where a concrete value could
 * be a parameter or a static segment, the static reading wins.
 */
export function findOperation(
	method: string,
	path: string,
): Operation | undefined {
	const given = path.split("/");
	let best: Operation | undefined;
	let bestStatics = -1;
	for (const operation of contract().get(method.toUpperCase()) ?? []) {
		const { segments } = operation;
		if (segments.length !== given.length) continue;
		let statics = 0;
		const fits = segments.every((segment, i) => {
			const got = given[i] ?? "";
			if (segment.startsWith("{")) return got !== "";
			if (segment !== got) return false;
			statics++;
			return true;
		});
		if (fits && statics > bestStatics) {
			best = operation;
			bestStatics = statics;
		}
	}
	return best;
}

/** The media type of `content` that carries JSON, if any. */
function jsonType(content: Record<string, Media> | undefined) {
	return JSON_TYPES.find((type) => content?.[type]?.schema);
}

/** What the contract says about a `status` reply: where its JSON schema is, or why there is none. */
function responseShape(
	operation: Operation,
	status: number,
):
	| { pointer: string }
	| { opaque: true }
	| { empty: true }
	| { undocumented: string } {
	const responses = operation.object.responses ?? {};
	const code = String(status) in responses ? String(status) : "default";
	const response = responses[code];
	if (!response) {
		return {
			undocumented: `the spec documents no ${status} response (documented: ${Object.keys(responses).join(", ")})`,
		};
	}
	const type = jsonType(response.content);
	if (type) {
		return {
			pointer: operationPointer(
				operation.path,
				operation.method,
				"responses",
				code,
				"content",
				type,
				"schema",
			),
		};
	}
	// Streams, text and HTML replies cannot be checked as JSON.
	return response.content && Object.keys(response.content).length > 0
		? { opaque: true }
		: { empty: true };
}

/** Problems with a response body; empty when it fits (or the spec cannot say). */
export function checkResponseBody(
	operation: Operation,
	status: number,
	body: unknown,
): string[] {
	const shape = responseShape(operation, status);
	if ("undocumented" in shape) return [shape.undocumented];
	if ("opaque" in shape) return [];
	if ("empty" in shape) {
		return body === undefined
			? []
			: [`$: the spec documents no body for ${status}, the reply has one`];
	}
	if (body === undefined) {
		return ["$: the spec documents a body, the reply has none"];
	}
	return validateAt(shape.pointer, wire(body));
}

/** Problems with a request body; empty when it fits. */
export function checkRequestBody(
	operation: Operation,
	body: unknown,
): string[] {
	const requestBody = operation.object.requestBody;
	if (!requestBody) {
		return body === undefined
			? []
			: ["$: the spec documents no request body, the app sent one"];
	}
	if (body === undefined) {
		return requestBody.required
			? ["$: the spec requires a request body, the app sent none"]
			: [];
	}
	const type = jsonType(requestBody.content);
	return type
		? validateAt(
				operationPointer(
					operation.path,
					operation.method,
					"requestBody",
					"content",
					type,
					"schema",
				),
				wire(body),
			)
		: [];
}

/** What the body looks like after the wire: undefined members are gone. */
const wire = (body: unknown): unknown => JSON.parse(JSON.stringify(body));
