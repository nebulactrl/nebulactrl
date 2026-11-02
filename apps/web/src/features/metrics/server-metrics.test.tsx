import { beforeEach, describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Server } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { stubChartSize } from "./chart-size";
import { ServerMetrics } from "./server-metrics";
import {
	isoAgo,
	MB,
	serverBody,
	serverMetrics,
	serverPoint,
} from "./test-fixtures";

beforeEach(() => {
	window.localStorage.clear();
	stubChartSize();
	return () => window.localStorage.clear();
});

const routes = (metrics: unknown, server: Server = serverBody()) => ({
	"GET /servers/:id/metrics": metrics as never,
	"GET /servers/:id": { body: server },
});

const card = (title: string) =>
	screen
		.getByRole("heading", { name: title })
		.closest("section") as HTMLElement;

describe("ServerMetrics", () => {
	test("shows a loading placeholder until the first samples arrive", async () => {
		stubApi(routes(() => new Promise<Response>(() => {})));
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		expect(
			screen.getByRole("status", { name: "Loading metrics" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("heading", { name: "CPU" }),
		).not.toBeInTheDocument();
	});

	test("requests the server's metrics for the default 1h range", async () => {
		const api = stubApi(routes({ body: serverMetrics([serverPoint()]) }));
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		await screen.findByRole("heading", { name: "CPU" });
		const call = api.calls.find((c) => c.path === "/servers/srv_1/metrics");
		expect(call?.method).toBe("GET");
		expect(call?.query.get("range")).toBe("1h");
		expect(api.unmatched).toEqual([]);
	});

	test("CPU, Memory and Disk cards carry the latest values and capacities", async () => {
		stubApi(
			routes({
				body: serverMetrics([
					serverPoint({ ts: isoAgo(20), cpuPercent: 10 }),
					serverPoint({
						cpuPercent: 31.25,
						memPercent: 40.5,
						memBytes: 3 * 1024 * MB,
						memTotalBytes: 8 * 1024 * MB,
						diskPercent: 62.5,
						diskUsedBytes: 50 * 1024 * MB,
						diskTotalBytes: 80 * 1024 * MB,
					}),
				]),
			}),
		);
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		await screen.findByRole("heading", { name: "CPU" });
		expect(
			within(card("CPU")).getByText("Percent of all cores"),
		).toBeInTheDocument();
		expect(within(card("CPU")).getByText("31.3%")).toBeInTheDocument();
		expect(
			within(card("Memory")).getByText("3 GiB of 8 GiB"),
		).toBeInTheDocument();
		expect(within(card("Memory")).getByText("40.5%")).toBeInTheDocument();
		expect(
			within(card("Disk")).getByText("50 GiB of 80 GiB"),
		).toBeInTheDocument();
		expect(within(card("Disk")).getByText("62.5%")).toBeInTheDocument();
		expect(
			screen.getByRole("img", { name: "CPU over time" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("img", { name: "Memory over time" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("img", { name: "Disk over time" }),
		).toBeInTheDocument();
	});

	test("a disk at or over its alert threshold says so in words", async () => {
		stubApi(
			routes(
				{ body: serverMetrics([serverPoint({ diskPercent: 85 })]) },
				serverBody(80),
			),
		);
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		expect(await screen.findByText("Over the 80% alert")).toBeInTheDocument();
		expect(within(card("Disk")).queryByText(/ of /)).not.toBeInTheDocument();
	});

	test("exactly at the threshold counts as over", async () => {
		stubApi(
			routes(
				{ body: serverMetrics([serverPoint({ diskPercent: 80 })]) },
				serverBody(80),
			),
		);
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		expect(await screen.findByText("Over the 80% alert")).toBeInTheDocument();
	});

	test("a disk under its threshold shows used and total instead", async () => {
		stubApi(
			routes(
				{ body: serverMetrics([serverPoint({ diskPercent: 79.9 })]) },
				serverBody(80),
			),
		);
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		await screen.findByRole("heading", { name: "Disk" });
		expect(screen.queryByText(/Over the/)).not.toBeInTheDocument();
		expect(
			within(card("Disk")).getByText("62 GiB of 100 GiB"),
		).toBeInTheDocument();
	});

	test("the disk chart names its alert line", async () => {
		stubApi(routes({ body: serverMetrics([serverPoint()]) }, serverBody(90)));
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		await screen.findByRole("heading", { name: "Disk" });
		await within(card("Disk")).findByText("alert 90%");
	});

	test("an empty response says there are no samples yet", async () => {
		stubApi(routes({ body: serverMetrics([]) }));
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		expect(await screen.findByText("No samples yet")).toBeInTheDocument();
		expect(
			screen.getByText(/Host samples arrive every 10 seconds/),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("heading", { name: "CPU" }),
		).not.toBeInTheDocument();
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	test("a null points list is treated as empty", async () => {
		stubApi(routes({ body: serverMetrics(null) }));
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		expect(await screen.findByText("No samples yet")).toBeInTheDocument();
	});

	test("a failed request shows an error and Try again fetches once more", async () => {
		let calls = 0;
		stubApi(
			routes((): Response => {
				calls += 1;
				return calls === 1
					? Response.json({ title: "boom" }, { status: 500 })
					: Response.json(serverMetrics([serverPoint()]));
			}),
		);
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		expect(await screen.findByText("Metrics did not load")).toBeInTheDocument();
		expect(screen.getByRole("alert")).toHaveTextContent(
			"The control plane did not answer",
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /try again/i }));
		expect(
			await screen.findByRole("heading", { name: "CPU" }),
		).toBeInTheDocument();
		expect(calls).toBe(2);
	});

	test("picking a range refetches with it and remembers the choice", async () => {
		const api = stubApi(routes({ body: serverMetrics([serverPoint()]) }));
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		await screen.findByRole("heading", { name: "CPU" });
		await userEvent.setup().click(screen.getByRole("button", { name: "24h" }));
		await screen.findByRole("button", { name: "24h", pressed: true });
		expect(
			api.calls.some(
				(c) =>
					c.path === "/servers/srv_1/metrics" && c.query.get("range") === "24h",
			),
		).toBe(true);
		expect(window.localStorage.getItem("nebulactrl:server-metrics-range")).toBe(
			"24h",
		);
	});

	test("opens on the range stored from an earlier visit", async () => {
		window.localStorage.setItem("nebulactrl:server-metrics-range", "6h");
		const api = stubApi(routes({ body: serverMetrics([serverPoint()]) }));
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		expect(
			await screen.findByRole("button", { name: "6h", pressed: true }),
		).toBeInTheDocument();
		expect(
			api.calls
				.find((c) => c.path === "/servers/srv_1/metrics")
				?.query.get("range"),
		).toBe("6h");
	});

	test("a stored range that no longer exists falls back to 1h", async () => {
		window.localStorage.setItem("nebulactrl:server-metrics-range", "3y");
		stubApi(routes({ body: serverMetrics([serverPoint()]) }));
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		expect(
			await screen.findByRole("button", { name: "1h", pressed: true }),
		).toBeInTheDocument();
	});

	test("a live range with fresh samples shows Live", async () => {
		window.localStorage.setItem("nebulactrl:server-metrics-range", "5m");
		stubApi(routes({ body: serverMetrics([serverPoint({ ts: isoAgo(5) })]) }));
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		await screen.findByRole("heading", { name: "CPU" });
		const badge = screen.getByRole("status");
		expect(badge).toHaveTextContent("Live");
		expect(badge).not.toHaveTextContent("delayed");
	});

	test("a live range whose newest sample is old says Live · delayed", async () => {
		window.localStorage.setItem("nebulactrl:server-metrics-range", "5m");
		stubApi(
			routes({ body: serverMetrics([serverPoint({ ts: isoAgo(600) })]) }),
		);
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		await screen.findByRole("heading", { name: "CPU" });
		expect(screen.getByRole("status")).toHaveTextContent("Live · delayed");
	});

	test("a range that is not live has no Live cue", async () => {
		stubApi(routes({ body: serverMetrics([serverPoint()]) }));
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		await screen.findByRole("heading", { name: "CPU" });
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	test("the note states retention and step, and warns when the range is longer", async () => {
		window.localStorage.setItem("nebulactrl:server-metrics-range", "30d");
		stubApi(
			routes({
				body: serverMetrics([serverPoint()], {
					retentionDays: 7,
					stepSeconds: 3600,
				}),
			}),
		);
		await renderWithProviders(<ServerMetrics serverId="srv_1" />);
		expect(
			await screen.findByText("History kept for 7 days."),
		).toBeInTheDocument();
		expect(screen.getByText("One point per 1 h.")).toBeInTheDocument();
		expect(
			screen.getByText(/30d reaches further back than the history kept/),
		).toBeInTheDocument();
	});

	test("without a server record the disk card has no alert line or alert text", async () => {
		stubApi({
			"GET /servers/:id/metrics": {
				body: serverMetrics([serverPoint({ diskPercent: 99 })]),
			},
			"GET /servers/:id": problem(404, { detail: "not found" }),
		});
		const { queryClient } = await renderWithProviders(
			<ServerMetrics serverId="srv_1" />,
		);
		await screen.findByRole("heading", { name: "Disk" });
		await waitFor(() => expect(queryClient.isFetching()).toBe(0));
		expect(screen.queryByText(/Over the/)).not.toBeInTheDocument();
		expect(screen.queryByText(/^alert /)).not.toBeInTheDocument();
	});
});
