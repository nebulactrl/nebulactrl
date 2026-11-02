import type { ApiRouteKey } from "./api-routes.gen";
import {
	checkRequestBody,
	checkResponseBody,
	findOperation,
	type Operation,
} from "./contract";

/**
 * A canned reply, or a function that builds the Response itself. A canned
 * reply's status must be one the spec documents for the route and its body
 * must fit that response's schema; a function reply is not checked (it is how
 * a test sends the malformed or undocumented reply on purpose).
 *
 * Build bodies with the factories (src/test/factories.ts): `make("Project")`
 * is a complete, valid Project, `make("Project", { name: "Shop" })` states
 * only what the test cares about.
 */
export type ApiReply =
	| CannedReply
	| ((request: Request) => Response | Promise<Response>);

interface CannedReply {
	status?: number;
	body?: unknown;
}

/**
 * Route keys: "METHOD /path" below /api/v1, a path parameter spelled `:name`
 * or as a concrete value. The generated key type (api-routes.gen.ts) rejects a
 * method or static segment that is not in the API; stubApi itself rejects a key
 * whose whole path is not an operation.
 */
export type ApiRoutes = { [K in ApiRouteKey | RawRouteKey]?: ApiReply };

/**
 * Routes the server serves outside the OpenAPI document: raw byte streams that
 * huma does not model (the spec documents them only in prose, on the service's
 * `/files` tag). Their replies and request bodies are not checked. A route
 * belongs here only when the Go handler is mounted straight on the router.
 */
const RAW_ROUTES = [
	"GET /services/:serviceId/files/content",
	"POST /services/:serviceId/files/upload",
];
type RawRouteKey =
	| `GET /services/${string}/files/content`
	| `POST /services/${string}/files/upload`;

export interface ApiCall {
	method: string;
	/** The path below /api/v1, without the query string. */
	path: string;
	query: URLSearchParams;
	/** The parsed JSON body, or undefined. */
	body: unknown;
}

export interface ApiStub {
	/** Every call that matched a route, in order. */
	calls: ApiCall[];
	/** Calls no route matched ("GET /path"); each one also rejects. */
	unmatched: string[];
}

const API_PREFIX = "/api/v1";

/** Request-contract violations since the last take; setup.ts fails the test on any. */
const violations: string[] = [];

/** Returns and clears the violations recorded by stubApi's calls. */
export function takeApiViolations(): string[] {
	return violations.splice(0);
}

function contractError(
	what: string,
	key: string,
	operation: Operation,
	problems: string[],
): Error {
	return new Error(
		`stubApi: ${what} for "${key}" breaks the contract (${operation.label}):\n  ${problems.join("\n  ")}`,
	);
}

/** A stubbed route: its key split into method and path pattern, the operation it names and its reply. */
interface Route {
	key: string;
	method: string;
	pattern: string;
	/** Undefined for the raw byte routes outside the spec. */
	operation: Operation | undefined;
	reply: ApiReply;
}

/** "/projects/:id" matches "/projects/42"; other segments match literally. */
function matches(pattern: string, path: string): boolean {
	const want = pattern.split("/");
	const got = path.split("/");
	return (
		want.length === got.length &&
		want.every((segment, i) => segment.startsWith(":") || segment === got[i])
	);
}

/** The status and body a canned reply sends; throws if they break the contract. */
function cannedReply(
	key: string,
	operation: Operation,
	reply: CannedReply,
): { status: number; body: unknown } {
	const status = reply.status ?? 200;
	const body = reply.body;
	const problems = checkResponseBody(operation, status, body);
	if (problems.length > 0) {
		throw contractError(`the ${status} reply`, key, operation, problems);
	}
	return { status, body };
}

/**
 * Replaces fetch for API calls: `routes` maps "METHOD /path" (below /api/v1,
 * `:name` for a path segment) to a reply. A call without a route rejects and is
 * listed in `unmatched`, so a test notices a request it did not expect.
 *
 * The stub holds the API to its contract (openapi/openapi.json): a key that is
 * not an operation throws, as does a canned reply whose status or body the spec
 * does not allow (checked here and again when the call is answered). A request
 * body the spec does not allow rejects the call and is reported by
 * takeApiViolations, which src/test/setup.ts turns into a failure after the
 * test. src/test/setup.ts also restores the real fetch after every test.
 */
export function stubApi(routes: ApiRoutes): ApiStub {
	const stub: ApiStub = { calls: [], unmatched: [] };
	const entries: [string, ApiReply | undefined][] = Object.entries(routes);
	const table = entries.flatMap<Route>(([key, reply]) => {
		if (reply === undefined) return [];
		const [method = "", pattern = ""] = key.split(" ");
		const operation = findOperation(method, pattern);
		const raw = RAW_ROUTES.some((route) => {
			const [rawMethod = "", rawPattern = ""] = route.split(" ");
			return rawMethod === method && matches(rawPattern, pattern);
		});
		if (!operation && raw) {
			return [{ key, method: method.toUpperCase(), pattern, operation, reply }];
		}
		if (!operation) {
			throw new Error(
				`stubApi: "${key}" is not an operation of the API (openapi/openapi.json)`,
			);
		}
		if (typeof reply !== "function") cannedReply(key, operation, reply);
		return [{ key, method: method.toUpperCase(), pattern, operation, reply }];
	});
	globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
		const request =
			input instanceof Request
				? input
				: new Request(new URL(String(input), location.href), init);
		const url = new URL(request.url, location.href);
		const path = url.pathname.startsWith(API_PREFIX)
			? url.pathname.slice(API_PREFIX.length) || "/"
			: url.pathname;
		const method = request.method.toUpperCase();
		const route = table.find(
			(r) => r.method === method && matches(r.pattern, path),
		);
		if (!route) {
			stub.unmatched.push(`${method} ${path}`);
			throw new Error(`stubApi: no route for ${method} ${path}`);
		}
		const text = await request.clone().text();
		const sent: unknown = text ? JSON.parse(text) : undefined;
		stub.calls.push({
			method,
			path,
			query: url.searchParams,
			body: sent,
		});
		const { operation } = route;
		const problems = operation ? checkRequestBody(operation, sent) : [];
		if (operation && problems.length > 0) {
			const error = contractError(
				"the request body the app sent",
				`${method} ${path}`,
				operation,
				problems,
			);
			violations.push(error.message);
			throw error;
		}
		if (typeof route.reply === "function") return route.reply(request);
		let canned: { status: number; body: unknown };
		try {
			canned = operation
				? cannedReply(route.key, operation, route.reply)
				: { status: route.reply.status ?? 200, body: route.reply.body };
		} catch (error) {
			violations.push(String((error as Error).message));
			throw error;
		}
		const { status, body } = canned;
		return new Response(body === undefined ? null : JSON.stringify(body), {
			status,
			headers: { "Content-Type": "application/json" },
		});
	}) as typeof fetch;
	return stub;
}
