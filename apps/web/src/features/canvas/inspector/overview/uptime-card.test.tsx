import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ServiceReport, UptimePoint } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeService } from "../../lib/fixtures";
import { InspectorContext } from "../inspector-context";
import type { InspectorTab } from "../tab-ids";
import { UptimeCard } from "./uptime-card";

function bucket(
	minute: number,
	overrides: Partial<UptimePoint> = {},
): UptimePoint {
	return {
		at: new Date(Date.UTC(2026, 9, 31, 10, minute)).toISOString(),
		checks: 2,
		up: 2,
		degraded: 0,
		down: 0,
		maintenance: 0,
		avgLatencyMs: 0,
		maxLatencyMs: 0,
		...overrides,
	};
}

function report(overrides: Partial<ServiceReport> = {}): ServiceReport {
	return {
		serviceId: "svc-up",
		state: "up",
		since: "2026-10-30T10:00:00Z",
		range: "1h",
		granularity: "minute",
		availability: { "24h": 100, "7d": 99.5, "30d": 99.99, "90d": null },
		consecutiveFailures: 0,
		failureThreshold: 3,
		incidents: [],
		lastCheckAt: "2026-10-31T10:05:00Z",
		lastError: "",
		lastOkAt: "2026-10-31T10:05:00Z",
		latencyMs: 40,
		monitor: {
			enabled: true,
			alerts: true,
			failureThreshold: 3,
			recoveryThreshold: 2,
			reminderMinutes: 0,
		},
		series: [bucket(0), bucket(1)],
		...overrides,
	};
}

const outage = (id: string, overrides = {}) => ({
	id,
	startedAt: "2026-10-30T09:00:00Z",
	resolvedAt: "2026-10-30T09:12:00Z",
	durationSeconds: 720,
	cause: "HTTP 503 from /healthz",
	notified: true,
	...overrides,
});

async function renderCard(options: {
	body?: ServiceReport;
	failed?: boolean;
	stack?: boolean;
	opened?: InspectorTab[];
}) {
	const api = stubApi({
		"GET /services/:serviceId/uptime": options.failed
			? problem(500, { title: "boom" })
			: { body: options.body },
	});
	const opened = options.opened ?? [];
	await renderWithProviders(
		<InspectorContext.Provider
			value={{ close: () => {}, openTab: (tab) => opened.push(tab) }}
		>
			<UptimeCard
				service={makeService({
					id: "svc-up",
					name: "api",
					kind: options.stack ? "stack" : "app",
				})}
			/>
		</InspectorContext.Provider>,
	);
	return { api };
}

function figure(label: string): string {
	const term = screen.getByText(label, { selector: "dt" });
	return term.parentElement?.querySelector("dd")?.textContent ?? "";
}

describe("UptimeCard with data", () => {
	test("shows the state and since when, availability per window and the request for the 1h range", async () => {
		const { api } = await renderCard({ body: report() });
		expect(await screen.findByText("Up")).toBeInTheDocument();
		expect(screen.getByText(/^since/)).toBeInTheDocument();
		expect(figure("24h")).toBe("100%");
		expect(figure("7d")).toBe("99.5%");
		// Floored, never rounded up: a window with downtime must not read 100%.
		expect(figure("30d")).toBe("99.99%");
		expect(figure("90d")).toBe("—No data");
		expect(api.calls[0]?.path).toBe("/services/svc-up/uptime");
		expect(api.calls[0]?.query.get("range")).toBe("1h");
		expect(api.unmatched).toEqual([]);
	});

	test("no outages recorded says so", async () => {
		await renderCard({ body: report() });
		expect(await screen.findByText("No outages recorded.")).toBeInTheDocument();
	});

	test("lists recent outages with their length, cause and whether one is ongoing", async () => {
		await renderCard({
			body: report({
				state: "down",
				incidents: [
					outage("o1", {
						resolvedAt: null,
						durationSeconds: 90,
						cause: "connection refused",
					}),
					outage("o2"),
				],
			}),
		});
		const region = await screen.findByRole("region", { name: "Uptime" });
		const outages = within(region)
			.getAllByRole("listitem")
			.filter((li) => /outage/i.test(li.textContent ?? ""));
		expect(outages).toHaveLength(2);
		expect(outages[0]).toHaveTextContent("Ongoing outage · 1m");
		expect(outages[0]).toHaveTextContent("connection refused");
		expect(outages[1]).toHaveTextContent("Outage · 12m");
		expect(outages[1]).toHaveTextContent("HTTP 503 from /healthz");
		expect(screen.getByText("Down")).toBeInTheDocument();
	});

	test("shows at most five outages", async () => {
		await renderCard({
			body: report({
				incidents: ["a", "b", "c", "d", "e", "f"].map((id) =>
					outage(id, { cause: `cause ${id}` }),
				),
			}),
		});
		expect(await screen.findByText("cause e")).toBeInTheDocument();
		expect(screen.queryByText("cause f")).toBeNull();
	});

	test("average and slowest latency come from the series; dashes without any", async () => {
		await renderCard({
			body: report({
				series: [
					bucket(0, { up: 2, avgLatencyMs: 100, maxLatencyMs: 150 }),
					bucket(1, { up: 2, avgLatencyMs: 200, maxLatencyMs: 900 }),
				],
			}),
		});
		expect(await screen.findByText("Average latency · 1h")).toBeInTheDocument();
		expect(figure("Average latency · 1h")).toBe("150 ms");
		expect(figure("Slowest check · 1h")).toBe("900 ms");
	});

	test("without latency figures both read a dash", async () => {
		await renderCard({ body: report() });
		await screen.findByText("Average latency · 1h");
		expect(figure("Average latency · 1h")).toBe("—");
		expect(figure("Slowest check · 1h")).toBe("—");
	});

	test("failing checks that have not become an outage show how close it is", async () => {
		await renderCard({
			body: report({ consecutiveFailures: 2, lastError: "HTTP 500" }),
		});
		expect(await screen.findByText("Failing 2/3")).toBeInTheDocument();
		expect(screen.getByText("Last error: HTTP 500")).toBeInTheDocument();
	});

	test("a recovered service does not keep showing its old error", async () => {
		await renderCard({ body: report({ lastError: "HTTP 500" }) });
		await screen.findByText("Up");
		expect(screen.queryByText(/Last error/)).toBeNull();
		expect(screen.queryByText(/Failing/)).toBeNull();
	});

	test("an unhealthy state keeps the last error visible", async () => {
		await renderCard({
			body: report({ state: "degraded", lastError: "slow" }),
		});
		expect(await screen.findByText("Degraded")).toBeInTheDocument();
		expect(screen.getByText("Last error: slow")).toBeInTheDocument();
	});

	test("checks that stopped arriving say since when, hide the 24h figure and link to the monitor settings", async () => {
		const opened: InspectorTab[] = [];
		await renderCard({
			opened,
			body: report({
				state: "unknown",
				since: null,
				lastCheckAt: "2020-03-04T13:46:00Z",
				lastError: "old failure",
				availability: { "24h": null, "7d": 99, "30d": 99, "90d": 99 },
			}),
		});
		expect(await screen.findByText("No data")).toBeInTheDocument();
		expect(
			screen.getByText("No checks since 4 Mar 13:46 UTC"),
		).toBeInTheDocument();
		expect(screen.queryByText("24h", { selector: "dt" })).toBeNull();
		expect(screen.queryByText(/Last error/)).toBeNull();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Monitor settings" }));
		expect(opened).toEqual(["settings"]);
	});

	test("the Settings link of the section opens the Settings tab", async () => {
		const opened: InspectorTab[] = [];
		await renderCard({ opened, body: report() });
		await screen.findByText("Up");
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Settings" }));
		expect(opened).toEqual(["settings"]);
	});

	test("choosing another range asks for it and keeps the card up", async () => {
		const { api } = await renderCard({ body: report() });
		await screen.findByText("Up");
		const group = screen.getByRole("group", {
			name: "Range of the recent checks",
		});
		expect(within(group).getByRole("button", { name: "1h" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		await userEvent
			.setup()
			.click(within(group).getByRole("button", { name: "24h" }));
		await waitFor(() =>
			expect(api.calls.map((c) => c.query.get("range"))).toContain("24h"),
		);
		expect(
			await screen.findByText("Average latency · 24h"),
		).toBeInTheDocument();
	});
});

describe("UptimeCard without data", () => {
	test("a monitored service with no check yet says it has none", async () => {
		await renderCard({
			body: report({
				state: "unknown",
				since: null,
				lastCheckAt: null,
				availability: { "24h": null, "7d": null, "30d": null, "90d": null },
				series: [],
			}),
		});
		expect(await screen.findByText("No checks yet")).toBeInTheDocument();
		expect(
			screen.getByText(/The agent probes a running service every 30 seconds/),
		).toBeInTheDocument();
	});

	test("with monitoring off it says so and points to Settings", async () => {
		const opened: InspectorTab[] = [];
		await renderCard({
			opened,
			body: report({
				state: "paused",
				since: null,
				lastCheckAt: null,
				availability: { "24h": null, "7d": null, "30d": null, "90d": null },
				monitor: {
					enabled: false,
					alerts: false,
					failureThreshold: 3,
					recoveryThreshold: 2,
					reminderMinutes: 0,
				},
				series: [],
			}),
		});
		expect(await screen.findByText("Monitoring is off")).toBeInTheDocument();
		expect(
			screen.getByText(/Turn it on in Settings to probe this service/),
		).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Settings" }));
		expect(opened).toEqual(["settings"]);
	});

	test("a stack without a check yet shows nothing", async () => {
		const { api } = await renderCard({
			stack: true,
			body: report({
				state: "unknown",
				since: null,
				lastCheckAt: null,
				availability: { "24h": null, "7d": null, "30d": null, "90d": null },
				series: [],
			}),
		});
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() => expect(screen.queryByRole("region")).toBeNull());
		expect(screen.queryByText("No checks yet")).toBeNull();
	});
});

describe("UptimeCard while loading and on failure", () => {
	test("while the report loads the section is there, with nothing in it yet", async () => {
		stubApi({ "GET /services/:serviceId/uptime": () => new Promise(() => {}) });
		await renderWithProviders(<UptimeCard service={makeService()} />);
		const region = screen.getByRole("region", { name: "Uptime" });
		expect(region).toBeInTheDocument();
		expect(within(region).queryByText("No data")).toBeNull();
		expect(within(region).queryByText(/did not load/)).toBeNull();
		expect(within(region).queryByRole("button")).toBeNull();
	});

	test("says the uptime did not load, for an app", async () => {
		await renderCard({ failed: true });
		expect(
			await screen.findByText(
				"Uptime did not load. It will refresh by itself.",
			),
		).toBeInTheDocument();
	});

	test("a stack that failed to load shows nothing", async () => {
		const { api } = await renderCard({ stack: true, failed: true });
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() => expect(screen.queryByText(/did not load/)).toBeNull());
		expect(screen.queryByRole("region", { name: "Uptime" })).toBeNull();
	});
});
