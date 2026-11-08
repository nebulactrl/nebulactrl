import { afterEach, beforeEach, describe, expect, jest, test } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { LogHistoryEntry, LogHistoryPage } from "#/api/gen/types.gen";
import type { LogLine } from "#/api/sse";
import type { RangeSelection } from "#/features/logs/history/model";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import type { LogSource } from "./sources";
import {
	insertByTime,
	LIVE_BUFFER,
	LiveMerger,
	newRequestPollState,
	REQUEST_POLL_MS,
	type RequestPollState,
	startRequestPoller,
	useExplorerLogs,
} from "./use-explorer-logs";

const stamp = (second: number) =>
	`2026-10-20T14:00:${String(second).padStart(2, "0")}.000Z`;

const request = (
	second: number,
	line = `GET /${second}`,
	serviceId = "svc-1",
): LogHistoryEntry => ({
	line,
	replica: 0,
	serviceId,
	stream: "requests",
	ts: stamp(second),
});

const pageOf = (
	items: LogHistoryEntry[],
	rest: Partial<LogHistoryPage> = {},
): LogHistoryPage => ({ capturing: true, items, ...rest });

/** Lets every promise already settled run its continuation. */
async function flush() {
	for (let i = 0; i < 20; i++) await Promise.resolve();
}

function setVisibility(state: "visible" | "hidden") {
	Object.defineProperty(document, "visibilityState", {
		value: state,
		configurable: true,
	});
}

afterEach(() => {
	Reflect.deleteProperty(document, "visibilityState");
});

describe("startRequestPoller", () => {
	beforeEach(() => {
		jest.useFakeTimers();
	});

	interface Call {
		from: string;
		cursor: string | undefined;
		signal: AbortSignal;
	}

	function start(
		replies: (LogHistoryPage | Error)[] = [],
		state?: RequestPollState,
	) {
		const calls: Call[] = [];
		const got: LogHistoryEntry[][] = [];
		const errors: unknown[] = [];
		const queue = [...replies];
		const stop = startRequestPoller({
			fetchPage: async (args) => {
				calls.push(args);
				const next = queue.shift() ?? pageOf([]);
				if (next instanceof Error) throw next;
				return next;
			},
			onEntries: (entries) => got.push(entries),
			onError: (error) => errors.push(error),
			now: () => Date.parse(stamp(30)),
			state,
		});
		return { calls, got, errors, stop };
	}

	const tick = async (ms = REQUEST_POLL_MS) => {
		jest.advanceTimersByTime(ms);
		await flush();
	};

	test("asks at once for the last minute, then from just before the newest line seen", async () => {
		const { calls, got, stop } = start([
			pageOf([request(1), request(2)]),
			pageOf([request(3)]),
		]);
		await flush();
		expect(calls).toHaveLength(1);
		expect(calls[0]?.from).toBe("2026-10-20T13:59:30.000Z");
		expect(calls[0]?.cursor).toBeUndefined();
		expect(got).toEqual([[request(1), request(2)]]);

		await tick();
		expect(calls).toHaveLength(2);
		expect(calls[1]?.from).toBe("2026-10-20T13:59:32.000Z");
		expect(got[1]).toEqual([request(3)]);
		stop();
	});

	test("the line at the boundary instant, seen before, is not delivered again", async () => {
		const { got, stop } = start([
			pageOf([request(1), request(2)]),
			pageOf([request(2), request(3)]),
		]);
		await flush();
		await tick();
		expect(got[1]).toEqual([request(3)]);
		stop();
	});

	test("another line at the boundary instant is new, whatever service or text", async () => {
		const { got, stop } = start([
			pageOf([request(2)]),
			pageOf([
				request(2),
				request(2, "GET /other"),
				request(2, "GET /2", "svc-2"),
			]),
		]);
		await flush();
		await tick();
		expect(got[1]).toEqual([
			request(2, "GET /other"),
			request(2, "GET /2", "svc-2"),
		]);
		stop();
	});

	test("an empty answer still reports its page and keeps the same from", async () => {
		const { calls, got, stop } = start([pageOf([request(5)]), pageOf([])]);
		await flush();
		await tick();
		await tick();
		expect(got).toEqual([[request(5)], [], []]);
		expect(calls.map((call) => call.from)).toEqual([
			"2026-10-20T13:59:30.000Z",
			"2026-10-20T13:59:35.000Z",
			"2026-10-20T13:59:35.000Z",
		]);
		stop();
	});

	test("a slow request that reaches the store after later ones is delivered", async () => {
		const { got, stop } = start([
			pageOf([request(10, "GET /fast")]),
			pageOf([request(8, "GET /slow"), request(10, "GET /fast")]),
		]);
		await flush();
		await tick();
		expect(got[1]).toEqual([request(8, "GET /slow")]);
		stop();
	});

	test("lines seen are not delivered again for as long as the overlap returns them", async () => {
		const { calls, got, stop } = start([
			pageOf([request(1)]),
			pageOf([request(1), request(20)]),
			pageOf([request(1), request(20), request(21)]),
			pageOf([request(20), request(21), request(55)]),
		]);
		await flush();
		await tick();
		await tick();
		await tick();
		expect(got).toEqual([
			[request(1)],
			[request(20)],
			[request(21)],
			[request(55)],
		]);
		expect(calls[3]?.from).toBe("2026-10-20T13:59:51.000Z");
		stop();
	});

	test("a poller started with the state of an earlier one goes on from its newest line", async () => {
		const state = newRequestPollState();
		const first = start([pageOf([request(40)])], state);
		await flush();
		first.stop();
		const second = start([pageOf([request(40), request(41)])], state);
		await flush();
		expect(second.calls[0]?.from).toBe(stamp(10));
		expect(second.got).toEqual([[request(41)]]);
		second.stop();
	});

	test("a page that says there is more is followed in the same turn", async () => {
		const { calls, got, stop } = start([
			pageOf([request(1)], { nextCursor: "c1" }),
			pageOf([request(2)]),
		]);
		await flush();
		expect(calls).toHaveLength(2);
		expect(calls[1]?.cursor).toBe("c1");
		expect(calls[1]?.from).toBe(calls[0]?.from);
		expect(got).toEqual([[request(1)], [request(2)]]);
		stop();
	});

	test("does not ask while the tab is hidden, and asks again when it shows", async () => {
		setVisibility("hidden");
		const { calls, stop } = start();
		await flush();
		await tick();
		await tick();
		expect(calls).toHaveLength(0);

		setVisibility("visible");
		document.dispatchEvent(new Event("visibilitychange"));
		await flush();
		expect(calls).toHaveLength(1);
		await tick();
		expect(calls).toHaveLength(2);
		stop();
	});

	test("never has two requests in flight", async () => {
		const gate = Promise.withResolvers<LogHistoryPage>();
		let calls = 0;
		const stop = startRequestPoller({
			fetchPage: () => {
				calls += 1;
				return gate.promise;
			},
			onEntries: () => {},
			onError: () => {},
		});
		await flush();
		await tick();
		await tick();
		expect(calls).toBe(1);
		gate.resolve(pageOf([]));
		await flush();
		await tick();
		expect(calls).toBe(2);
		stop();
	});

	test("stopping ends the polling and abandons the request in flight", async () => {
		const { calls, got, stop } = start([pageOf([request(1)])]);
		await flush();
		expect(calls).toHaveLength(1);
		stop();
		expect(calls[0]?.signal.aborted).toBe(true);
		await tick(REQUEST_POLL_MS * 5);
		expect(calls).toHaveLength(1);
		expect(got).toHaveLength(1);
		document.dispatchEvent(new Event("visibilitychange"));
		await flush();
		expect(calls).toHaveLength(1);
	});

	test("an answer that arrives after stopping is dropped", async () => {
		const gate = Promise.withResolvers<LogHistoryPage>();
		const got: LogHistoryEntry[][] = [];
		const stop = startRequestPoller({
			fetchPage: () => gate.promise,
			onEntries: (entries) => got.push(entries),
			onError: () => {},
		});
		await flush();
		stop();
		gate.resolve(pageOf([request(1)]));
		await flush();
		expect(got).toEqual([]);
	});

	test("a failed request is reported and the next turn tries again", async () => {
		const { calls, got, errors, stop } = start([
			new Error("offline"),
			pageOf([request(1)]),
		]);
		await flush();
		expect(errors).toHaveLength(1);
		expect((errors[0] as Error).message).toBe("offline");
		await tick();
		expect(calls).toHaveLength(2);
		expect(calls[1]?.from).toBe(calls[0]?.from);
		expect(got).toEqual([[request(1)]]);
		stop();
	});
});

// --- LiveMerger -------------------------------------------------------------------

const line = (id: number, second: number, text = `l${id}`): LogLine => ({
	id,
	ts: stamp(second),
	line: text,
});
const texts = (lines: readonly LogLine[]) => lines.map((l) => l.line);
const ascending = (lines: readonly LogLine[]) =>
	lines.every((l, i) => i === 0 || l.id > (lines[i - 1] as LogLine).id);

describe("LiveMerger", () => {
	test("merges by time, runtime before request at the same instant", () => {
		const merger = new LiveMerger();
		const lines = merger.merge(
			"k",
			[line(1, 1, "r1"), line(2, 3, "r3"), line(3, 5, "r5")],
			[line(1, 3, "q3"), line(2, 4, "q4")],
		);
		expect(texts(lines)).toEqual(["r1", "r3", "q3", "q4", "r5"]);
		expect(ascending(lines)).toBe(true);
	});

	test("a line with no time stays behind the line before it", () => {
		const merger = new LiveMerger();
		const bare: LogLine = { id: 2, line: "no time" };
		const lines = merger.merge(
			"k",
			[line(1, 1, "r1"), bare],
			[line(1, 2, "q2")],
		);
		expect(texts(lines)).toEqual(["r1", "no time", "q2"]);
	});

	test("lines added at the end keep every id and the epoch", () => {
		const merger = new LiveMerger();
		const runtime = [line(1, 1), line(2, 2)];
		const first = merger.merge("k", runtime, []);
		const epoch = merger.epoch;
		const second = merger.merge(
			"k",
			[...runtime, line(3, 3)],
			[line(1, 4, "q")],
		);
		expect(merger.epoch).toBe(epoch);
		expect(second.slice(0, 2)).toEqual([...first]);
		expect(second[0]).toBe(first[0] as LogLine);
		expect(texts(second)).toEqual(["l1", "l2", "l3", "q"]);
		expect(ascending(second)).toBe(true);
	});

	test("merging the same lines again changes nothing", () => {
		const merger = new LiveMerger();
		const runtime = [line(1, 1), line(2, 3)];
		const requests = [line(1, 2, "q")];
		const first = merger.merge("k", runtime, requests);
		const epoch = merger.epoch;
		const again = merger.merge("k", runtime, requests);
		expect(merger.epoch).toBe(epoch);
		expect(again).toEqual(first);
		expect(again.map((l) => l.id)).toEqual(first.map((l) => l.id));
	});

	test("a request line that lands between runtime lines keeps the other ids and changes reorder, not the epoch", () => {
		const merger = new LiveMerger();
		const runtime = [line(1, 1), line(2, 3)];
		const first = merger.merge("k", runtime, []);
		const { epoch, reorder } = merger;
		const second = merger.merge("k", runtime, [line(1, 2, "q")]);
		expect(texts(second)).toEqual(["l1", "q", "l2"]);
		expect(second[0]?.id).toBe(first[0]?.id as number);
		expect(second[2]?.id).toBe(first[1]?.id as number);
		expect(second[1]?.id).toBeGreaterThan(second[0]?.id as number);
		expect(second[1]?.id).toBeLessThan(second[2]?.id as number);
		expect(merger.epoch).toBe(epoch);
		expect(merger.reorder).toBeGreaterThan(reorder);
	});

	test("several lines landing in the same gap are spread inside it", () => {
		const merger = new LiveMerger();
		const runtime = [line(1, 1), line(2, 9)];
		merger.merge("k", runtime, []);
		const lines = merger.merge("k", runtime, [
			line(1, 2, "a"),
			line(2, 3, "b"),
			line(3, 4, "c"),
		]);
		expect(texts(lines)).toEqual(["l1", "a", "b", "c", "l2"]);
		expect(ascending(lines)).toBe(true);
	});

	test("lines dropped from the front keep the epoch", () => {
		const merger = new LiveMerger();
		const all = Array.from({ length: 5 }, (_, i) => line(i + 1, i + 1));
		merger.merge("k", all, []);
		const epoch = merger.epoch;
		const lines = merger.merge("k", all.slice(2), []);
		expect(texts(lines)).toEqual(["l3", "l4", "l5"]);
		expect(merger.epoch).toBe(epoch);
	});

	test("past the buffer the oldest lines go and the epoch holds", () => {
		const merger = new LiveMerger();
		const runtime = Array.from({ length: LIVE_BUFFER }, (_, i) =>
			line(i + 1, 0, `r${i}`),
		);
		merger.merge("k", runtime, []);
		const epoch = merger.epoch;
		const more = [...runtime.slice(1), line(LIVE_BUFFER + 1, 0, "new")];
		const lines = merger.merge("k", more, [line(1, 59, "q")]);
		expect(lines).toHaveLength(LIVE_BUFFER);
		expect(lines.at(-1)?.line).toBe("q");
		expect(lines[0]?.line).toBe("r2");
		expect(merger.epoch).toBe(epoch);
	});

	test("a cleared stream, with request lines left, is a reorder, not a new epoch", () => {
		const merger = new LiveMerger();
		const requests = [line(1, 2, "q")];
		merger.merge("k", [line(1, 1), line(2, 3)], requests);
		const { epoch, reorder } = merger;
		const lines = merger.merge("k", [], requests);
		expect(texts(lines)).toEqual(["q"]);
		expect(merger.epoch).toBe(epoch);
		expect(merger.reorder).toBeGreaterThan(reorder);
	});

	test("every line cleared is a new epoch", () => {
		const merger = new LiveMerger();
		merger.merge("k", [line(1, 1), line(2, 3)], [line(1, 2, "q")]);
		const epoch = merger.epoch;
		expect(merger.merge("k", [], [])).toEqual([]);
		expect(merger.epoch).toBeGreaterThan(epoch);
	});

	test("counts the lines it numbers, wherever they land", () => {
		const merger = new LiveMerger();
		const runtime = [line(1, 1), line(2, 3)];
		const late = line(1, 2, "q");
		merger.merge("k", runtime, []);
		expect(merger.arrived).toBe(2);
		merger.merge("k", runtime, [late]);
		expect(merger.arrived).toBe(3);
		merger.merge("k", runtime, [late]);
		expect(merger.arrived).toBe(3);
	});

	test("a restarted stream, whose lines are new objects, is a new epoch", () => {
		const merger = new LiveMerger();
		merger.merge("k", [line(1, 1), line(2, 2)], []);
		const epoch = merger.epoch;
		const lines = merger.merge("k", [line(1, 1), line(2, 2), line(3, 3)], []);
		expect(texts(lines)).toEqual(["l1", "l2", "l3"]);
		expect(merger.epoch).toBeGreaterThan(epoch);
		expect(ascending(lines)).toBe(true);
	});

	test("another key starts over", () => {
		const merger = new LiveMerger();
		const runtime = [line(1, 1)];
		merger.merge("a", runtime, []);
		const epoch = merger.epoch;
		merger.merge("b", runtime, []);
		expect(merger.epoch).toBeGreaterThan(epoch);
	});
});

describe("insertByTime", () => {
	test("lines at or after the last one are appended", () => {
		const old = [line(1, 1, "a"), line(2, 5, "b")];
		expect(
			texts(insertByTime(old, [line(3, 5, "c"), line(4, 6, "d")])),
		).toEqual(["a", "b", "c", "d"]);
	});

	test("a line that arrives late lands among the older ones, equal times keep their order", () => {
		const old = [line(1, 1, "a"), line(2, 5, "b"), line(3, 9, "c")];
		const merged = insertByTime(old, [line(4, 5, "same"), line(5, 3, "late")]);
		expect(texts(merged)).toEqual(["a", "late", "b", "same", "c"]);
	});

	test("nothing added is the same list", () => {
		const old = [line(1, 1)];
		expect(insertByTime(old, [])).toBe(old);
	});
});

// --- useExplorerLogs ---------------------------------------------------------------

/** An open server-sent stream the test writes to by hand. */
interface OpenStream {
	url: URL;
	send(data: unknown): void;
	end(): void;
}

let streams: OpenStream[] = [];
afterEach(() => {
	for (const stream of streams) {
		try {
			stream.end();
		} catch {
			// already closed
		}
	}
	streams = [];
});

function sseReply(request: Request): Response {
	const encoder = new TextEncoder();
	let controller!: ReadableStreamDefaultController<Uint8Array>;
	const body = new ReadableStream<Uint8Array>({
		start: (c) => {
			controller = c;
		},
	});
	streams.push({
		url: new URL(request.url),
		send: (data) =>
			controller.enqueue(encoder.encode(`data: ${JSON.stringify(data)}\n\n`)),
		end: () => controller.close(),
	});
	return new Response(body, {
		headers: { "Content-Type": "text/event-stream" },
	});
}

const frame = (second: number, text: string) => ({
	line: text,
	ts: stamp(second),
	stream: "stdout",
	serviceId: "svc-1",
	serviceName: "api",
});
const lastStream = () => streams[streams.length - 1] as OpenStream;
const push = (data: unknown) => act(async () => lastStream().send(data));

const stored = (second: number, text: string): LogHistoryEntry => ({
	line: text,
	replica: 0,
	serviceId: "svc-1",
	stream: "stdout",
	ts: stamp(second),
});

function Probe({
	sources: initial = ["runtime", "requests"],
	mode: initialMode = "live",
	range = { kind: "preset", id: "1h" },
	query = "",
}: {
	sources?: readonly LogSource[];
	mode?: "live" | "history";
	range?: RangeSelection;
	query?: string;
}) {
	const [sources, setSources] = useState(initial);
	const [mode, setMode] = useState(initialMode);
	const logs = useExplorerLogs({
		environmentId: "env-1",
		sources,
		mode,
		range,
		query,
	});
	return (
		<div>
			<output aria-label="lines">
				{logs.lines.map((l) => `${l.stream}:${l.line}`).join("|")}
			</output>
			<output aria-label="mode">{logs.mode}</output>
			<output aria-label="loading">{String(logs.loading)}</output>
			<output aria-label="error">
				{logs.error instanceof Error ? logs.error.message : ""}
			</output>
			<output aria-label="partial">{String(logs.partial)}</output>
			<output aria-label="hasOlder">{String(logs.hasOlder)}</output>
			<output aria-label="epoch">{logs.epoch}</output>
			<output aria-label="unavailable">
				{logs.unavailable.map((u) => u.serverId).join("|")}
			</output>
			<button type="button" onClick={logs.fetchOlder}>
				older
			</button>
			<button type="button" onClick={() => setSources(["runtime"])}>
				runtime only
			</button>
			<button type="button" onClick={() => setMode("history")}>
				history
			</button>
			<button type="button" onClick={() => setMode("live")}>
				live
			</button>
			<output aria-label="status">{logs.status}</output>
			<output aria-label="paused">{String(logs.paused)}</output>
			<button type="button" onClick={() => logs.setPaused(true)}>
				pause
			</button>
			<button type="button" onClick={() => logs.setPaused(false)}>
				resume
			</button>
			<button type="button" onClick={logs.clear}>
				clear
			</button>
			<button type="button" onClick={logs.refresh}>
				refresh
			</button>
			<button type="button" onClick={() => logs.setAtBottom(false)}>
				away
			</button>
			<button type="button" onClick={() => logs.setAtBottom(true)}>
				bottom
			</button>
			<output aria-label="reorder">{logs.reorder}</output>
			<output aria-label="newCount">{logs.newCount}</output>
			<output aria-label="capturing">
				{String(logs.capturing ?? "unknown")}
			</output>
		</div>
	);
}

const pause = (ms: number) => {
	const { promise, resolve } = Promise.withResolvers<void>();
	setTimeout(resolve, ms);
	return promise;
};
const out = (name: string) => screen.getByLabelText(name).textContent ?? "";
const shown = () => {
	const text = out("lines");
	return text === "" ? [] : text.split("|");
};

describe("useExplorerLogs live", () => {
	test("merges the runtime stream with the polled request lines by time", async () => {
		const api = stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": {
				body: pageOf([request(2, "GET /a"), request(4, "GET /b")]),
			},
		});
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame(1, "booted"));
		await push(frame(3, "listening"));
		await push(frame(5, "done"));
		await waitFor(() =>
			expect(shown()).toEqual([
				"stdout:booted",
				"requests:GET /a",
				"stdout:listening",
				"requests:GET /b",
				"stdout:done",
			]),
		);
		const poll = api.calls.find((call) => call.query.get("order") === "asc");
		expect(poll?.path).toBe("/environments/env-1/logs/history");
		expect(poll?.query.get("sources")).toBe("requests");
		expect(poll?.query.get("order")).toBe("asc");
		expect(
			Date.now() - Date.parse(poll?.query.get("from") ?? ""),
		).toBeGreaterThanOrEqual(59_000);
		expect(out("mode")).toBe("live");
		expect(out("loading")).toBe("false");
	});

	test("the runtime alone opens the stream and never polls the requests", async () => {
		const api = stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": { body: pageOf([]) },
		});
		await renderWithProviders(<Probe sources={["runtime"]} />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame(1, "booted"));
		await waitFor(() => expect(shown()).toEqual(["stdout:booted"]));
		expect(api.calls.some((call) => call.query.has("order"))).toBe(false);
	});

	test("requests alone poll and open no stream", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/history": {
				body: pageOf([request(2, "GET /a")]),
			},
		});
		await renderWithProviders(<Probe sources={["requests"]} />);
		await waitFor(() => expect(shown()).toEqual(["requests:GET /a"]));
		expect(streams).toHaveLength(0);
		expect(api.calls[0]?.query.get("sources")).toBe("requests");
	});

	test("is loading until the first poll answers", async () => {
		const gate = Promise.withResolvers<void>();
		stubApi({
			"GET /environments/:id/logs/history": async () => {
				await gate.promise;
				return Response.json(pageOf([request(2, "GET /a")]));
			},
		});
		await renderWithProviders(<Probe sources={["requests"]} />);
		expect(out("loading")).toBe("true");
		gate.resolve();
		await waitFor(() => expect(out("loading")).toBe("false"));
		expect(shown()).toEqual(["requests:GET /a"]);
	});

	test("a failing poll is the error, a server that could not answer is listed", async () => {
		stubApi({
			"GET /environments/:id/logs/history": () =>
				Response.json(
					{ status: 500, title: "Internal Server Error", detail: "no luck" },
					{ status: 500 },
				),
		});
		await renderWithProviders(<Probe sources={["requests"]} />);
		await waitFor(() => expect(out("error")).toContain("no luck"));
		expect(out("loading")).toBe("false");
	});

	test("lists the servers whose request log could not be read", async () => {
		stubApi({
			"GET /environments/:id/logs/history": {
				body: pageOf([], {
					unavailable: [
						{
							message: "offline",
							reason: "server_offline",
							serverId: "srv-9",
							serverName: "hetzner-09",
							serviceId: "svc-1",
							serviceName: "api",
						},
					],
				}),
			},
		});
		await renderWithProviders(<Probe sources={["requests"]} />);
		await waitFor(() => expect(out("unavailable")).toBe("srv-9"));
	});

	test("has no older page and no partial result", async () => {
		stubApi({
			"GET /environments/:id/logs/history": { body: pageOf([]) },
		});
		await renderWithProviders(<Probe sources={["requests"]} />);
		await waitFor(() => expect(out("loading")).toBe("false"));
		expect(out("hasOlder")).toBe("false");
		expect(out("partial")).toBe("false");
	});

	test("switching to the runtime alone drops the request lines and stops polling", async () => {
		const api = stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": {
				body: pageOf([request(2, "GET /a")]),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame(1, "booted"));
		await waitFor(() => expect(shown()).toHaveLength(2));
		const epoch = out("epoch");
		const polls = api.calls.filter((c) => c.path.endsWith("/logs/history"));

		await user.click(screen.getByRole("button", { name: "runtime only" }));
		await waitFor(() => expect(shown()).toEqual(["stdout:booted"]));
		expect(out("epoch")).not.toBe(epoch);
		expect(
			api.calls.filter((c) => c.path.endsWith("/logs/history")),
		).toHaveLength(polls.length);
	});
});

describe("useExplorerLogs pause, clear and status", () => {
	const click = (name: string) =>
		act(async () => screen.getByRole("button", { name }).click());

	test("the status follows the runtime stream, and paused closes it while the lines stay", async () => {
		stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": { body: pageOf([]) },
		});
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(streams).toHaveLength(1));
		expect(out("status")).toBe("connecting");
		await push(frame(1, "booted"));
		await waitFor(() => expect(out("status")).toBe("live"));
		await waitFor(() => expect(shown()).toEqual(["stdout:booted"]));
		const epoch = out("epoch");
		await click("pause");
		expect(out("paused")).toBe("true");
		expect(shown()).toEqual(["stdout:booted"]);
		expect(out("epoch")).toBe(epoch);
		expect(out("loading")).toBe("false");
		expect(streams).toHaveLength(1);
		await click("resume");
		expect(out("paused")).toBe("false");
		await waitFor(() => expect(streams).toHaveLength(2));
		// A new stream starts afresh: the list is replaced.
		await waitFor(() => expect(shown()).toEqual([]));
		expect(out("epoch")).not.toBe(epoch);
	});

	test("paused keeps the request lines and does not wait on the closed poll; resuming polls again", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/history": {
				body: pageOf([request(2, "GET /a")]),
			},
		});
		await renderWithProviders(<Probe sources={["requests"]} />);
		await waitFor(() => expect(shown()).toEqual(["requests:GET /a"]));
		await click("pause");
		expect(shown()).toEqual(["requests:GET /a"]);
		expect(out("loading")).toBe("false");
		const asked = api.calls.length;
		await click("resume");
		await waitFor(() => expect(api.calls.length).toBeGreaterThan(asked));
	});

	test("with the runtime not searched the status is the request poll's", async () => {
		stubApi({
			"GET /environments/:id/logs/history": { body: pageOf([]) },
		});
		await renderWithProviders(<Probe sources={["requests"]} />);
		await waitFor(() => expect(out("status")).toBe("live"));
	});

	test("clear empties the lines of both sources and they stay gone", async () => {
		stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": {
				body: pageOf([request(2, "GET /a")]),
			},
		});
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame(1, "booted"));
		await waitFor(() => expect(shown()).toHaveLength(2));
		await click("clear");
		await waitFor(() => expect(shown()).toEqual([]));
		await push(frame(3, "later"));
		await waitFor(() => expect(shown()).toEqual(["stdout:later"]));
	});

	test("clear while paused empties the frozen lines", async () => {
		stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": { body: pageOf([]) },
		});
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame(1, "booted"));
		await waitFor(() => expect(shown()).toEqual(["stdout:booted"]));
		await click("pause");
		await click("clear");
		expect(shown()).toEqual([]);
		expect(out("paused")).toBe("true");
	});

	test("going to the history ends a pause", async () => {
		stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": { body: pageOf([]) },
		});
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await click("pause");
		await click("history");
		await waitFor(() => expect(out("paused")).toBe("false"));
		expect(out("status")).toBe("ended");
	});
});

describe("useExplorerLogs clear, refresh, capture and new lines", () => {
	const click = (name: string) =>
		act(async () => screen.getByRole("button", { name }).click());

	test("cleared lines stay gone after Pause and Resume, when the stream replays them", async () => {
		stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": { body: pageOf([]) },
		});
		await renderWithProviders(<Probe sources={["runtime"]} />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame(1, "old one"));
		await push(frame(3, "old two"));
		await waitFor(() => expect(shown()).toHaveLength(2));
		await click("clear");
		await waitFor(() => expect(shown()).toEqual([]));
		await click("pause");
		await click("resume");
		await waitFor(() => expect(streams).toHaveLength(2));
		await push(frame(1, "old one"));
		await push(frame(3, "old two"));
		await push(frame(5, "new"));
		await waitFor(() => expect(shown()).toEqual(["stdout:new"]));
	});

	test("a clear while paused holds after Resume too", async () => {
		stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": { body: pageOf([]) },
		});
		await renderWithProviders(<Probe sources={["runtime"]} />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame(1, "old"));
		await waitFor(() => expect(shown()).toEqual(["stdout:old"]));
		await click("pause");
		await click("clear");
		await click("resume");
		await waitFor(() => expect(streams).toHaveLength(2));
		await push(frame(1, "old"));
		await push(frame(4, "new"));
		await waitFor(() => expect(shown()).toEqual(["stdout:new"]));
	});

	test("resuming asks for request lines from before the newest one seen, not from a minute ago", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/history": {
				body: pageOf([request(2, "GET /a")]),
			},
		});
		await renderWithProviders(<Probe sources={["requests"]} />);
		await waitFor(() => expect(shown()).toEqual(["requests:GET /a"]));
		await click("pause");
		await click("resume");
		await waitFor(() =>
			expect(api.calls.filter((c) => c.query.has("order"))).toHaveLength(2),
		);
		const resumed = api.calls.filter((c) => c.query.has("order"))[1];
		expect(resumed?.query.get("from")).toBe("2026-10-20T13:59:32.000Z");
		expect(shown()).toEqual(["requests:GET /a"]);
	});

	test("a request line that reaches the store late is placed in time and does not replace the list", async () => {
		let polls = 0;
		stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": (req) => {
				if (new URL(req.url).searchParams.get("order") !== "asc") {
					return Response.json(pageOf([]));
				}
				polls += 1;
				return Response.json(
					polls === 1
						? pageOf([request(4, "GET /fast")])
						: pageOf([request(2, "GET /slow"), request(4, "GET /fast")]),
				);
			},
		});
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame(1, "booted"));
		await push(frame(3, "listening"));
		await push(frame(5, "done"));
		await waitFor(() => expect(shown()).toHaveLength(4));
		const epoch = out("epoch");
		const reorder = out("reorder");
		await click("refresh");
		await waitFor(() =>
			expect(shown()).toEqual([
				"stdout:booted",
				"requests:GET /slow",
				"stdout:listening",
				"requests:GET /fast",
				"stdout:done",
			]),
		);
		expect(out("epoch")).toBe(epoch);
		expect(out("reorder")).not.toBe(reorder);
	});

	test("refresh in the history asks again from a new now, also after a failure", async () => {
		let calls = 0;
		const api = stubApi({
			"GET /environments/:id/logs/history": () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{ status: 500, title: "Internal Server Error", detail: "boom" },
							{ status: 500 },
						)
					: Response.json(pageOf([stored(1, "fresh")]));
			},
		});
		await renderWithProviders(<Probe mode="history" sources={["runtime"]} />);
		await waitFor(() => expect(out("error")).toContain("boom"));
		await pause(10);
		await click("refresh");
		await waitFor(() => expect(shown()).toEqual(["stdout:fresh"]));
		const [first, second] = api.calls.map((c) =>
			Date.parse(c.query.get("to") ?? ""),
		);
		expect(second).toBeGreaterThan(first as number);
	});

	test("a history that was waiting behind the live view asks up to the moment it opens", async () => {
		const api = stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": {
				body: pageOf([stored(1, "stored")]),
			},
		});
		const mounted = Date.now();
		await renderWithProviders(<Probe sources={["runtime"]} />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await pause(30);
		await click("history");
		await waitFor(() => expect(shown()).toEqual(["stdout:stored"]));
		const read = api.calls.find((c) => c.query.get("limit") === "500");
		expect(Date.parse(read?.query.get("to") ?? "")).toBeGreaterThanOrEqual(
			mounted + 30,
		);
	});

	test("capturing in the live view is the runtime's own answer, not the requests'", async () => {
		const api = stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": (req) => {
				const search = new URL(req.url).searchParams;
				return Response.json(
					pageOf([], { capturing: search.get("order") === "asc" }),
				);
			},
		});
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(out("capturing")).toBe("false"));
		const probe = api.calls.find(
			(c) => c.path.endsWith("/logs/history") && !c.query.has("order"),
		);
		expect(probe?.query.get("limit")).toBe("1");
		expect(probe?.query.has("sources")).toBe(false);
	});

	test("capturing is unknown while the runtime is not a source", async () => {
		stubApi({
			"GET /environments/:id/logs/history": { body: pageOf([]) },
		});
		await renderWithProviders(<Probe sources={["requests"]} />);
		await waitFor(() => expect(out("status")).toBe("live"));
		expect(out("capturing")).toBe("unknown");
	});

	test("a history of the runtime alone reports its page's capturing without another request", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/history": {
				body: pageOf([], { capturing: false }),
			},
		});
		await renderWithProviders(<Probe mode="history" sources={["runtime"]} />);
		await waitFor(() => expect(out("capturing")).toBe("false"));
		expect(api.calls).toHaveLength(1);
	});

	test("new lines are counted from the moment the reader leaves the end, and start over at the end", async () => {
		stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": { body: pageOf([]) },
		});
		await renderWithProviders(<Probe sources={["runtime"]} />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame(1, "a"));
		await waitFor(() => expect(shown()).toHaveLength(1));
		expect(out("newCount")).toBe("0");
		await click("away");
		await push(frame(2, "b"));
		await push(frame(3, "c"));
		await waitFor(() => expect(out("newCount")).toBe("2"));
		await click("bottom");
		expect(out("newCount")).toBe("0");
		await push(frame(4, "d"));
		await waitFor(() => expect(shown()).toHaveLength(4));
		expect(out("newCount")).toBe("0");
	});
});

describe("useExplorerLogs history", () => {
	test("reads the stored lines of the range with the query and the sources, and opens no stream or poll", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/history": {
				body: pageOf([
					{ ...request(2, "GET /a"), stream: "requests" },
					stored(1, "booted"),
				]),
			},
		});
		await renderWithProviders(<Probe mode="history" query="level:error" />);
		await waitFor(() =>
			expect(shown()).toEqual(["stdout:booted", "requests:GET /a"]),
		);
		expect(out("mode")).toBe("history");
		const reads = api.calls.filter((c) => c.query.get("limit") === "500");
		expect(reads).toHaveLength(1);
		expect(api.calls.some((c) => c.query.has("order"))).toBe(false);
		const query = reads[0]?.query;
		expect(query?.get("sources")).toBe("runtime,requests");
		expect(query?.get("q")).toBe("level:error");
		expect(query?.has("order")).toBe(false);
		expect(streams).toHaveLength(0);
	});

	test("offers older lines and loads them on request", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/history": (request) => {
				const cursor = new URL(request.url).searchParams.get("cursor");
				return Response.json(
					cursor === null
						? pageOf([stored(4, "d"), stored(3, "c")], { nextCursor: "c1" })
						: pageOf([stored(2, "b"), stored(1, "a")]),
				);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Probe mode="history" sources={["runtime"]} />);
		await waitFor(() => expect(shown()).toEqual(["stdout:c", "stdout:d"]));
		expect(out("hasOlder")).toBe("true");
		await user.click(screen.getByRole("button", { name: "older" }));
		await waitFor(() =>
			expect(shown()).toEqual(["stdout:a", "stdout:b", "stdout:c", "stdout:d"]),
		);
		expect(out("hasOlder")).toBe("false");
		expect(api.calls.map((c) => c.query.get("cursor"))).toEqual([null, "c1"]);
		expect(api.calls[0]?.query.has("sources")).toBe(false);
	});

	test("a partial scan is reported and can be continued", async () => {
		stubApi({
			"GET /environments/:id/logs/history": {
				body: pageOf([], { partial: true, nextCursor: "c9" }),
			},
		});
		await renderWithProviders(<Probe mode="history" />);
		await waitFor(() => expect(out("partial")).toBe("true"));
		expect(out("hasOlder")).toBe("true");
	});

	test("reports the services that could not be read", async () => {
		stubApi({
			"GET /environments/:id/logs/history": {
				body: pageOf([stored(1, "a")], {
					unavailable: [
						{
							message: "offline",
							reason: "server_offline",
							serverId: "srv-1",
							serverName: "hetzner-01",
							serviceId: "svc-2",
							serviceName: "worker",
						},
					],
				}),
			},
		});
		await renderWithProviders(<Probe mode="history" />);
		await waitFor(() => expect(out("unavailable")).toBe("srv-1"));
	});

	test("switching from live to history stops the stream and the polling and changes the epoch", async () => {
		const api = stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": (req) => {
				const search = new URL(req.url).searchParams;
				return Response.json(
					search.get("order") === "asc"
						? pageOf([request(2)])
						: pageOf([stored(1, "stored")]),
				);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(streams).toHaveLength(1));
		await waitFor(() => expect(shown()).toEqual(["requests:GET /2"]));
		const epoch = out("epoch");

		await user.click(screen.getByRole("button", { name: "history" }));
		await waitFor(() => expect(shown()).toEqual(["stdout:stored"]));
		expect(out("epoch")).not.toBe(epoch);
		const polls = api.calls.filter((c) => c.query.get("order") === "asc");
		const { promise, resolve } = Promise.withResolvers<void>();
		setTimeout(resolve, 30);
		await promise;
		expect(
			api.calls.filter((c) => c.query.get("order") === "asc"),
		).toHaveLength(polls.length);
	});
});
