import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import { stubLayout } from "#/features/logs/test-layout";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { EnvLogStream } from "./env-log-stream";

let restoreLayout: () => void;
beforeEach(() => {
	restoreLayout = stubLayout();
});

/** An open server-sent stream the test writes to by hand. */
interface OpenStream {
	url: URL;
	send(data: unknown): void;
	end(): void;
}

let streams: OpenStream[] = [];
afterEach(() => {
	restoreLayout();
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

const service = (
	id: string,
	name: string,
	server?: { id: string; name: string },
) =>
	({
		id,
		name,
		kind: "app",
		sourceType: "image",
		config: {},
		server: server ? { ...server, status: "online", lastSeenAt: null } : null,
	}) as unknown as Service;

const services = [
	service("s1", "api", { id: "srv1", name: "alpha" }),
	service("s2", "worker", { id: "srv1", name: "alpha" }),
	service("s3", "cron", { id: "srv2", name: "beta" }),
];

const frame = (line: string, extra: Record<string, unknown> = {}) => ({
	line,
	ts: "2026-10-29T10:00:00.000Z",
	stream: "stdout",
	serviceId: "s1",
	serviceName: "api",
	...extra,
});

const render = (path = "/") =>
	renderWithProviders(
		<EnvLogStream environmentId="env1" services={services} />,
		{
			path,
		},
	);

const log = () => screen.getByRole("log", { name: "Environment logs" });
const lastStream = () => streams[streams.length - 1] as OpenStream;
const push = (data: unknown) => act(async () => lastStream().send(data));

describe("EnvLogStream live", () => {
	test("tails the environment's log from the last 200 lines and follows", async () => {
		stubApi({ "GET /environments/:id/logs": sseReply });
		await render();
		await waitFor(() => expect(streams).toHaveLength(1));
		expect(lastStream().url.pathname).toBe("/api/v1/environments/env1/logs");
		expect(lastStream().url.searchParams.get("tail")).toBe("200");
		expect(lastStream().url.searchParams.get("follow")).toBe("true");
	});

	test("says it is connecting until the stream answers", async () => {
		stubApi({ "GET /environments/:id/logs": sseReply });
		await render();
		expect(log()).toHaveTextContent("Connecting…");
		expect(screen.getAllByText("Connecting…").length).toBeGreaterThan(0);
	});

	test("once live with no output it says it is waiting for lines", async () => {
		stubApi({ "GET /environments/:id/logs": sseReply });
		await render();
		await waitFor(() => expect(streams).toHaveLength(1));
		await push({});
		await waitFor(() =>
			expect(log()).toHaveTextContent(
				"Waiting for output. Lines appear here as your services write them.",
			),
		);
		expect(screen.getByText("Live", { selector: "p" })).toBeInTheDocument();
	});

	test("streamed lines appear with the service that wrote them", async () => {
		stubApi({ "GET /environments/:id/logs": sseReply });
		await render();
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame("server listening on :8080"));
		await push(
			frame("job finished", { serviceId: "s2", serviceName: "worker" }),
		);
		expect(
			await within(log()).findByText("server listening on :8080"),
		).toBeInTheDocument();
		expect(within(log()).getByText("job finished")).toBeInTheDocument();
		expect(within(log()).getByText("worker")).toBeInTheDocument();
		expect(within(log()).getByText("api")).toBeInTheDocument();
		expect(screen.getByText("Live", { selector: "p" })).toBeInTheDocument();
	});

	test("blank lines are not shown as rows", async () => {
		stubApi({ "GET /environments/:id/logs": sseReply });
		await render();
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame("   "));
		await push(frame("real line"));
		await within(log()).findByText("real line");
		expect(within(log()).getAllByText("api")).toHaveLength(1);
	});

	test("Clear empties the log", async () => {
		stubApi({ "GET /environments/:id/logs": sseReply });
		const user = userEvent.setup();
		await render();
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame("to be cleared"));
		await within(log()).findByText("to be cleared");
		await user.click(screen.getByRole("button", { name: "Clear" }));
		await waitFor(() =>
			expect(
				within(log()).queryByText("to be cleared"),
			).not.toBeInTheDocument(),
		);
	});

	test("Pause stops the stream and says Paused; Resume opens a new one", async () => {
		stubApi({ "GET /environments/:id/logs": sseReply });
		const user = userEvent.setup();
		await render();
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame("before pause"));
		await within(log()).findByText("before pause");
		const pause = screen.getByRole("button", { name: "Pause" });
		expect(pause).toHaveAttribute("aria-pressed", "false");
		await user.click(pause);
		const resume = screen.getByRole("button", { name: "Resume" });
		expect(resume).toHaveAttribute("aria-pressed", "true");
		expect(screen.getByText("Paused", { selector: "p" })).toBeInTheDocument();
		expect(streams).toHaveLength(1);
		await user.click(resume);
		await waitFor(() => expect(streams).toHaveLength(2));
		expect(screen.getByRole("button", { name: "Pause" })).toBeInTheDocument();
	});

	test("Follow starts pressed and can be switched off", async () => {
		stubApi({ "GET /environments/:id/logs": sseReply });
		const user = userEvent.setup();
		await render();
		const follow = screen.getByRole("button", { name: "Follow" });
		expect(follow).toHaveAttribute("aria-pressed", "true");
		await user.click(follow);
		expect(follow).toHaveAttribute("aria-pressed", "false");
	});

	test("a search that matches nothing says so and Clear search restores the lines", async () => {
		stubApi({ "GET /environments/:id/logs": sseReply });
		const user = userEvent.setup();
		await render();
		await waitFor(() => expect(streams).toHaveLength(1));
		await push(frame("disk almost full"));
		await within(log()).findByText("disk almost full");
		await user.type(
			screen.getByRole("combobox", { name: "Search environment logs" }),
			"zzzz",
		);
		expect(await screen.findByText(/No lines match/)).toBeInTheDocument();
		expect(
			within(log()).getByText("zzzz", { selector: "code" }),
		).toBeInTheDocument();
		await user.click(
			within(log()).getByRole("button", { name: "Clear search" }),
		);
		expect(
			await within(log()).findByText("disk almost full"),
		).toBeInTheDocument();
	});

	test("an ended stream says so", async () => {
		stubApi({ "GET /environments/:id/logs": sseReply });
		await render();
		await waitFor(() => expect(streams).toHaveLength(1));
		await act(async () => lastStream().end());
		await waitFor(() => expect(log()).toHaveTextContent("Stream ended"));
	});
});

describe("EnvLogStream history", () => {
	const page = (items: unknown[], extra: Record<string, unknown> = {}) => ({
		capturing: true,
		items,
		...extra,
	});

	test("?logs=history reads the stored log instead of opening a stream", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/history": {
				body: page([
					{
						line: "stored line",
						ts: "2026-10-29T09:00:00.000Z",
						replica: 0,
						serviceId: "s1",
						serviceName: "api",
						stream: "stdout",
					},
				]),
			},
		});
		await render("/?logs=history");
		expect(
			await screen.findByRole("log", { name: "Environment stored logs" }),
		).toBeInTheDocument();
		expect(await screen.findByText("stored line")).toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toEqual([
			"/environments/env1/logs/history",
		]);
		expect(streams).toHaveLength(0);
		expect(
			screen.getByLabelText("Search environment log history"),
		).toBeInTheDocument();
	});

	test("history says runtime output only, with secrets masked", async () => {
		stubApi({ "GET /environments/:id/logs/history": { body: page([]) } });
		await render("/?logs=history");
		expect(
			await screen.findByText(
				/Runtime output only \(stdout and stderr\), secret values masked/,
			),
		).toBeInTheDocument();
	});

	test("capture off links each server once, even when several services share it", async () => {
		stubApi({
			"GET /environments/:id/logs/history": {
				body: page([], { capturing: false }),
			},
		});
		await render("/?logs=history");
		expect(await screen.findByText("Log capture is off")).toBeInTheDocument();
		const alpha = screen.getAllByRole("link", { name: "alpha" });
		expect(alpha).toHaveLength(1);
		expect(alpha[0]?.getAttribute("href")).toBe(
			"/servers/srv1/settings/logs-and-disk",
		);
		expect(
			screen.getByRole("link", { name: "beta" }).getAttribute("href"),
		).toBe("/servers/srv2/settings/logs-and-disk");
	});

	test("the Live | History switch moves between the two and keeps ?logs in the address", async () => {
		stubApi({
			"GET /environments/:id/logs": sseReply,
			"GET /environments/:id/logs/history": { body: page([]) },
		});
		const user = userEvent.setup();
		await render();
		await waitFor(() => expect(streams).toHaveLength(1));
		await user.click(screen.getByRole("button", { name: "History" }));
		expect(
			await screen.findByRole("log", { name: "Environment stored logs" }),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Live" }));
		expect(
			await screen.findByRole("log", { name: "Environment logs" }),
		).toBeInTheDocument();
	});

	test("a shared ?logs=central link reads as live in an environment", async () => {
		stubApi({ "GET /environments/:id/logs": sseReply });
		await render("/?logs=central");
		expect(
			await screen.findByRole("log", { name: "Environment logs" }),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Live" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.queryByRole("button", { name: /central/i })).toBeNull();
	});
});
