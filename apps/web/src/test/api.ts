/** A canned reply, or a function that builds the Response itself. */
export type ApiReply =
	| { status?: number; body?: unknown }
	| ((request: Request) => Response | Promise<Response>);

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

/** "/projects/:id" matches "/projects/42"; other segments match literally. */
function matches(pattern: string, path: string): boolean {
	const want = pattern.split("/");
	const got = path.split("/");
	return (
		want.length === got.length &&
		want.every((segment, i) => segment.startsWith(":") || segment === got[i])
	);
}

/**
 * Replaces fetch for API calls: `routes` maps "METHOD /path" (below /api/v1,
 * `:name` for a path segment) to a reply. A call without a route rejects and is
 * listed in `unmatched`, so a test notices a request it did not expect.
 * src/test/setup.ts restores the real fetch after every test.
 */
export function stubApi(routes: Record<string, ApiReply>): ApiStub {
	const stub: ApiStub = { calls: [], unmatched: [] };
	const table = Object.entries(routes).map(([key, reply]) => {
		const [method = "", pattern = ""] = key.split(" ");
		return { method: method.toUpperCase(), pattern, reply };
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
		stub.calls.push({
			method,
			path,
			query: url.searchParams,
			body: text ? JSON.parse(text) : undefined,
		});
		if (typeof route.reply === "function") return route.reply(request);
		const { status = 200, body } = route.reply;
		return new Response(body === undefined ? null : JSON.stringify(body), {
			status,
			headers: { "Content-Type": "application/json" },
		});
	}) as typeof fetch;
	return stub;
}
