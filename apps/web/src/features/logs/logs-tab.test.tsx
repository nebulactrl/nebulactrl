import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { makeService } from "../canvas/lib/fixtures";
import { LogsTab } from "./logs-tab";
import { stubLayout } from "./test-layout";

let restoreLayout: () => void;
beforeEach(() => {
	restoreLayout = stubLayout();
});
afterEach(() => restoreLayout());

interface Stream {
	url: URL;
	send(frame: { line: string; ts?: string }): void;
	end(): void;
}

/** A log stream the test feeds by hand; every request to it is recorded. */
function liveStream() {
	const streams: Stream[] = [];
	const reply = (request: Request): Response => {
		const encoder = new TextEncoder();
		let controller!: ReadableStreamDefaultController<Uint8Array>;
		const body = new ReadableStream<Uint8Array>({
			start: (c) => {
				controller = c;
			},
		});
		streams.push({
			url: new URL(request.url),
			send: (frame) =>
				controller.enqueue(
					encoder.encode(`data: ${JSON.stringify(frame)}\n\n`),
				),
			end: () => {
				controller.enqueue(encoder.encode("event: end\ndata: {}\n\n"));
				controller.close();
			},
		});
		return new Response(body, {
			headers: { "Content-Type": "text/event-stream" },
		});
	};
	return { streams, reply };
}

const service = (overrides: Partial<Service> = {}) =>
	makeService({ id: "svc-1", name: "api", ...overrides });

const runtimeRows = () =>
	[
		...screen
			.getByRole("log", { name: "api runtime logs" })
			.querySelectorAll("span.whitespace-pre"),
	].map((el) => el.textContent);

async function sendLines(stream: Stream, lines: string[]) {
	await act(async () => {
		for (const line of lines) stream.send({ line });
	});
}

describe("LogsTab: live", () => {
	test("opens the service's tail (200 lines, following) and shows what arrives", async () => {
		const live = liveStream();
		const api = stubApi({ "GET /services/:id/logs": live.reply });
		await renderWithProviders(<LogsTab service={service()} />);
		expect(screen.getByText("Connecting…")).toBeInTheDocument();
		await waitFor(() => expect(live.streams).toHaveLength(1));
		expect(api.calls[0]?.path).toBe("/services/svc-1/logs");
		expect(api.calls[0]?.query.get("tail")).toBe("200");
		expect(api.calls[0]?.query.get("follow")).toBe("true");
		expect(api.calls[0]?.query.has("component")).toBe(false);
		await sendLines(live.streams[0] as Stream, ["first", "ERROR second"]);
		await waitFor(() =>
			expect(runtimeRows()).toEqual(["first", "ERROR second"]),
		);
		expect(
			await screen.findByText("Live", { selector: "footer span" }),
		).toBeInTheDocument();
		expect(screen.getByText(/Searching the last 2 lines/)).toBeInTheDocument();
	});

	test("waits with a sentence while nothing has arrived, and says when the stream ended", async () => {
		const live = liveStream();
		stubApi({ "GET /services/:id/logs": live.reply });
		await renderWithProviders(<LogsTab service={service()} />);
		expect(screen.getByText("Waiting for log output…")).toBeInTheDocument();
		await waitFor(() => expect(live.streams).toHaveLength(1));
		await act(async () => live.streams[0]?.end());
		expect(await screen.findByText("Stream ended")).toBeInTheDocument();
		expect(screen.getByText("No log output yet.")).toBeInTheDocument();
	});

	test("searching narrows the window; a search with no match offers to clear it", async () => {
		const live = liveStream();
		stubApi({ "GET /services/:id/logs": live.reply });
		const user = userEvent.setup();
		await renderWithProviders(<LogsTab service={service()} />);
		await waitFor(() => expect(live.streams).toHaveLength(1));
		await sendLines(live.streams[0] as Stream, [
			"ERROR disk full",
			"INFO started",
			"ERROR timeout",
		]);
		await waitFor(() => expect(runtimeRows()).toHaveLength(3));
		const box = screen.getByRole("combobox", { name: "Search logs" });
		await user.type(box, "level:error");
		await waitFor(() =>
			expect(runtimeRows()).toEqual(["ERROR disk full", "ERROR timeout"]),
		);
		expect(screen.getByText(/2 of 3 lines/)).toBeInTheDocument();
		await user.clear(box);
		await user.type(box, "zzz");
		expect(await screen.findByText(/No lines match/)).toBeInTheDocument();
		await user.click(screen.getByText("Clear search"));
		await waitFor(() => expect(runtimeRows()).toHaveLength(3));
	});

	test("Clear empties the view without touching the stream", async () => {
		const live = liveStream();
		stubApi({ "GET /services/:id/logs": live.reply });
		const user = userEvent.setup();
		await renderWithProviders(<LogsTab service={service()} />);
		await waitFor(() => expect(live.streams).toHaveLength(1));
		await sendLines(live.streams[0] as Stream, ["one", "two"]);
		await waitFor(() => expect(runtimeRows()).toHaveLength(2));
		await user.click(screen.getByRole("button", { name: "Clear" }));
		expect(await screen.findByText("No log output yet.")).toBeInTheDocument();
		await sendLines(live.streams[0] as Stream, ["three"]);
		await waitFor(() => expect(runtimeRows()).toEqual(["three"]));
	});

	test("scrolling up lets go of the tail and Jump to latest takes it back", async () => {
		const live = liveStream();
		stubApi({ "GET /services/:id/logs": live.reply });
		const user = userEvent.setup();
		await renderWithProviders(<LogsTab service={service()} />);
		await waitFor(() => expect(live.streams).toHaveLength(1));
		await sendLines(live.streams[0] as Stream, ["one"]);
		await waitFor(() => expect(runtimeRows()).toEqual(["one"]));
		const follow = screen.getByRole("switch", { name: "Follow" });
		expect(follow).toBeChecked();
		expect(
			screen.queryByRole("button", { name: "Jump to latest" }),
		).not.toBeInTheDocument();
		fireEvent.wheel(screen.getByRole("log", { name: "api runtime logs" }), {
			deltaY: -50,
		});
		expect(follow).not.toBeChecked();
		await user.click(
			await screen.findByRole("button", { name: "Jump to latest" }),
		);
		expect(follow).toBeChecked();
		expect(
			screen.queryByRole("button", { name: "Jump to latest" }),
		).not.toBeInTheDocument();
	});

	test("the Follow switch can be turned off by hand", async () => {
		const live = liveStream();
		stubApi({ "GET /services/:id/logs": live.reply });
		const user = userEvent.setup();
		await renderWithProviders(<LogsTab service={service()} />);
		await user.click(screen.getByRole("switch", { name: "Follow" }));
		expect(screen.getByRole("switch", { name: "Follow" })).not.toBeChecked();
	});

	test("offers Live, History and Central logs, with Live pressed", async () => {
		stubApi({ "GET /services/:id/logs": liveStream().reply });
		await renderWithProviders(<LogsTab service={service()} />);
		expect(screen.getByRole("button", { name: "Live" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "History" })).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Central logs (7 days)" }),
		).toBeInTheDocument();
	});
});

describe("LogsTab: filters", () => {
	const stack = service({
		kind: "stack",
		config: {
			...service().config,
			stack: {
				components: [
					{ name: "db", index: 0 },
					{ name: "web", index: 1 },
				],
			},
		} as never,
	});

	test("a stack offers its components and tails the one picked", async () => {
		const live = liveStream();
		const api = stubApi({ "GET /services/:id/logs": live.reply });
		const user = userEvent.setup();
		await renderWithProviders(<LogsTab service={stack} />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.query.has("component")).toBe(false);
		await user.click(screen.getByRole("button", { name: "web" }));
		await waitFor(() => expect(api.calls).toHaveLength(2));
		expect(api.calls[1]?.query.get("component")).toBe("web");
		expect(screen.getByRole("button", { name: "web" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	test("a component the stack no longer has is ignored", async () => {
		const live = liveStream();
		const api = stubApi({ "GET /services/:id/logs": live.reply });
		await renderWithProviders(<LogsTab service={stack} />, {
			path: "/?component=gone",
		});
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.query.has("component")).toBe(false);
		expect(
			screen.getByRole("button", { name: "All components" }),
		).toHaveAttribute("aria-pressed", "true");
	});

	const withProcesses = service({
		processes: [
			{ name: "web", default: true, removed: false, pending: false },
			{ name: "worker", default: false, removed: false, pending: false },
		] as never,
	});

	test("a service with processes offers them and tails the one picked", async () => {
		const live = liveStream();
		const api = stubApi({ "GET /services/:id/logs": live.reply });
		const user = userEvent.setup();
		await renderWithProviders(<LogsTab service={withProcesses} />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await user.click(screen.getByRole("button", { name: "worker" }));
		await waitFor(() => expect(api.calls).toHaveLength(2));
		expect(api.calls[1]?.query.get("process")).toBe("worker");
	});

	test("a process that no longer exists is ignored", async () => {
		const live = liveStream();
		const api = stubApi({ "GET /services/:id/logs": live.reply });
		await renderWithProviders(<LogsTab service={withProcesses} />, {
			path: "/?process=removed",
		});
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.query.has("process")).toBe(false);
	});

	test("a plain service has no filter chips", async () => {
		stubApi({ "GET /services/:id/logs": liveStream().reply });
		await renderWithProviders(<LogsTab service={service()} />);
		expect(screen.queryByLabelText("Show logs of")).not.toBeInTheDocument();
	});
});

describe("LogsTab: modes", () => {
	test("History is read from the address and asks the server for stored lines", async () => {
		const api = stubApi({
			"GET /services/:id/logs/history": {
				body: {
					capturing: true,
					items: [
						{
							line: "stored line",
							replica: 0,
							serviceId: "svc-1",
							stream: "stdout",
							ts: "2026-10-20T14:00:00.000Z",
						},
					],
				},
			},
		});
		await renderWithProviders(<LogsTab service={service()} />, {
			path: "/?logs=history",
		});
		await waitFor(() =>
			expect(
				screen.getByRole("log", { name: "api stored logs" }),
			).toHaveTextContent("stored line"),
		);
		expect(api.calls[0]?.path).toBe("/services/svc-1/logs/history");
		expect(screen.getByRole("button", { name: "History" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(
			screen.getByRole("combobox", { name: "Search log history" }),
		).toBeInTheDocument();
	});

	test("Central logs is read from the address", async () => {
		const api = stubApi({
			"GET /services/:id/logs/central": {
				body: { enabled: true, hint: "", lines: [] },
			},
		});
		await renderWithProviders(<LogsTab service={service()} />, {
			path: "/?logs=central",
		});
		expect(
			await screen.findByText("No lines in this range."),
		).toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/services/svc-1/logs/central");
	});

	test("switching from Live to History and back changes what is asked for", async () => {
		const live = liveStream();
		const api = stubApi({
			"GET /services/:id/logs": live.reply,
			"GET /services/:id/logs/history": {
				body: { capturing: true, items: [] },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<LogsTab service={service()} />);
		await waitFor(() => expect(live.streams).toHaveLength(1));
		await user.click(screen.getByRole("button", { name: "History" }));
		await waitFor(() =>
			expect(
				api.calls.some((c) => c.path === "/services/svc-1/logs/history"),
			).toBe(true),
		);
		expect(
			await screen.findByRole("combobox", { name: "Search log history" }),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Live" }));
		expect(
			await screen.findByRole("combobox", { name: "Search logs" }),
		).toBeInTheDocument();
	});
});
