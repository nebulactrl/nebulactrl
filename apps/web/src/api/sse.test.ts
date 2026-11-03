import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { act, configure, renderHook, waitFor } from "@testing-library/react";
import { ApiError, setApiHandlers } from "./client-config";
import {
	startEventsFeed as startFeed,
	type UseLogStreamOptions,
	useEventsStatus,
	useLogStream,
} from "./sse";

const running: Array<() => void> = [];

/** Starts the feed and stops it after the test, so no stream leaks into the next. */
function startEventsFeed(queryClient: QueryClient): () => void {
	let stopped = false;
	const stop = startFeed(queryClient);
	const once = () => {
		if (stopped) return;
		stopped = true;
		stop();
	};
	running.push(once);
	return once;
}
interface Frame {
	data?: unknown;
	event?: string;
}

/** One open server-sent stream the test writes to by hand. */
class FakeStream {
	readonly request: Request;
	readonly response: Response;
	#controller!: ReadableStreamDefaultController<Uint8Array>;
	#encoder = new TextEncoder();
	closed = false;

	constructor(request: Request) {
		this.request = request;
		const body = new ReadableStream<Uint8Array>({
			start: (controller) => {
				this.#controller = controller;
			},
			cancel: () => {
				this.closed = true;
			},
		});
		this.response = new Response(body, {
			headers: { "Content-Type": "text/event-stream" },
		});
	}

	get url(): URL {
		return new URL(this.request.url);
	}

	send({ data, event }: Frame): void {
		let text = "";
		if (event) text += `event: ${event}\n`;
		if (data !== undefined) text += `data: ${JSON.stringify(data)}\n`;
		this.#controller.enqueue(this.#encoder.encode(`${text}\n`));
	}

	/** The server ends the response normally. */
	end(): void {
		this.closed = true;
		this.#controller.close();
	}
}

type Reply = Response | "stream";

interface Server {
	/** Every request that reached the server, in order. */
	requests: Request[];
	/** Streams the server opened, in order. */
	streams: FakeStream[];
}

/**
 * Replaces fetch with a server that answers each request with the next reply
 * (`"stream"` opens a FakeStream); once the replies run out every request
 * opens a stream.
 */
function serve(replies: Reply[] = []): Server {
	const server: Server = { requests: [], streams: [] };
	const queue = [...replies];
	globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
		const request =
			input instanceof Request ? input : new Request(String(input), init);
		server.requests.push(request);
		const reply = queue.shift() ?? "stream";
		if (reply !== "stream") return reply;
		const stream = new FakeStream(request);
		server.streams.push(stream);
		return stream.response;
	}) as typeof fetch;
	return server;
}

const realSetTimeout = globalThis.setTimeout;
let delays: number[];

/** The waits the retry loop asks for: 1 s doubling up to 30 s. */
const BACKOFFS = new Set([1000, 2000, 4000, 8000, 16_000, 30_000]);

/**
 * Retry waits run at once; the delays the code asked for are recorded.
 * waitFor's own 1 s timeout would match a backoff, so its timeout is raised.
 */
function skipBackoff(): void {
	delays = [];
	configure({ asyncUtilTimeout: 1500 });
	globalThis.setTimeout = ((
		fn: () => void,
		ms?: number,
		...rest: unknown[]
	) => {
		if (typeof ms === "number" && BACKOFFS.has(ms)) {
			delays.push(ms);
			return realSetTimeout(fn, 0, ...rest);
		}
		return realSetTimeout(fn, ms, ...rest);
	}) as typeof setTimeout;
}

beforeEach(() => {
	delays = [];
});

afterEach(() => {
	for (const stop of running.splice(0)) stop();
	configure({ asyncUtilTimeout: 1000 });
	globalThis.setTimeout = realSetTimeout;
	setApiHandlers({});
});

const stale = (queryClient: QueryClient, id: string) =>
	queryClient.getQueryState([{ _id: id }])?.isInvalidated;

function seed(...ids: string[]): QueryClient {
	const queryClient = new QueryClient();
	for (const id of ids) queryClient.setQueryData([{ _id: id }], 1);
	return queryClient;
}

describe("the events feed", () => {
	test("opens GET /events as an event stream with the session cookie", async () => {
		const server = serve();
		const stop = startEventsFeed(seed());
		await waitFor(() => expect(server.requests).toHaveLength(1));
		const [request] = server.requests;
		expect(request?.method).toBe("GET");
		expect(new URL(request?.url ?? "").pathname).toBe("/api/v1/events");
		expect(request?.headers.get("Accept")).toBe("text/event-stream");
		expect(request?.credentials).toBe("include");
		stop();
	});

	test("an event makes exactly the queries it names stale", async () => {
		const server = serve();
		const queryClient = seed(
			"listProjects",
			"listEnvironments",
			"listServers",
			"getMe",
		);
		const stop = startEventsFeed(queryClient);
		await waitFor(() => expect(server.streams).toHaveLength(1));

		server.streams[0]?.send({ data: { type: "project.created" } });

		await waitFor(() => expect(stale(queryClient, "listProjects")).toBe(true));
		expect(stale(queryClient, "listEnvironments")).toBe(false);
		expect(stale(queryClient, "listServers")).toBe(false);
		expect(stale(queryClient, "getMe")).toBe(false);
		stop();
	});

	test("task and run events refresh the service views of cron services", async () => {
		const server = serve();
		const queryClient = seed(
			"listServices",
			"getService",
			"listServiceTaskRuns",
			"listServers",
		);
		const stop = startEventsFeed(queryClient);
		await waitFor(() => expect(server.streams).toHaveLength(1));

		server.streams[0]?.send({ data: { type: "task.run.updated" } });

		await waitFor(() => expect(stale(queryClient, "listServices")).toBe(true));
		expect(stale(queryClient, "getService")).toBe(true);
		expect(stale(queryClient, "listServiceTaskRuns")).toBe(true);
		expect(stale(queryClient, "listServers")).toBe(false);
		stop();
	});

	test("a burst of events is merged into one invalidation of every named query", async () => {
		const server = serve();
		const queryClient = seed("listProjects", "listEnvironments", "listTeams");
		const stop = startEventsFeed(queryClient);
		await waitFor(() => expect(server.streams).toHaveLength(1));

		const stream = server.streams[0];
		stream?.send({ data: { type: "project.updated" } });
		stream?.send({ data: { type: "environment.created" } });
		stream?.send({ data: { type: "not.a.real.event" } });

		await waitFor(() => {
			expect(stale(queryClient, "listProjects")).toBe(true);
			expect(stale(queryClient, "listEnvironments")).toBe(true);
		});
		expect(stale(queryClient, "listTeams")).toBe(false);
		stop();
	});

	test("stopping closes the stream and stops reacting to events", async () => {
		const server = serve();
		const queryClient = seed("listProjects");
		const stop = startEventsFeed(queryClient);
		await waitFor(() => expect(server.streams).toHaveLength(1));
		stop();
		await waitFor(() => expect(server.streams[0]?.closed).toBe(true));
		expect(stale(queryClient, "listProjects")).toBe(false);
	});

	test("useEventsStatus follows the connection: connecting, live, then connecting again once stopped", async () => {
		const server = serve();
		const { result } = renderHook(() => useEventsStatus());
		expect(result.current).toBe("connecting");

		const stop = startEventsFeed(seed());
		await waitFor(() => expect(server.streams).toHaveLength(1));
		server.streams[0]?.send({ data: { type: "project.created" } });
		await waitFor(() => expect(result.current).toBe("live"));

		await act(async () => stop());
		expect(result.current).toBe("connecting");
	});

	test("a failing feed reads paused, retries with doubling delays and refetches everything once it is back", async () => {
		skipBackoff();
		const unavailable = () =>
			new Response(null, { status: 503, statusText: "Unavailable" });
		const server = serve([unavailable(), unavailable(), "stream"]);
		const queryClient = seed("getMe", "listProjects");
		const { result } = renderHook(() => useEventsStatus());
		const stop = startEventsFeed(queryClient);

		await waitFor(() => expect(server.requests).toHaveLength(3));
		expect(delays.slice(0, 2)).toEqual([1000, 2000]);

		server.streams[0]?.send({ data: { type: "project.created" } });
		await waitFor(() => expect(result.current).toBe("live"));
		// The outage missed events, so even queries no event named are stale.
		await waitFor(() => expect(stale(queryClient, "getMe")).toBe(true));
		expect(stale(queryClient, "listProjects")).toBe(true);
		stop();
	});

	test("a 401 on the feed ends the session and is not retried", async () => {
		skipBackoff();
		const reported: ApiError[] = [];
		setApiHandlers({
			onUnauthorized: () => reported.push(new ApiError(401, "", "")),
		});
		const server = serve([new Response(null, { status: 401 })]);
		const stop = startEventsFeed(seed());
		await waitFor(() => expect(reported).toHaveLength(1));
		expect(server.requests).toHaveLength(1);
		expect(delays).toEqual([]);
		stop();
	});

	test("a 403 on the feed goes to the forbidden handler", async () => {
		const forbidden: number[] = [];
		setApiHandlers({ onForbidden: (error) => forbidden.push(error.status) });
		serve([new Response(null, { status: 403 })]);
		const stop = startEventsFeed(seed());
		await waitFor(() => expect(forbidden).toEqual([403]));
		stop();
	});

	test("reconnects after the server closes the feed cleanly", async () => {
		skipBackoff();
		const server = serve();
		const stop = startEventsFeed(seed());
		await waitFor(() => expect(server.streams).toHaveLength(1));
		server.streams[0]?.send({ data: { type: "project.created" } });
		server.streams[0]?.end();
		await waitFor(() => expect(server.streams).toHaveLength(2));
		stop();
	});
});

function renderLogs(options: UseLogStreamOptions) {
	return renderHook((props: UseLogStreamOptions) => useLogStream(props), {
		initialProps: options,
	});
}

const logLine = (line: string, extra: Record<string, unknown> = {}) => ({
	data: { line, ...extra },
});

describe("useLogStream", () => {
	test("a service tail asks for follow and the requested number of lines of history", async () => {
		const server = serve();
		renderLogs({ kind: "service", id: "svc-1", tail: 50 });
		await waitFor(() => expect(server.requests).toHaveLength(1));
		const url = new URL(server.requests[0]?.url ?? "");
		expect(url.pathname).toBe("/api/v1/services/svc-1/logs");
		expect(url.searchParams.get("follow")).toBe("true");
		expect(url.searchParams.get("tail")).toBe("50");
	});

	test("defaults to 200 lines of history and never asks for a negative tail", async () => {
		const server = serve();
		renderLogs({ kind: "service", id: "svc-1" });
		renderLogs({ kind: "environment", id: "env-1", tail: -5 });
		await waitFor(() => expect(server.requests).toHaveLength(2));
		const tails = Object.fromEntries(
			server.requests.map((r) => {
				const url = new URL(r.url);
				return [url.pathname, url.searchParams.get("tail")];
			}),
		);
		expect(tails).toEqual({
			"/api/v1/services/svc-1/logs": "200",
			"/api/v1/environments/env-1/logs": "0",
		});
	});

	test("a compose component and a process narrow a service stream", async () => {
		const server = serve();
		renderLogs({
			kind: "service",
			id: "svc-1",
			component: "api",
			process: "worker",
		});
		await waitFor(() => expect(server.requests).toHaveLength(1));
		const params = new URL(server.requests[0]?.url ?? "").searchParams;
		expect(params.get("component")).toBe("api");
		expect(params.get("process")).toBe("worker");
	});

	test("deployment and task-run streams follow without a tail; only deployments take a streams filter", async () => {
		const server = serve();
		renderLogs({ kind: "deployment", id: "dep-1", streams: "build,deploy" });
		renderLogs({
			kind: "taskRun",
			id: "run-1",
			streams: "debug",
			component: "x",
			process: "y",
		});
		await waitFor(() => expect(server.requests).toHaveLength(2));
		const byPath = Object.fromEntries(
			server.requests.map((r) => {
				const url = new URL(r.url);
				return [url.pathname, url.searchParams];
			}),
		);
		const deployment = byPath["/api/v1/deployments/dep-1/logs"];
		expect(deployment?.get("follow")).toBe("true");
		expect(deployment?.has("tail")).toBe(false);
		expect(deployment?.get("streams")).toBe("build,deploy");
		const run = byPath["/api/v1/task-runs/run-1/logs"];
		expect(run?.get("follow")).toBe("true");
		expect([...(run?.keys() ?? [])]).toEqual(["follow"]);
	});

	test("lines arrive in order with their metadata, and the stream goes live", async () => {
		const server = serve();
		const { result } = renderLogs({ kind: "deployment", id: "dep-1" });
		expect(result.current.status).toBe("connecting");
		await waitFor(() => expect(server.streams).toHaveLength(1));

		const stream = server.streams[0];
		stream?.send(
			logLine("building", {
				ts: "2026-01-01T00:00:00Z",
				stream: "build",
				serviceId: "svc-1",
				serviceName: "web",
			}),
		);
		stream?.send(logLine("done"));
		await waitFor(() => expect(result.current.lines).toHaveLength(2));

		expect(result.current.status).toBe("live");
		expect(result.current.lines).toEqual([
			{
				id: 1,
				line: "building",
				ts: "2026-01-01T00:00:00Z",
				stream: "build",
				serviceId: "svc-1",
				serviceName: "web",
			},
			{ id: 2, line: "done" },
		]);
	});

	test("frames without a text line are ignored", async () => {
		const server = serve();
		const { result } = renderLogs({ kind: "service", id: "svc-1" });
		await waitFor(() => expect(server.streams).toHaveLength(1));
		const stream = server.streams[0];
		stream?.send({ data: { line: 42 } });
		stream?.send({ data: { note: "heartbeat" } });
		stream?.send({ data: "just text" });
		stream?.send(logLine("real"));
		await waitFor(() => expect(result.current.lines).toHaveLength(1));
		expect(result.current.lines.map((l) => l.line)).toEqual(["real"]);
	});

	test("keeps only the newest maxLines", async () => {
		const server = serve();
		const { result } = renderLogs({
			kind: "service",
			id: "svc-1",
			maxLines: 3,
		});
		await waitFor(() => expect(server.streams).toHaveLength(1));
		for (const n of [1, 2, 3, 4, 5]) server.streams[0]?.send(logLine(`l${n}`));
		await waitFor(() =>
			expect(result.current.lines.map((l) => l.line)).toEqual([
				"l3",
				"l4",
				"l5",
			]),
		);
	});

	test("an environment tail merges its services by time and bumps the epoch when it reorders", async () => {
		const server = serve();
		const { result } = renderLogs({ kind: "environment", id: "env-1" });
		await waitFor(() => expect(server.streams).toHaveLength(1));
		const stream = server.streams[0];

		stream?.send(logLine("b", { ts: "2026-01-01T00:00:02Z" }));
		await waitFor(() => expect(result.current.lines).toHaveLength(1));
		expect(result.current.epoch).toBe(0);

		stream?.send(logLine("a", { ts: "2026-01-01T00:00:01Z" }));
		await waitFor(() => expect(result.current.lines).toHaveLength(2));
		expect(result.current.lines.map((l) => l.line)).toEqual(["a", "b"]);
		expect(result.current.epoch).toBe(1);
	});

	test("clear empties the view and later lines still arrive", async () => {
		const server = serve();
		const { result } = renderLogs({ kind: "service", id: "svc-1" });
		await waitFor(() => expect(server.streams).toHaveLength(1));
		server.streams[0]?.send(logLine("one"));
		await waitFor(() => expect(result.current.lines).toHaveLength(1));
		act(() => result.current.clear());
		expect(result.current.lines).toEqual([]);
		server.streams[0]?.send(logLine("two"));
		await waitFor(() => expect(result.current.lines).toHaveLength(1));
		expect(result.current.lines[0]?.line).toBe("two");
	});

	test("without an id, or when disabled, nothing is opened and the stream reads ended", async () => {
		const server = serve();
		const none = renderLogs({ kind: "service", id: undefined });
		const off = renderLogs({ kind: "service", id: "svc-1", enabled: false });
		await waitFor(() => expect(none.result.current.status).toBe("ended"));
		await waitFor(() => expect(off.result.current.status).toBe("ended"));
		expect(server.requests).toEqual([]);
	});

	test("an event: end frame finishes the stream and it is not reopened", async () => {
		skipBackoff();
		const server = serve();
		const { result } = renderLogs({ kind: "deployment", id: "dep-1" });
		await waitFor(() => expect(server.streams).toHaveLength(1));
		server.streams[0]?.send(logLine("last"));
		server.streams[0]?.send({ event: "end" });
		await waitFor(() => expect(result.current.status).toBe("ended"));
		expect(server.requests).toHaveLength(1);
		expect(delays).toEqual([]);
	});

	test("a log stream the server closes cleanly ends instead of reconnecting", async () => {
		skipBackoff();
		const server = serve();
		const { result } = renderLogs({ kind: "service", id: "svc-1" });
		await waitFor(() => expect(server.streams).toHaveLength(1));
		server.streams[0]?.send(logLine("x"));
		server.streams[0]?.end();
		await waitFor(() => expect(result.current.status).toBe("ended"));
		expect(server.requests).toHaveLength(1);
	});

	test("a failing log stream reads paused and reconnects with backoff", async () => {
		skipBackoff();
		const server = serve([
			new Response(null, { status: 500, statusText: "Boom" }),
			"stream",
		]);
		const { result } = renderLogs({ kind: "service", id: "svc-1" });
		await waitFor(() => expect(server.requests).toHaveLength(2));
		expect(delays[0]).toBe(1000);
		server.streams[0]?.send(logLine("back"));
		await waitFor(() => expect(result.current.status).toBe("live"));
		expect(result.current.lines.map((l) => l.line)).toEqual(["back"]);
	});

	test("changing the id starts over with an empty view on the new stream", async () => {
		const server = serve();
		const { result, rerender } = renderLogs({ kind: "service", id: "svc-1" });
		await waitFor(() => expect(server.streams).toHaveLength(1));
		server.streams[0]?.send(logLine("old"));
		await waitFor(() => expect(result.current.lines).toHaveLength(1));

		rerender({ kind: "service", id: "svc-2" });
		await waitFor(() => expect(server.streams).toHaveLength(2));
		expect(server.streams[1]?.url.pathname).toBe("/api/v1/services/svc-2/logs");
		expect(result.current.lines).toEqual([]);
		await waitFor(() => expect(server.streams[0]?.closed).toBe(true));
	});

	test("two views of the same tail share one connection", async () => {
		const server = serve();
		const first = renderLogs({ kind: "service", id: "svc-1" });
		const second = renderLogs({ kind: "service", id: "svc-1" });
		await waitFor(() => expect(server.streams).toHaveLength(1));
		server.streams[0]?.send(logLine("shared"));
		await waitFor(() => {
			expect(first.result.current.lines).toHaveLength(1);
			expect(second.result.current.lines).toHaveLength(1);
		});
		expect(server.requests).toHaveLength(1);
	});
});
