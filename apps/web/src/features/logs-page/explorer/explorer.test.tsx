import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Environment, Service } from "#/api/models";
import { stubLayout } from "#/features/logs/test-layout";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { LogsExplorer } from "./explorer";

let restoreLayout: () => void;
const openStreams: Array<() => void> = [];
afterEach(() => {
	senders.length = 0;
	restoreLayout();
	for (const close of openStreams.splice(0)) {
		try {
			close();
		} catch {
			// already closed
		}
	}
});

/** Wide enough to dock the facet panel (it docks from 900px). */
beforeEach(() => {
	restoreLayout = stubLayout(1200, 400);
});

/** Writes a frame to the newest open live stream. */
const senders: Array<(data: unknown) => void> = [];
const push = (data: unknown) =>
	act(async () =>
		(senders[senders.length - 1] as (data: unknown) => void)(data),
	);

/** A live runtime stream that stays open until the test writes to it. */
function silentStream(): Response {
	let controller!: ReadableStreamDefaultController<Uint8Array>;
	const body = new ReadableStream<Uint8Array>({
		start: (c) => {
			controller = c;
		},
	});
	const encoder = new TextEncoder();
	senders.push((data) =>
		controller.enqueue(encoder.encode(`data: ${JSON.stringify(data)}\n\n`)),
	);
	openStreams.push(() => controller.close());
	return new Response(body, {
		headers: { "Content-Type": "text/event-stream" },
	});
}

const environment = { id: "env1" } as unknown as Environment;

const service = (
	id: string,
	name: string,
	server?: { id: string; name: string },
) =>
	({
		id,
		name,
		currentDeploymentId: `deploy-${id}`,
		desiredState: "running",
		kind: "app",
		sourceType: "image",
		config: {},
		server: server ? { ...server, status: "online", lastSeenAt: null } : null,
	}) as unknown as Service;

const services = [
	service("s1", "api", { id: "srv1", name: "alpha" }),
	service("s2", "worker", { id: "srv2", name: "beta" }),
];

const runtimeEntry = {
	line: "server listening on :8080",
	ts: "2026-10-29T10:00:05.000Z",
	replica: 0,
	serviceId: "s1",
	serviceName: "api",
	stream: "runtime",
};

const requestEntry = {
	line: JSON.stringify({
		method: "POST",
		host: "app.example.com",
		path: "/api/orders",
		status: 502,
		duration_ms: 1234,
		process: "web",
		ip: "203.0.113.0/24",
	}),
	ts: "2026-10-29T10:00:20.000Z",
	replica: 0,
	serviceId: "s1",
	serviceName: "api",
	stream: "requests",
};

const page = (items: unknown[], extra: Record<string, unknown> = {}) => ({
	capturing: true,
	items,
	...extra,
});

/** Four 15-second buckets from 10:00:00, 6 lines of which 1 is an error. */
const HISTOGRAM = [
	{ start: "2026-10-29T10:00:00.000Z", count: 1, errors: 0 },
	{ start: "2026-10-29T10:00:15.000Z", count: 2, errors: 1 },
	{ start: "2026-10-29T10:00:30.000Z", count: 0, errors: 0 },
	{ start: "2026-10-29T10:00:45.000Z", count: 3, errors: 0 },
];

const facetsBody = (extra: Record<string, unknown> = {}) => ({
	matched: 1240,
	scanned: 1240,
	partial: false,
	capturing: true,
	requestsCapturing: true,
	facets: [
		{
			key: "level",
			count: 1240,
			values: [
				{ value: "error", count: 12 },
				{ value: "info", count: 1228 },
			],
		},
		{
			key: "service",
			count: 1240,
			values: [
				{ value: "api", count: 1000 },
				{ value: "worker", count: 240 },
			],
		},
		{
			key: "status",
			count: 800,
			values: [
				{ value: "200", count: 700 },
				{ value: "204", count: 20 },
				{ value: "404", count: 60 },
				{ value: "500", count: 15 },
				{ value: "502", count: 5 },
			],
		},
	],
	attrs: [
		{
			key: "@kind",
			count: 800,
			values: [
				{ value: "api", count: 600 },
				{ value: "asset", count: 200 },
			],
		},
		{
			key: "@method",
			count: 800,
			values: [
				{ value: "POST", count: 300 },
				{ value: "GET", count: 500 },
			],
		},
		{
			key: "@duration_ms",
			count: 800,
			numeric: true,
			min: 1,
			max: 4000,
			values: [],
		},
		{
			key: "@path",
			count: 800,
			values: [{ value: "/api/orders", count: 800 }],
		},
	],
	histogram: HISTOGRAM,
	...extra,
});

function routes(overrides: Record<string, unknown> = {}) {
	return {
		"GET /environments/:id/logs": silentStream,
		"GET /environments/:id/logs/history": {
			body: page([runtimeEntry, requestEntry]),
		},
		"GET /environments/:id/logs/facets": { body: facetsBody() },
		...overrides,
	} as Parameters<typeof stubApi>[0];
}

function Address() {
	const search = useRouterState({ select: (s) => s.location.searchStr });
	return <output aria-label="address">{decodeURIComponent(search)}</output>;
}

const render = (path: string) =>
	renderWithProviders(
		<>
			<LogsExplorer environment={environment} services={services} />
			<Address />
		</>,
		{ path },
	);

const log = () => screen.getByRole("log", { name: "Environment logs" });
const address = () => screen.getByLabelText("address").textContent ?? "";
const searchBox = () =>
	screen.getByRole("combobox", { name: "Search environment logs" });

describe("LogsExplorer", () => {
	test("lays out the toolbar, the histogram, the facets and the log", async () => {
		stubApi(routes());
		await render("/?logs=history");
		expect(searchBox()).toHaveAttribute(
			"placeholder",
			"Search logs: level:error @path:/api/* status:5* …",
		);
		expect(screen.getByRole("button", { name: "Runtime" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "History" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(
			await within(log()).findByText("server listening on :8080"),
		).toBeInTheDocument();
		// The histogram's numbers, for a screen reader.
		expect(
			await screen.findByText("6 lines, 1 error in the last hour"),
		).toBeInTheDocument();
		// The facet counts are the server's (12 errors), not the two lines loaded.
		const level = await screen.findByRole("region", { name: "Level" });
		await waitFor(() =>
			expect(within(level).getByText("12")).toBeInTheDocument(),
		);
		expect(
			within(screen.getByRole("region", { name: "Service" })).getByText(
				"worker",
			),
		).toBeInTheDocument();
		expect(
			await screen.findByText(/1,240 lines match in the last hour/),
		).toBeInTheDocument();
	});

	test("live mode says Last 15 min, has no Follow toggle and sends no range to the facets", async () => {
		const api = stubApi(routes());
		await render("/");
		expect(screen.getByText("Last 15 min")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Follow" })).toBeNull();
		expect(screen.getByRole("button", { name: "Pause" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Clear" })).toBeInTheDocument();
		await screen.findByText("6 lines, 1 error in the last 15 minutes");
		const facets = api.calls.find((c) => c.path.endsWith("/logs/facets"));
		expect(facets?.query.has("from")).toBe(false);
	});

	test("choosing Requests writes ?sources= and asks for requests only", async () => {
		const api = stubApi(routes());
		const user = userEvent.setup();
		await render("/?logs=history");
		await within(log()).findByText("server listening on :8080");
		await user.click(screen.getByRole("button", { name: "Requests" }));
		await waitFor(() => expect(address()).toContain("sources=requests"));
		await waitFor(() =>
			expect(
				api.calls.some(
					(c) =>
						c.path === "/environments/env1/logs/history" &&
						c.query.get("sources") === "requests",
				),
			).toBe(true),
		);
		await user.click(screen.getByRole("button", { name: "All" }));
		await waitFor(() => expect(address()).not.toContain("sources="));
	});

	test("a request line shows its method, status, path and duration; one host is left to the tooltip", async () => {
		stubApi(routes());
		await render("/?logs=history");
		const view = within(log());
		expect(await view.findByText("POST")).toBeInTheDocument();
		expect(view.getByText("502")).toHaveAttribute("data-tone", "bad");
		expect(view.getByText("/api/orders")).toBeInTheDocument();
		expect(view.getByText("1.2 s")).toBeInTheDocument();
		expect(view.getByText("web")).toBeInTheDocument();
		// Every request here is for one host: it is not repeated on the row.
		expect(view.queryByText("app.example.com")).toBeNull();
		expect(view.getByTitle("app.example.com/api/orders")).toBeInTheDocument();
		// A runtime line stays a plain line.
		expect(view.getByText("server listening on :8080")).toBeInTheDocument();
	});

	test("clicking a row opens its detail; Filter adds the term to the query; Escape closes it", async () => {
		stubApi(routes());
		const user = userEvent.setup();
		await render("/?logs=history");
		await user.click(await within(log()).findByText("/api/orders"));
		const detail = await screen.findByRole("complementary", {
			name: "Log line detail",
		});
		expect(
			within(detail).getByRole("button", { name: "Copy raw line" }),
		).toBeInTheDocument();
		expect(within(detail).getByText("@duration_ms")).toBeInTheDocument();
		// A row is marked while its detail is open.
		expect(
			within(log()).getByRole("button", { pressed: true }),
		).toHaveTextContent("/api/orders");

		await user.click(
			within(detail).getByRole("button", { name: "Filter @method:POST" }),
		);
		expect(searchBox()).toHaveValue("@method:POST");
		await user.click(
			within(detail).getByRole("button", {
				name: "Exclude @host:app.example.com",
			}),
		);
		expect(searchBox()).toHaveValue("@method:POST -@host:app.example.com");
		// The built-in status is a facet key, not an attribute.
		expect(
			within(detail).getByRole("button", { name: "Filter status:502" }),
		).toBeInTheDocument();

		await user.keyboard("{Escape}");
		await waitFor(() =>
			expect(
				screen.queryByRole("complementary", { name: "Log line detail" }),
			).toBeNull(),
		);
	});

	test("the keyboard moves between rows and Enter opens one", async () => {
		stubApi(routes());
		const user = userEvent.setup();
		await render("/?logs=history");
		await within(log()).findByText("/api/orders");
		log().focus();
		await user.keyboard("{ArrowDown}");
		const first = within(log()).getAllByRole("button")[0];
		expect(first).toHaveFocus();
		await user.keyboard("{ArrowDown}{Enter}");
		expect(
			await screen.findByRole("complementary", { name: "Log line detail" }),
		).toBeInTheDocument();
		expect(within(log()).getByRole("button", { pressed: true })).toHaveFocus();
	});

	test("dragging across the histogram switches to History with that span", async () => {
		stubApi(routes());
		await render("/");
		await screen.findByText("6 lines, 1 error in the last 15 minutes");
		const chart = screen.getByTestId("volume-chart");
		chart.getBoundingClientRect = () =>
			({
				left: 0,
				top: 0,
				width: 400,
				height: 56,
				right: 400,
				bottom: 56,
			}) as DOMRect;
		fireEvent.pointerDown(chart, { clientX: 10, button: 0, pointerId: 1 });
		fireEvent.pointerMove(chart, { clientX: 200, pointerId: 1 });
		fireEvent.pointerUp(chart, { clientX: 200, pointerId: 1 });
		await waitFor(() => expect(address()).toContain("logs=history"));
		expect(address()).toContain("from=2026-10-29T10:00:00.000Z");
		expect(address()).toContain("to=2026-10-29T10:00:45.000Z");
		expect(address()).not.toContain("range=");
		expect(
			await screen.findByRole("button", { name: "History" }),
		).toHaveAttribute("aria-pressed", "true");
	});

	test("a click without a drag leaves the view alone", async () => {
		stubApi(routes());
		await render("/");
		await screen.findByText("6 lines, 1 error in the last 15 minutes");
		const chart = screen.getByTestId("volume-chart");
		chart.getBoundingClientRect = () =>
			({
				left: 0,
				top: 0,
				width: 400,
				height: 56,
				right: 400,
				bottom: 56,
			}) as DOMRect;
		fireEvent.pointerDown(chart, { clientX: 10, button: 0, pointerId: 1 });
		fireEvent.pointerUp(chart, { clientX: 10, pointerId: 1 });
		expect(address()).not.toContain("logs=history");
	});

	test("suggestions come from the server's counts, numeric attributes keep their operators", async () => {
		stubApi(routes());
		const user = userEvent.setup();
		await render("/?logs=history");
		await screen.findByText(/1,240 lines match/);
		await user.click(searchBox());
		await user.type(searchBox(), "@met");
		expect(
			await screen.findByRole("option", { name: /@method:/ }),
		).toBeInTheDocument();
		await user.clear(searchBox());
		await user.type(searchBox(), "@method:");
		expect(
			await screen.findByRole("option", { name: /GET/ }),
		).toBeInTheDocument();
		expect(screen.getByRole("option", { name: /POST/ })).toBeInTheDocument();
		await user.clear(searchBox());
		await user.type(searchBox(), "@duration_ms:");
		await waitFor(() =>
			expect(
				screen
					.getAllByRole("option")
					.some((option) => /compare/.test(option.textContent ?? "")),
			).toBe(true),
		);
	});

	test("with request logs off it says so and links the servers", async () => {
		stubApi(
			routes({
				"GET /environments/:id/logs/facets": {
					body: facetsBody({ requestsCapturing: false }),
				},
			}),
		);
		await render("/?logs=history");
		expect(
			await screen.findByText(
				"Request logs are off on this environment's servers",
			),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "alpha" }).getAttribute("href"),
		).toBe("/servers/srv1/settings/logs-and-disk");
		expect(
			screen.getByRole("link", { name: "beta" }).getAttribute("href"),
		).toBe("/servers/srv2/settings/logs-and-disk");
	});

	test("the request-logs note is for when requests are searched", async () => {
		const api = stubApi(
			routes({
				"GET /environments/:id/logs/facets": {
					body: facetsBody({ requestsCapturing: false }),
				},
			}),
		);
		await render("/?logs=history&sources=runtime");
		await waitFor(() =>
			expect(api.calls.some((c) => c.path.endsWith("/logs/facets"))).toBe(true),
		);
		await screen.findByText(/1,240 lines match/);
		expect(screen.queryByText(/Request logs are off/)).toBeNull();
	});

	test("a search that stopped early says so and offers older lines", async () => {
		stubApi(
			routes({
				"GET /environments/:id/logs/history": {
					body: page([runtimeEntry], { partial: true, nextCursor: "older" }),
				},
			}),
		);
		await render("/?logs=history");
		expect(
			await screen.findByText("Searched part of the range; scroll up for more"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Load older lines" }),
		).toBeInTheDocument();
	});

	test("an empty history says so", async () => {
		stubApi(
			routes({
				"GET /environments/:id/logs/history": { body: page([]) },
				"GET /environments/:id/logs/facets": {
					body: facetsBody({ matched: 0, histogram: [] }),
				},
			}),
		);
		await render("/?logs=history");
		expect(
			await screen.findByText("No lines in this range."),
		).toBeInTheDocument();
		expect(
			await screen.findByText("0 lines, 0 errors in the last hour"),
		).toBeInTheDocument();
	});
});

describe("LogsExplorer live controls", () => {
	const frame = (line: string, ts = "2026-10-29T10:00:00.000Z") => ({
		line,
		ts,
		stream: "stdout",
		serviceId: "s1",
		serviceName: "api",
	});
	const status = () => screen.getByTestId("stream-status");
	const live = () =>
		stubApi(
			routes({
				"GET /environments/:id/logs/history": { body: page([]) },
			}),
		);

	test("the footer says connecting, then live once the stream answers, with the count and what is shown", async () => {
		live();
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		expect(status()).toHaveTextContent("Connecting…");
		await push({});
		await waitFor(() => expect(status()).toHaveTextContent("Live"));
		await push(frame("hello"));
		await within(log()).findByText("hello");
		expect(
			await screen.findByText(
				/1,240 in the last 15 min · showing the newest 1(?!\d)/,
			),
		).toBeInTheDocument();
	});

	test("an ended stream says so", async () => {
		live();
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		await act(async () => openStreams[0]?.());
		await waitFor(() => expect(status()).toHaveTextContent("Stream ended"));
	});

	test("Pause keeps the lines on screen, says Paused and closes the stream; Resume opens a new one", async () => {
		live();
		const user = userEvent.setup();
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		await push(frame("before pause"));
		await within(log()).findByText("before pause");
		const pause = screen.getByRole("button", { name: "Pause" });
		expect(pause).toHaveAttribute("aria-pressed", "false");
		await user.click(pause);
		const resume = screen.getByRole("button", { name: "Resume" });
		expect(resume).toHaveAttribute("aria-pressed", "true");
		expect(status()).toHaveTextContent("Paused");
		expect(within(log()).getByText("before pause")).toBeInTheDocument();
		expect(senders).toHaveLength(1);
		await user.click(resume);
		await waitFor(() => expect(senders).toHaveLength(2));
		expect(screen.getByRole("button", { name: "Pause" })).toBeInTheDocument();
		expect(status()).not.toHaveTextContent("Paused");
	});

	test("Clear empties the log and lines already seen do not come back", async () => {
		live();
		const user = userEvent.setup();
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		await push(frame("to be cleared"));
		await within(log()).findByText("to be cleared");
		await user.click(screen.getByRole("button", { name: "Clear" }));
		await waitFor(() =>
			expect(within(log()).queryByText("to be cleared")).toBeNull(),
		);
		await push(frame("after clear", "2026-10-29T10:00:05.000Z"));
		expect(await within(log()).findByText("after clear")).toBeInTheDocument();
		expect(within(log()).queryByText("to be cleared")).toBeNull();
	});

	test("Clear while paused empties the frozen lines", async () => {
		live();
		const user = userEvent.setup();
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		await push(frame("frozen line"));
		await within(log()).findByText("frozen line");
		await user.click(screen.getByRole("button", { name: "Pause" }));
		await user.click(screen.getByRole("button", { name: "Clear" }));
		await waitFor(() =>
			expect(within(log()).queryByText("frozen line")).toBeNull(),
		);
		expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument();
	});

	test("History has no Pause, Clear or stream status", async () => {
		stubApi(routes());
		await render("/?logs=history");
		await within(log()).findByText("server listening on :8080");
		expect(screen.queryByRole("button", { name: "Pause" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
		expect(screen.queryByTestId("stream-status")).toBeNull();
	});
});

describe("LogsExplorer autocomplete and the sidebar while typing", () => {
	/**
	 * What the server says per query: the whole range for no query, what the
	 * error lines have for `level:error`, and nothing for the half-typed
	 * clauses (which match no line).
	 */
	const seen: string[] = [];
	const facetsReply = (request: Request) => {
		const q = new URL(request.url).searchParams.get("q") ?? "";
		seen.push(q);
		if (q === "level:error") {
			return Response.json(
				facetsBody({
					matched: 12,
					attrs: [
						{
							key: "@method",
							count: 12,
							values: [{ value: "POST", count: 7 }],
						},
					],
				}),
			);
		}
		if (q === "@" || q === "@me" || q.endsWith("@method:")) {
			return Response.json(
				facetsBody({ matched: 0, facets: [], attrs: [], histogram: [] }),
			);
		}
		return Response.json(facetsBody());
	};
	const withScopes = () => {
		seen.length = 0;
		stubApi(routes({ "GET /environments/:id/logs/facets": facetsReply }));
	};
	const optionTexts = () =>
		screen.getAllByRole("option").map((option) => option.textContent ?? "");

	test("typing @ lists the server's attributes although @ itself matches nothing", async () => {
		withScopes();
		const user = userEvent.setup();
		await render("/?logs=history");
		await screen.findByText(/1,240 lines match/);
		await user.type(searchBox(), "@");
		await waitFor(() => expect(seen).toContain("@"));
		await waitFor(() => {
			const texts = optionTexts();
			expect(texts.some((t) => t.includes("@method:"))).toBe(true);
			expect(texts.some((t) => t.includes("@path:"))).toBe(true);
			expect(texts.some((t) => t.includes("@duration_ms:"))).toBe(true);
		});
	});

	test("typing @method: lists the values with their counts", async () => {
		withScopes();
		const user = userEvent.setup();
		await render("/?logs=history");
		await screen.findByText(/1,240 lines match/);
		await user.type(searchBox(), "@method:");
		await waitFor(() => expect(seen).toContain("@method:"));
		expect(
			await screen.findByRole("option", { name: /GET\s*500/ }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("option", { name: /POST\s*300/ }),
		).toBeInTheDocument();
	});

	test("the values are counted over the query without the clause being typed", async () => {
		withScopes();
		const user = userEvent.setup();
		await render("/?logs=history");
		await screen.findByText(/1,240 lines match/);
		await user.type(searchBox(), "level:error @method:");
		// The error lines have 7 POSTs; the whole range has 300.
		expect(
			await screen.findByRole("option", { name: /POST\s*7\b/ }),
		).toBeInTheDocument();
		expect(seen).toContain("level:error");
	});

	test("the sidebar, the histogram and the footer keep the last whole query's counts while the query ends in a half-typed clause", async () => {
		withScopes();
		const user = userEvent.setup();
		await render("/?logs=history");
		const level = await screen.findByRole("region", { name: "Level" });
		await waitFor(() =>
			expect(within(level).getByText("12")).toBeInTheDocument(),
		);
		await screen.findByText(/1,240 lines match/);
		await user.type(searchBox(), "@me");
		await waitFor(() => expect(seen).toContain("@me"));
		// Let the empty answer land.
		const settle = Promise.withResolvers<void>();
		setTimeout(settle.resolve, 400);
		await settle.promise;
		expect(screen.queryByText(/No facets yet/)).toBeNull();
		expect(
			within(screen.getByRole("region", { name: "Level" })).getByText("12"),
		).toBeInTheDocument();
		// The footer and the histogram do not blank out either.
		expect(screen.getByText(/1,240 lines match/)).toBeInTheDocument();
		expect(
			screen.getByText("6 lines, 1 error in the last hour"),
		).toBeInTheDocument();
	});
});

describe("LogsExplorer notices and live counts", () => {
	test("the ignored-parts banner waits for the clause being typed, and shows for a settled query", async () => {
		const seen: string[] = [];
		stubApi(
			routes({
				"GET /environments/:id/logs/facets": (request: Request) => {
					const q = new URL(request.url).searchParams.get("q") ?? "";
					seen.push(q);
					return Response.json(
						facetsBody(q === "" ? {} : { queryErrors: ["Ignored a term."] }),
					);
				},
			}),
		);
		const user = userEvent.setup();
		await render("/?logs=history");
		await screen.findByText(/1,240 lines match/);
		await user.type(searchBox(), "@method:");
		await waitFor(() => expect(seen).toContain("@method:"));
		// Let the answer for the half-typed query land.
		const settle = Promise.withResolvers<void>();
		setTimeout(settle.resolve, 150);
		await settle.promise;
		expect(screen.queryByText("Part of the search was ignored")).toBeNull();
		await user.type(searchBox(), "POST");
		expect(
			await screen.findByText("Part of the search was ignored"),
		).toBeInTheDocument();
		expect(screen.getByText("Ignored a term.")).toBeInTheDocument();
	});

	test("live counts do not ask again as lines arrive; the 10 s timer is the only trigger", async () => {
		const api = stubApi(
			routes({ "GET /environments/:id/logs/history": { body: page([]) } }),
		);
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		const asked = () =>
			api.calls.filter((c) => c.path.endsWith("/logs/facets")).length;
		await waitFor(() => expect(asked()).toBe(1));
		await push({
			line: "a new line",
			ts: "2026-10-29T10:00:00.000Z",
			stream: "stdout",
			serviceId: "s1",
			serviceName: "api",
		});
		await within(log()).findByText("a new line");
		const settle = Promise.withResolvers<void>();
		setTimeout(settle.resolve, 2500);
		await settle.promise;
		expect(asked()).toBe(1);
	}, 8000);
});

describe("LogsExplorer counts that are loading or failed", () => {
	test("while the first counts load the histogram is a skeleton, not an empty range", async () => {
		const gate = Promise.withResolvers<void>();
		stubApi(
			routes({
				"GET /environments/:id/logs/facets": async () => {
					await gate.promise;
					return Response.json(facetsBody());
				},
			}),
		);
		await render("/?logs=history");
		await within(log()).findByText("server listening on :8080");
		expect(screen.getAllByText(/Loading counts/).length).toBeGreaterThan(0);
		expect(screen.queryByText(/No matching lines/)).toBeNull();
		gate.resolve();
		expect(
			await screen.findByText("6 lines, 1 error in the last hour"),
		).toBeInTheDocument();
		expect(screen.queryByText(/Loading counts/)).toBeNull();
	});

	test("failed counts say Counts unavailable with the reason, show an alert, and Try again asks again", async () => {
		let answers = 0;
		const api = stubApi(
			routes({
				"GET /environments/:id/logs/facets": () => {
					answers += 1;
					return answers === 1
						? Response.json(
								{ title: "Internal", status: 500, detail: "scan failed" },
								{ status: 500 },
							)
						: Response.json(facetsBody());
				},
			}),
		);
		const user = userEvent.setup();
		await render("/?logs=history");
		expect(await screen.findByText("Counts did not load")).toBeInTheDocument();
		expect(screen.getAllByText(/Counts unavailable/).length).toBeGreaterThan(1);
		expect(screen.queryByText(/No matching lines/)).toBeNull();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByText("6 lines, 1 error in the last hour"),
		).toBeInTheDocument();
		expect(screen.queryByText("Counts did not load")).toBeNull();
		expect(
			api.calls.filter((c) => c.path.endsWith("/logs/facets")).length,
		).toBeGreaterThanOrEqual(2);
	});

	test("an agent too old to count is Counts unavailable, not an empty range", async () => {
		stubApi(
			routes({
				"GET /environments/:id/logs/facets": {
					body: facetsBody({
						matched: 0,
						histogram: [],
						unavailable: [
							{
								message: "Update the agent.",
								reason: "agent_outdated",
								serverId: "srv2",
								serverName: "beta",
								serviceId: "s2",
								serviceName: "worker",
							},
						],
					}),
				},
			}),
		);
		await render("/?logs=history");
		expect(
			await screen.findByText(
				"Counts unavailable · the agent on beta needs an update",
			),
		).toBeInTheDocument();
		expect(screen.queryByText(/No matching lines/)).toBeNull();
		expect(screen.getByText("Some servers did not answer")).toBeInTheDocument();
		// With a server that did not answer, the request-log note does not apply.
		expect(screen.queryByText(/Request logs are off/)).toBeNull();
	});

	test("the request-log note needs a deployed service", async () => {
		const undeployed = [
			{ ...services[0], currentDeploymentId: null },
		] as unknown as Service[];
		stubApi(
			routes({
				"GET /environments/:id/logs/facets": {
					body: facetsBody({ requestsCapturing: false }),
				},
			}),
		);
		await renderWithProviders(
			<LogsExplorer environment={environment} services={undeployed} />,
			{ path: "/?logs=history" },
		);
		await screen.findByText(/1,240 lines match/);
		expect(screen.queryByText(/Request logs are off/)).toBeNull();
	});
});

describe("LogsExplorer history refresh and retry", () => {
	test("Refresh asks for the history and the counts again", async () => {
		const api = stubApi(routes());
		const user = userEvent.setup();
		await render("/?logs=history");
		await within(log()).findByText("server listening on :8080");
		const asked = (suffix: string) =>
			api.calls.filter((c) => c.path.endsWith(suffix)).length;
		const history = asked("/logs/history");
		const counts = asked("/logs/facets");
		await user.click(screen.getByRole("button", { name: "Refresh" }));
		await waitFor(() =>
			expect(asked("/logs/history")).toBeGreaterThan(history),
		);
		await waitFor(() => expect(asked("/logs/facets")).toBeGreaterThan(counts));
	});

	test("Live has no Refresh", async () => {
		stubApi(routes());
		await render("/");
		expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull();
	});

	test("a failed history says what happened and Try again loads it", async () => {
		let answers = 0;
		stubApi(
			routes({
				"GET /environments/:id/logs/history": () => {
					answers += 1;
					return answers === 1
						? Response.json(
								{ title: "Internal", status: 500, detail: "disk full" },
								{ status: 500 },
							)
						: Response.json(page([runtimeEntry, requestEntry]));
				},
			}),
		);
		const user = userEvent.setup();
		await render("/?logs=history");
		expect(await screen.findByText("Logs did not load")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await within(log()).findByText("server listening on :8080"),
		).toBeInTheDocument();
		expect(screen.queryByText("Logs did not load")).toBeNull();
	});
});

describe("LogsExplorer with log capture off", () => {
	const frame = (line: string, ts: string) => ({
		line,
		ts,
		stream: "stdout",
		serviceId: "s1",
		serviceName: "api",
	});

	test("Live says so and counts the lines it holds for the sidebar", async () => {
		stubApi(
			routes({
				"GET /environments/:id/logs/history": { body: page([]) },
				"GET /environments/:id/logs/facets": {
					body: facetsBody({ capturing: false }),
				},
			}),
		);
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		expect(await screen.findByText("Log capture is off")).toBeInTheDocument();
		await push(frame("ERROR boom", "2026-10-29T10:00:01.000Z"));
		await push(frame("INFO fine", "2026-10-29T10:00:02.000Z"));
		await within(log()).findByText(/fine/);
		const level = await screen.findByRole("region", { name: "Level" });
		// The server's 12 errors are stored lines; capture is off, so the browser's own count (1) shows.
		expect(within(level).queryByText("12")).toBeNull();
		expect(
			within(level).getByRole("button", { name: "Include level:error" }),
		).toBeInTheDocument();
		expect(within(level).getByText("error").closest("li")).toHaveTextContent(
			"1",
		);
		// And the footer counts what is loaded, not the stored range.
		expect(screen.getByText(/2 lines loaded · 2 shown/)).toBeInTheDocument();
	});

	test("with capture on, Live shows the server's counts and no notice", async () => {
		stubApi(
			routes({ "GET /environments/:id/logs/history": { body: page([]) } }),
		);
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		const level = await screen.findByRole("region", { name: "Level" });
		await waitFor(() =>
			expect(within(level).getByText("12")).toBeInTheDocument(),
		);
		expect(screen.queryByText("Log capture is off")).toBeNull();
	});
});

describe("LogsExplorer jump to latest", () => {
	const frame = (line: string, second: number) => ({
		line,
		ts: `2026-10-29T10:00:${String(second).padStart(2, "0")}.000Z`,
		stream: "stdout",
		serviceId: "s1",
		serviceName: "api",
	});
	const liveRoutes = () =>
		stubApi(
			routes({ "GET /environments/:id/logs/history": { body: page([]) } }),
		);
	const pill = () => screen.queryByRole("button", { name: /Jump to latest/ });

	test("following is automatic: no pill while at the bottom", async () => {
		liveRoutes();
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		await push(frame("one", 1));
		await within(log()).findByText("one");
		expect(pill()).toBeNull();
	});

	test("scrolling up stops following and shows the pill with the count of new lines; the pill resumes", async () => {
		liveRoutes();
		const user = userEvent.setup();
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		await push(frame("one", 1));
		await within(log()).findByText("one");
		fireEvent.wheel(log(), { deltaY: -60 });
		expect(
			await screen.findByRole("button", { name: "Jump to latest" }),
		).toBeInTheDocument();
		await push(frame("two", 2));
		await push(frame("three", 3));
		expect(
			await screen.findByRole("button", {
				name: "2 new lines · Jump to latest",
			}),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: "2 new lines · Jump to latest" }),
		);
		await waitFor(() => expect(pill()).toBeNull());
	});

	test("scrolling back to the bottom resumes following", async () => {
		liveRoutes();
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		await push(frame("one", 1));
		await within(log()).findByText("one");
		fireEvent.wheel(log(), { deltaY: -60 });
		await screen.findByRole("button", { name: /Jump to latest/ });
		Object.defineProperty(log(), "scrollHeight", {
			value: 1000,
			configurable: true,
		});
		log().scrollTop = 600;
		fireEvent.scroll(log());
		await waitFor(() => expect(pill()).toBeNull());
	});

	test("the pill is a live-only control, with a static label and motion gated to motion-safe", async () => {
		liveRoutes();
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		await push(frame("one", 1));
		await within(log()).findByText("one");
		fireEvent.wheel(log(), { deltaY: -60 });
		const button = await screen.findByRole("button", {
			name: /Jump to latest/,
		});
		expect(button.className).toContain("motion-safe:transition-");
		expect(button.className).toContain("starting:opacity-0");
		expect(button.className).not.toMatch(/(^|\s)transition-/);
	});

	test("History has no pill", async () => {
		stubApi(routes());
		await render("/?logs=history");
		await within(log()).findByText("server listening on :8080");
		fireEvent.wheel(log(), { deltaY: -60 });
		expect(pill()).toBeNull();
	});
});

describe("LogsExplorer facets for requests", () => {
	const withStream = (extra: Record<string, unknown> = {}) =>
		facetsBody({
			facets: [
				...(facetsBody().facets as unknown[]),
				{
					key: "stream",
					count: 1240,
					values: [
						{ value: "runtime", count: 440 },
						{ value: "requests", count: 800 },
					],
				},
			],
			...extra,
		});
	const sidebar = () =>
		routes({ "GET /environments/:id/logs/facets": { body: withStream() } });

	test("status is grouped by class with the class as text and the codes behind a disclosure", async () => {
		stubApi(sidebar());
		const user = userEvent.setup();
		await render("/?logs=history");
		const status = await screen.findByRole("region", { name: "Status" });
		const row = (label: string) =>
			within(status).getByText(label).closest("li");
		// 700 + 20 | 60 | 15 + 5, summed from the server's codes.
		expect(row("2xx")).toHaveTextContent("720");
		expect(row("4xx")).toHaveTextContent("60");
		expect(row("5xx")).toHaveTextContent("20");
		expect(within(status).queryByText("502")).toBeNull();
		await user.click(
			within(status).getByRole("button", { name: "Show 5xx codes" }),
		);
		expect(within(status).getByText("502")).toBeInTheDocument();
		await user.click(
			within(status).getByRole("button", { name: "Include status:5*" }),
		);
		expect(searchBox()).toHaveValue("status:5*");
	});

	test("the Requests group lists kind, method, path and duration ranges that add terms", async () => {
		stubApi(sidebar());
		const user = userEvent.setup();
		await render("/?logs=history");
		const requests = await screen.findByRole("region", { name: "Requests" });
		expect(
			within(within(requests).getByRole("region", { name: "Kind" })).getByText(
				"asset",
			),
		).toBeInTheDocument();
		expect(
			within(within(requests).getByRole("region", { name: "Path" })).getByText(
				"/api/orders",
			),
		).toBeInTheDocument();
		await user.click(
			within(requests).getByRole("button", { name: "Include @method:GET" }),
		);
		expect(searchBox()).toHaveValue("@method:GET");
		await user.click(
			within(requests).getByRole("button", { name: "100 ms – 1 s" }),
		);
		expect(searchBox()).toHaveValue("@method:GET @duration_ms:[100 TO 1000]");
		await user.click(within(requests).getByRole("button", { name: "> 1 s" }));
		expect(searchBox()).toHaveValue("@method:GET @duration_ms:>1000");
	});

	test("a runtime-only view keeps the plain status facet and has no Requests group", async () => {
		stubApi(sidebar());
		await render("/?logs=history&sources=runtime");
		const status = await screen.findByRole("region", { name: "Status" });
		expect(within(status).getByText("502")).toBeInTheDocument();
		expect(within(status).queryByText("5xx")).toBeNull();
		expect(screen.queryByRole("region", { name: "Requests" })).toBeNull();
	});

	test("the Stream facet shows for All and is left out when one source is chosen", async () => {
		stubApi(sidebar());
		const user = userEvent.setup();
		await render("/?logs=history");
		expect(
			await screen.findByRole("region", { name: "Stream" }),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Requests" }));
		await waitFor(() =>
			expect(screen.queryByRole("region", { name: "Stream" })).toBeNull(),
		);
		await user.click(screen.getByRole("button", { name: "All" }));
		expect(
			await screen.findByRole("region", { name: "Stream" }),
		).toBeInTheDocument();
	});
});

describe("LogsExplorer request rows", () => {
	const req = (over: Record<string, unknown>, ts: string, service = "api") => ({
		...requestEntry,
		ts,
		serviceId: service === "api" ? "s1" : "s2",
		serviceName: service,
		line: JSON.stringify({
			method: "GET",
			host: "app.example.com",
			path: "/x",
			status: 200,
			duration_ms: 20,
			...over,
		}),
	});
	const historyOf = (items: unknown[]) =>
		routes({ "GET /environments/:id/logs/history": { body: page(items) } });

	test("the service column is left out while one service has lines in view, and shown for two", async () => {
		stubApi(historyOf([req({ path: "/one" }, "2026-10-29T10:00:01.000Z")]));
		const first = await render("/?logs=history");
		await within(log()).findByText("/one");
		expect(within(log()).queryByText("api")).toBeNull();
		first.unmount();

		stubApi(
			historyOf([
				req({ path: "/one" }, "2026-10-29T10:00:01.000Z"),
				req({ path: "/two" }, "2026-10-29T10:00:02.000Z", "worker"),
			]),
		);
		await render("/?logs=history");
		await within(log()).findByText("/two");
		expect(within(log()).getByText("api")).toBeInTheDocument();
		expect(within(log()).getByText("worker")).toBeInTheDocument();
	});

	test("the host is shown only when the requests in view are for more than one", async () => {
		stubApi(
			historyOf([
				req({ path: "/one" }, "2026-10-29T10:00:01.000Z"),
				req(
					{ path: "/two", host: "b.example.com" },
					"2026-10-29T10:00:02.000Z",
				),
			]),
		);
		await render("/?logs=history");
		await within(log()).findByText("/two");
		expect(within(log()).getByText("app.example.com")).toBeInTheDocument();
		expect(within(log()).getByText("b.example.com")).toBeInTheDocument();
	});

	test("duration is coloured by speed with the number always printed; assets get a tag", async () => {
		stubApi(
			historyOf([
				req({ path: "/fast", duration_ms: 40 }, "2026-10-29T10:00:01.000Z"),
				req({ path: "/slow", duration_ms: 1500 }, "2026-10-29T10:00:02.000Z"),
				req(
					{ path: "/stuck", duration_ms: 6000, kind: "asset" },
					"2026-10-29T10:00:03.000Z",
				),
			]),
		);
		await render("/?logs=history");
		await within(log()).findByText("/stuck");
		expect(within(log()).getByText("40 ms")).toHaveClass("text-ink-3");
		expect(within(log()).getByText("1.5 s")).toHaveClass(
			"text-status-staged-on",
		);
		expect(within(log()).getByText("6.0 s")).toHaveClass(
			"text-status-failed-on",
		);
		expect(within(log()).getByText("asset")).toBeInTheDocument();
	});
});

describe("LogsExplorer request detail", () => {
	test("Show logs around this request opens History ±30 s for the service, over all sources, with the pane closed", async () => {
		stubApi(routes());
		const user = userEvent.setup();
		await render("/?sources=requests");
		// Live has no stored lines; History with only the requests source does.
		await user.click(screen.getByRole("button", { name: "History" }));
		await user.click(await within(log()).findByText("/api/orders"));
		const detail = await screen.findByRole("complementary", {
			name: "Log line detail",
		});
		expect(within(detail).getAllByText("POST").length).toBeGreaterThan(0);
		await user.click(
			within(detail).getByRole("button", {
				name: "Show logs around this request",
			}),
		);
		await waitFor(() => expect(address()).toContain("replica:0"));
		expect(address()).toContain("logs=history");
		expect(address()).toContain("q=service:api+replica:0");
		expect(address()).toContain("from=2026-10-29T09:59:50.000Z");
		expect(address()).toContain("to=2026-10-29T10:00:50.000Z");
		expect(address()).not.toContain("sources=");
		expect(address()).not.toContain("range=");
		expect(
			screen.queryByRole("complementary", { name: "Log line detail" }),
		).toBeNull();
		expect(searchBox()).toHaveValue("service:api replica:0");
		expect(screen.getByRole("button", { name: "All" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	test("a runtime line's pane has no such button", async () => {
		stubApi(routes());
		const user = userEvent.setup();
		await render("/?logs=history");
		await user.click(
			await within(log()).findByText("server listening on :8080"),
		);
		const detail = await screen.findByRole("complementary", {
			name: "Log line detail",
		});
		expect(
			within(detail).queryByRole("button", {
				name: "Show logs around this request",
			}),
		).toBeNull();
	});

	test("Escape that closes a popover does not also close the pane; Escape elsewhere does", async () => {
		stubApi(routes());
		const user = userEvent.setup();
		await render("/?logs=history");
		await user.click(await within(log()).findByText("/api/orders"));
		await screen.findByRole("complementary", { name: "Log line detail" });
		const popover = document.createElement("div");
		popover.setAttribute("role", "dialog");
		popover.tabIndex = -1;
		document.body.append(popover);
		fireEvent.keyDown(popover, { key: "Escape" });
		expect(
			screen.getByRole("complementary", { name: "Log line detail" }),
		).toBeInTheDocument();
		popover.remove();
		fireEvent.keyDown(document.body, { key: "Escape" });
		await waitFor(() =>
			expect(
				screen.queryByRole("complementary", { name: "Log line detail" }),
			).toBeNull(),
		);
	});
});

describe("LogsExplorer footer", () => {
	test("Live says how many lines the window holds and how many are shown", async () => {
		stubApi(
			routes({ "GET /environments/:id/logs/history": { body: page([]) } }),
		);
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		await push({
			line: "hello",
			ts: "2026-10-29T10:00:00.000Z",
			stream: "stdout",
			serviceId: "s1",
			serviceName: "api",
		});
		await within(log()).findByText("hello");
		expect(
			await screen.findByText(
				/1,240 in the last 15 min · showing the newest 1(?!\d)/,
			),
		).toBeInTheDocument();
		expect(screen.queryByText(/lines match/)).toBeNull();
	});

	test("Live gives only the count when the list shows all of it", async () => {
		stubApi(
			routes({
				"GET /environments/:id/logs/history": { body: page([]) },
				"GET /environments/:id/logs/facets": {
					body: facetsBody({ matched: 1 }),
				},
			}),
		);
		await render("/");
		await waitFor(() => expect(senders).toHaveLength(1));
		await push({
			line: "hello",
			ts: "2026-10-29T10:00:00.000Z",
			stream: "stdout",
			serviceId: "s1",
			serviceName: "api",
		});
		await within(log()).findByText("hello");
		expect(
			await screen.findByText(/(^|[^\d,])1 in the last 15 min(?! ·\sshowing)/),
		).toBeInTheDocument();
		expect(screen.queryByText(/showing the newest/)).toBeNull();
	});

	test("History keeps the match count of the range and says how many are shown", async () => {
		stubApi(routes());
		await render("/?logs=history");
		expect(
			await screen.findByText(/1,240 lines match in the last hour · 2 shown/),
		).toBeInTheDocument();
	});

	test("the masking note is for when runtime output is included", async () => {
		stubApi(routes());
		const first = await render("/?logs=history");
		expect(
			await screen.findByText(/Runtime output has secret values masked/),
		).toBeInTheDocument();
		first.unmount();
		stubApi(routes());
		await render("/?logs=history&sources=requests");
		await screen.findByText(/1,240 lines match/);
		expect(
			screen.queryByText(/Runtime output has secret values masked/),
		).toBeNull();
	});
});
