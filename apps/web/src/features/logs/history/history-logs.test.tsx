import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { LogHistoryEntry } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { stubLayout } from "../test-layout";
import { HistoryLogs } from "./history-logs";
import type { HistoryScope } from "./use-history-logs";

let restoreLayout: () => void;
beforeEach(() => {
	restoreLayout = stubLayout();
});
afterEach(() => restoreLayout());

const entry = (
	n: number,
	extra: Partial<LogHistoryEntry> = {},
): LogHistoryEntry => ({
	line: `line ${n}`,
	replica: 0,
	serviceId: "svc-1",
	stream: "stdout",
	ts: `2026-10-20T14:00:${String(n).padStart(2, "0")}.000Z`,
	...extra,
});

const SERVICE = {
	kind: "service",
	id: "svc-1",
} as const satisfies HistoryScope;
const HOST = { id: "srv-1", name: "hetzner-01" };

async function show(
	props: { servers?: { id: string; name: string }[]; path?: string } = {},
) {
	const { servers = [HOST], path } = props;
	return renderWithProviders(
		<HistoryLogs
			scope={SERVICE}
			servers={servers}
			modeToggle={<div>mode toggle</div>}
			label="Search log history"
			logLabel="api stored logs"
			facetKeys={["level", "stream"]}
		/>,
		path ? { path } : {},
	);
}

const log = () => screen.getByRole("log", { name: "api stored logs" });
const rows = () =>
	[...log().querySelectorAll("span.whitespace-pre")].map(
		(el) => el.textContent,
	);
const problem = (status: number, detail: string, code?: string) => ({
	status,
	body: { status, title: "Error", detail, ...(code ? { code } : {}) },
});
const historyPath = "/services/:id/logs/history";

describe("HistoryLogs: lines", () => {
	test("draws the stored lines oldest first and says how far back the server keeps logs", async () => {
		stubApi({
			[`GET ${historyPath}`]: {
				body: {
					capturing: true,
					items: [entry(2), entry(1)],
					oldest: "2026-10-01T09:30:00.000Z",
				},
			},
		});
		await show();
		await waitFor(() => expect(rows()).toEqual(["line 1", "line 2"]));
		expect(
			screen.getByText(
				/Showing 2 lines\. This server keeps logs back to 1 Oct/,
			),
		).toBeInTheDocument();
		expect(
			screen.getByText("This is the start of the log in this range."),
		).toBeInTheDocument();
		expect(screen.getByText("mode toggle")).toBeInTheDocument();
	});

	test("shows a loading state before the first page", async () => {
		stubApi({ [`GET ${historyPath}`]: () => new Promise<Response>(() => {}) });
		await show();
		expect(await screen.findByText("Loading history")).toBeInTheDocument();
	});

	test("an empty range says so", async () => {
		stubApi({
			[`GET ${historyPath}`]: { body: { capturing: true, items: [] } },
		});
		await show();
		expect(
			await screen.findByText("No lines in this range."),
		).toBeInTheDocument();
		expect(screen.queryByText(/start of the log/)).not.toBeInTheDocument();
	});
});

describe("HistoryLogs: paging", () => {
	test("Load older lines adds the next page in front", async () => {
		const api = stubApi({
			[`GET ${historyPath}`]: (request) => {
				const cursor = new URL(request.url).searchParams.get("cursor");
				return Response.json(
					cursor === null
						? { capturing: true, items: [entry(4), entry(3)], nextCursor: "c1" }
						: { capturing: true, items: [entry(2), entry(1)] },
				);
			},
		});
		const user = userEvent.setup();
		await show();
		await waitFor(() => expect(rows()).toEqual(["line 3", "line 4"]));
		await user.click(screen.getByRole("button", { name: "Load older lines" }));
		await waitFor(() =>
			expect(rows()).toEqual(["line 1", "line 2", "line 3", "line 4"]),
		);
		expect(api.calls.map((c) => c.query.get("cursor"))).toEqual([null, "c1"]);
		expect(
			screen.queryByRole("button", { name: "Load older lines" }),
		).toBeNull();
		expect(
			screen.getByText("This is the start of the log in this range."),
		).toBeInTheDocument();
	});

	test("a partial scan with lines offers to keep searching", async () => {
		const api = stubApi({
			[`GET ${historyPath}`]: (request) => {
				const cursor = new URL(request.url).searchParams.get("cursor");
				return Response.json(
					cursor === null
						? {
								capturing: true,
								items: [entry(3)],
								partial: true,
								nextCursor: "p1",
							}
						: { capturing: true, items: [entry(2)] },
				);
			},
		});
		const user = userEvent.setup();
		await show();
		expect(
			await screen.findByText("Searched part of the log."),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Keep searching" }));
		await waitFor(() => expect(rows()).toEqual(["line 2", "line 3"]));
		expect(api.calls[1]?.query.get("cursor")).toBe("p1");
	});

	test("a partial scan without lines says it found none yet", async () => {
		stubApi({
			[`GET ${historyPath}`]: {
				body: { capturing: true, items: [], partial: true, nextCursor: "p1" },
			},
		});
		await show();
		expect(
			await screen.findByText(
				"Searched part of the log and found no lines yet.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Keep searching" }),
		).toBeEnabled();
	});
});

describe("HistoryLogs: search", () => {
	test("the settled query goes to the server, and a search with no hits says so and can be cleared", async () => {
		const api = stubApi({
			[`GET ${historyPath}`]: (request) => {
				const q = new URL(request.url).searchParams.get("q");
				return Response.json({
					capturing: true,
					items: q === "boom" ? [] : [entry(1)],
				});
			},
		});
		const user = userEvent.setup();
		await show();
		await waitFor(() => expect(rows()).toEqual(["line 1"]));
		await user.type(
			screen.getByRole("combobox", { name: "Search log history" }),
			"boom",
		);
		await waitFor(() =>
			expect(api.calls.some((c) => c.query.get("q") === "boom")).toBe(true),
		);
		expect(await screen.findByText(/No lines match/)).toHaveTextContent(
			"No lines match boom in this range.",
		);
		const before = api.calls.length;
		await user.click(screen.getByText("Clear search"));
		await waitFor(() => expect(rows()).toEqual(["line 1"]));
		// The unfiltered result is still cached: clearing asks for nothing new.
		expect(api.calls).toHaveLength(before);
	});

	test("the first request carries the query from the address", async () => {
		const api = stubApi({
			[`GET ${historyPath}`]: { body: { capturing: true, items: [entry(1)] } },
		});
		await show({ path: "/?q=error" });
		await waitFor(() => expect(api.calls.length).toBeGreaterThan(0));
		expect(api.calls[0]?.query.get("q")).toBe("error");
	});

	test("parts of the query the server ignored are listed", async () => {
		stubApi({
			[`GET ${historyPath}`]: {
				body: {
					capturing: true,
					items: [entry(1)],
					queryErrors: ["unknown field foo", "bad range"],
				},
			},
		});
		await show();
		expect(
			await screen.findByText("Part of the search was ignored"),
		).toBeInTheDocument();
		expect(screen.getByText("unknown field foo bad range")).toBeInTheDocument();
	});
});

describe("HistoryLogs: states", () => {
	test("a server that is offline is named", async () => {
		stubApi({
			[`GET ${historyPath}`]: problem(503, "agent offline", "server_offline"),
		});
		await show();
		expect(await screen.findByText("Server offline")).toBeInTheDocument();
		expect(
			screen.getByText(
				"History is stored on hetzner-01 and is unavailable while it is offline.",
			),
		).toBeInTheDocument();
		expect(screen.getByText("No lines to show.")).toBeInTheDocument();
	});

	test("an offline server with no known name uses the server's own message", async () => {
		stubApi({
			[`GET ${historyPath}`]: problem(503, "agent offline", "server_offline"),
		});
		await show({ servers: [] });
		expect(await screen.findByText("agent offline")).toBeInTheDocument();
	});

	test("an agent that is too old asks for an update", async () => {
		stubApi({
			[`GET ${historyPath}`]: problem(409, "old agent", "agent_outdated"),
		});
		await show();
		expect(
			await screen.findByText("Agent needs an update"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"The agent on hetzner-01 is too old to read history. Update it from the server's page.",
			),
		).toBeInTheDocument();
	});

	test("an old agent with no known server shows the server's message", async () => {
		stubApi({
			[`GET ${historyPath}`]: problem(409, "old agent", "agent_outdated"),
		});
		await show({ servers: [] });
		expect(await screen.findByText("old agent")).toBeInTheDocument();
	});

	test("another failure shows its message and Try again asks again", async () => {
		let attempt = 0;
		const api = stubApi({
			[`GET ${historyPath}`]: () => {
				attempt += 1;
				return attempt === 1
					? Response.json(
							{ status: 500, title: "Error", detail: "disk read failed" },
							{ status: 500 },
						)
					: Response.json({ capturing: true, items: [entry(1)] });
			},
		});
		const user = userEvent.setup();
		await show();
		expect(await screen.findByText("History did not load")).toBeInTheDocument();
		expect(screen.getByText("disk read failed")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		await waitFor(() => expect(rows()).toEqual(["line 1"]));
		expect(api.calls).toHaveLength(2);
	});

	test("services whose servers could not answer are listed", async () => {
		stubApi({
			[`GET ${historyPath}`]: {
				body: {
					capturing: true,
					items: [entry(1)],
					unavailable: [
						{
							message: "agent unreachable",
							reason: "server_offline",
							serverId: "srv-2",
							serverName: "hetzner-02",
							serviceId: "svc-2",
							serviceName: "worker",
						},
					],
				},
			},
		});
		await show();
		expect(
			await screen.findByText("Some services are missing"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"History is stored on hetzner-02 and is unavailable while it is offline.",
			),
		).toBeInTheDocument();
		const item = screen.getByText("worker").closest("li");
		expect(item).toHaveTextContent("worker on hetzner-02: agent unreachable");
	});

	test("capture switched off says where to turn it on and links the servers", async () => {
		stubApi({
			[`GET ${historyPath}`]: { body: { capturing: false, items: [] } },
		});
		await show({ servers: [HOST, { id: "srv-2", name: "hetzner-02" }] });
		expect(await screen.findByText("Log capture is off")).toBeInTheDocument();
		expect(
			screen.getByText(/Log capture is switched off on this server\./),
		).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "hetzner-01" })).toHaveAttribute(
			"href",
			"/servers/srv-1/settings/logs-and-disk",
		);
		expect(screen.getByRole("link", { name: "hetzner-02" })).toHaveAttribute(
			"href",
			"/servers/srv-2/settings/logs-and-disk",
		);
	});
});

describe("HistoryLogs: toolbar", () => {
	test("has Download", async () => {
		stubApi({
			[`GET ${historyPath}`]: { body: { capturing: true, items: [] } },
		});
		await show();
		expect(
			await screen.findByRole("button", { name: /Download/ }),
		).toBeInTheDocument();
	});

	test("Refresh looks for newer lines from a new now", async () => {
		const api = stubApi({
			[`GET ${historyPath}`]: { body: { capturing: true, items: [entry(1)] } },
		});
		const user = userEvent.setup();
		await show();
		await waitFor(() => expect(rows()).toEqual(["line 1"]));
		const first = api.calls[0]?.query.get("to") ?? "";
		await new Promise((r) => setTimeout(r, 5));
		await user.click(screen.getByRole("button", { name: "Refresh" }));
		await waitFor(() => expect(api.calls).toHaveLength(2));
		expect(Date.parse(api.calls[1]?.query.get("to") ?? "")).toBeGreaterThan(
			Date.parse(first),
		);
	});

	test("choosing another range asks the server for it", async () => {
		const api = stubApi({
			[`GET ${historyPath}`]: { body: { capturing: true, items: [entry(1)] } },
		});
		const user = userEvent.setup();
		await show();
		await waitFor(() => expect(rows()).toEqual(["line 1"]));
		await user.click(screen.getByRole("button", { name: "Time range" }));
		await user.click(
			await screen.findByRole("button", { name: "Last 24 hours" }),
		);
		await waitFor(() => expect(api.calls).toHaveLength(2));
		const last = api.calls[1];
		expect(
			Date.parse(last?.query.get("to") ?? "") -
				Date.parse(last?.query.get("from") ?? ""),
		).toBe(24 * 60 * 60_000);
	});
});
