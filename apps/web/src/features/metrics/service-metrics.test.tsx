import { beforeEach, describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DeploymentView } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import {
	makeProcess,
	makeProcessService,
	makeScaling,
} from "#/features/processes/fixtures";
import { makeStack } from "#/features/stacks/test-fixtures";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { stubChartSize } from "./chart-size";
import { ServiceMetrics } from "./service-metrics";
import { isoAgo, MB, serviceMetrics, servicePoint } from "./test-fixtures";

beforeEach(() => {
	window.localStorage.clear();
	stubChartSize();
	return () => window.localStorage.clear();
});

const SERVICE_ID = "svc-1";

interface Setup {
	metrics?: unknown;
	service?: Service;
	deployments?: DeploymentView[];
	path?: string;
	/** Wait for every request to settle; off where a request never answers. */
	settle?: boolean;
}

async function open({
	metrics,
	service,
	deployments = [],
	path = "/",
	settle = true,
}: Setup) {
	const api = stubApi({
		"GET /services/:id/metrics": (metrics ?? {
			body: serviceMetrics([servicePoint()]),
		}) as never,
		"GET /services/:id": {
			body: service ?? makeService({ id: SERVICE_ID }),
		},
		"GET /services/:id/deployments": {
			body: make("PageDeploymentView", { items: deployments }),
		},
	});
	const view = await renderWithProviders(
		<ServiceMetrics serviceId={SERVICE_ID} />,
		{ path },
	);
	if (settle)
		await waitFor(() => expect(view.queryClient.isFetching()).toBe(0));
	return { api, ...view };
}

const metricsCalls = (api: {
	calls: { path: string; query: URLSearchParams }[];
}) => api.calls.filter((c) => c.path === `/services/${SERVICE_ID}/metrics`);

const tile = (label: string) =>
	screen.getByText(label).closest("[class*=surface-raised]") as HTMLElement;
const card = (title: string) =>
	screen
		.getByRole("heading", { name: title })
		.closest("section") as HTMLElement;
/** A tile's figure as screen readers get it (the digits themselves are decorative). */
const tileValue = (label: string) => {
	const figure = tile(label).querySelector(".type-metric");
	return figure?.querySelector(".sr-only")?.textContent ?? figure?.textContent;
};
const ready = () => screen.findByRole("heading", { name: "CPU" });

describe("ServiceMetrics states", () => {
	test("shows a loading placeholder until the samples arrive", async () => {
		await open({
			metrics: () => new Promise<Response>(() => {}),
			settle: false,
		});
		expect(
			screen.getByRole("status", { name: "Loading metrics" }),
		).toBeInTheDocument();
		expect(screen.queryByText("CPU now")).not.toBeInTheDocument();
	});

	test("requests the service's metrics for 1h, without a process, plus its deployments", async () => {
		const { api } = await open({});
		await ready();
		const [call] = metricsCalls(api);
		expect(call?.query.get("range")).toBe("1h");
		expect(call?.query.has("process")).toBe(false);
		expect(
			api.calls.some((c) => c.path === `/services/${SERVICE_ID}/deployments`),
		).toBe(true);
		expect(api.unmatched).toEqual([]);
	});

	test("an empty response asks for a running container", async () => {
		await open({ metrics: { body: serviceMetrics([]) } });
		expect(await screen.findByText("No samples yet")).toBeInTheDocument();
		expect(screen.getByText(/container must be running/)).toBeInTheDocument();
		expect(screen.queryByText("CPU now")).not.toBeInTheDocument();
	});

	test("a null points list is treated as empty", async () => {
		await open({ metrics: { body: serviceMetrics(null) } });
		expect(await screen.findByText("No samples yet")).toBeInTheDocument();
	});

	test("a failed request shows an error and Try again fetches once more", async () => {
		let calls = 0;
		await open({
			metrics: (): Response => {
				calls += 1;
				return calls === 1
					? Response.json({ title: "boom" }, { status: 500 })
					: Response.json(serviceMetrics([servicePoint()]));
			},
		});
		expect(await screen.findByText("Metrics did not load")).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /try again/i }));
		expect(await screen.findByText("CPU now")).toBeInTheDocument();
		expect(calls).toBe(2);
	});

	test("the empty and error states still offer the range control", async () => {
		await open({ metrics: { body: serviceMetrics([]) } });
		await screen.findByText("No samples yet");
		expect(
			screen.getByRole("group", { name: "Time range" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});
});

describe("ServiceMetrics tiles and charts", () => {
	test("a single replica shows now, peak and replica tiles with their hints", async () => {
		await open({
			metrics: {
				body: serviceMetrics([
					servicePoint({
						ts: isoAgo(30),
						cpuPercent: 10,
						cpuMax: 90,
						memMax: 480 * MB,
					}),
					servicePoint({
						cpuPercent: 40.04,
						cpuMax: 55,
						memBytes: 256 * MB,
						memLimitBytes: 512 * MB,
						memMax: 300 * MB,
					}),
				]),
			},
		});
		await ready();
		expect(tileValue("CPU now")).toBe("40.0%");
		expect(
			within(tile("CPU now")).getByText("of one core = 100%"),
		).toBeInTheDocument();
		expect(tileValue("Memory now")).toBe("256 MiB");
		expect(
			within(tile("Memory now")).getByText("of 512 MiB limit"),
		).toBeInTheDocument();
		expect(tileValue("CPU peak 1h")).toBe("90.0%");
		expect(
			within(tile("CPU peak 1h")).getByText("highest sample"),
		).toBeInTheDocument();
		expect(tileValue("Memory peak 1h")).toBe("480 MiB");
		expect(
			within(tile("Memory peak 1h")).getByText("94% of limit"),
		).toBeInTheDocument();
		expect(tileValue("Replicas")).toBe("1");
		expect(
			within(tile("Replicas")).getByText("1 of 1 ready"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("img", { name: "CPU over time" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("img", { name: "Memory over time" }),
		).toBeInTheDocument();
		expect(card("CPU")).toHaveTextContent(
			"Percent of one core; 200% is two cores busy.",
		);
		expect(card("Memory")).toHaveTextContent(
			"Working set in MiB, page cache excluded; matches docker stats.",
		);
	});

	test("one replica has no per-replica list and no CPU series switch", async () => {
		await open({});
		await ready();
		expect(
			screen.queryByRole("group", { name: "CPU series" }),
		).not.toBeInTheDocument();
		expect(screen.queryByText("Per replica · now")).not.toBeInTheDocument();
	});

	test("peak tiles follow the selected range", async () => {
		window.localStorage.setItem("nebulactrl:metrics-range", "24h");
		await open({});
		await ready();
		expect(screen.getByText("CPU peak 24h")).toBeInTheDocument();
		expect(screen.getByText("Memory peak 24h")).toBeInTheDocument();
	});

	test("the memory tile turns to a warning at 90% of the limit", async () => {
		await open({
			metrics: {
				body: serviceMetrics([
					servicePoint({ memBytes: 470 * MB, memLimitBytes: 512 * MB }),
				]),
			},
		});
		await ready();
		expect(tile("Memory now")).toHaveAttribute("data-tone", "warning");
		expect(tile("CPU now")).not.toHaveAttribute("data-tone");
	});

	test("memory below 90% of the limit is not a warning", async () => {
		await open({
			metrics: {
				body: serviceMetrics([
					servicePoint({ memBytes: 400 * MB, memLimitBytes: 512 * MB }),
				]),
			},
		});
		await ready();
		expect(tile("Memory now")).not.toHaveAttribute("data-tone");
	});

	test("the CPU chart draws the memory limit chart line and the CPU quota from the configuration", async () => {
		const service = makeService({ id: SERVICE_ID });
		service.config.cpus = 2;
		await open({ service });
		await ready();
		expect(
			within(tile("CPU now")).getByText("of 200% quota"),
		).toBeInTheDocument();
		expect(
			await within(card("CPU")).findByText("quota 200%"),
		).toBeInTheDocument();
		expect(
			await within(card("Memory")).findByText("limit 512 MiB"),
		).toBeInTheDocument();
	});

	test("a limit the samples reached is called out as reached", async () => {
		await open({
			metrics: {
				body: serviceMetrics([
					servicePoint({
						memBytes: 400 * MB,
						memLimitBytes: 512 * MB,
						memMax: 512 * MB,
					}),
				]),
			},
		});
		await ready();
		expect(
			await within(card("Memory")).findByText("limit 512 MiB — reached"),
		).toBeInTheDocument();
	});

	test("without a limit the tiles say so and the % limit unit is disabled", async () => {
		await open({
			metrics: { body: serviceMetrics([servicePoint({ memLimitBytes: 0 })]) },
		});
		await ready();
		expect(
			within(tile("Memory now")).getByText("no limit set"),
		).toBeInTheDocument();
		expect(
			within(tile("Memory peak 1h")).getByText("highest sample"),
		).toBeInTheDocument();
		expect(card("Memory")).toHaveTextContent(
			"Set a memory limit to scale by percent.",
		);
		expect(
			within(card("Memory")).getByRole("button", { name: "% limit" }),
		).toBeDisabled();
		expect(
			within(card("Memory")).queryByText(/^limit /),
		).not.toBeInTheDocument();
	});

	test("with no sampled limit the configured per-container limit applies", async () => {
		const service = makeService({ id: SERVICE_ID });
		service.config.memoryMb = 1024;
		await open({
			service,
			metrics: { body: serviceMetrics([servicePoint({ memLimitBytes: 0 })]) },
		});
		await ready();
		await screen.findByText("of 1 GiB limit");
		expect(
			within(card("Memory")).getByRole("button", { name: "% limit" }),
		).toBeEnabled();
	});

	test("choosing % limit scales memory by the limit and is remembered", async () => {
		await open({});
		await ready();
		await userEvent
			.setup()
			.click(within(card("Memory")).getByRole("button", { name: "% limit" }));
		expect(card("Memory")).toHaveTextContent(
			"Percent of the memory limit (working set, cache excluded).",
		);
		expect(window.localStorage.getItem("nebulactrl:metrics-memory-unit")).toBe(
			"pct",
		);
		expect(
			within(card("Memory")).getByRole("button", { name: "% limit" }),
		).toHaveAttribute("aria-pressed", "true");
	});

	test("a stored % limit choice without a limit falls back to MiB", async () => {
		window.localStorage.setItem("nebulactrl:metrics-memory-unit", "pct");
		await open({
			metrics: { body: serviceMetrics([servicePoint({ memLimitBytes: 0 })]) },
		});
		await ready();
		expect(
			within(card("Memory")).getByRole("button", { name: "MiB" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(card("Memory")).toHaveTextContent("Working set in MiB");
	});

	test("a gap in sampling does not break the charts", async () => {
		await open({
			metrics: {
				body: serviceMetrics([
					servicePoint({ ts: isoAgo(3_000) }),
					servicePoint({ ts: isoAgo(10) }),
				]),
			},
		});
		await ready();
		expect(
			screen.getByRole("img", { name: "CPU over time" }),
		).toBeInTheDocument();
	});

	test("deployments that started appear as labelled markers on the charts", async () => {
		await open({
			deployments: [
				make("DeploymentView", {
					id: "dep-1",
					serviceId: SERVICE_ID,
					startedAt: isoAgo(600),
					release: make("ReleaseView", {
						id: "rel-1",
						imageRef: "",
						createdAt: "2026-01-01T00:00:00Z",
						source: { type: "git", commitSha: "a1b2c3d4e5" },
					}),
				}),
				make("DeploymentView", {
					id: "dep-queued",
					serviceId: SERVICE_ID,
					startedAt: null,
				}),
			],
		});
		await ready();
		expect(
			(await within(card("CPU")).findAllByText("a1b2c3d")).length,
		).toBeGreaterThan(0);
		expect(
			within(card("CPU")).getByRole("img", { name: "CPU over time" }),
		).toBeInTheDocument();
	});
});

describe("ServiceMetrics range and live cue", () => {
	test("picking a range refetches with it and remembers it", async () => {
		const { api } = await open({});
		await ready();
		await userEvent.setup().click(screen.getByRole("button", { name: "7d" }));
		await screen.findByRole("button", { name: "7d", pressed: true });
		expect(metricsCalls(api).some((c) => c.query.get("range") === "7d")).toBe(
			true,
		);
		expect(window.localStorage.getItem("nebulactrl:metrics-range")).toBe("7d");
	});

	test("opens on the stored range", async () => {
		window.localStorage.setItem("nebulactrl:metrics-range", "30m");
		const { api } = await open({});
		await ready();
		expect(metricsCalls(api)[0]?.query.get("range")).toBe("30m");
		expect(
			screen.getByRole("button", { name: "30m", pressed: true }),
		).toBeInTheDocument();
	});

	test("keeps showing the previous range while the next one loads", async () => {
		let release: (() => void) | undefined;
		const { api } = await open({
			metrics: (request: Request) => {
				if (new URL(request.url).searchParams.get("range") === "7d") {
					return new Promise<Response>((resolve) => {
						release = () =>
							resolve(
								Response.json(
									serviceMetrics([servicePoint({ cpuPercent: 77 })]),
								),
							);
					});
				}
				return Response.json(
					serviceMetrics([servicePoint({ cpuPercent: 40 })]),
				);
			},
		});
		await ready();
		await userEvent.setup().click(screen.getByRole("button", { name: "7d" }));
		await screen.findByRole("button", { name: "7d", pressed: true });
		expect(tileValue("CPU now")).toBe("40.0%");
		expect(
			screen.queryByRole("status", { name: "Loading metrics" }),
		).not.toBeInTheDocument();
		release?.();
		await waitFor(() => expect(tileValue("CPU now")).toBe("77.0%"));
		expect(api.unmatched).toEqual([]);
	});

	test("a live range with fresh samples shows Live", async () => {
		window.localStorage.setItem("nebulactrl:metrics-range", "5m");
		await open({
			metrics: { body: serviceMetrics([servicePoint({ ts: isoAgo(3) })]) },
		});
		await ready();
		expect(screen.getByRole("status")).toHaveTextContent("Live");
		expect(screen.getByRole("status")).not.toHaveTextContent("delayed");
	});

	test("a lagging feed says Live · delayed", async () => {
		window.localStorage.setItem("nebulactrl:metrics-range", "5m");
		await open({
			metrics: { body: serviceMetrics([servicePoint({ ts: isoAgo(300) })]) },
		});
		await ready();
		expect(screen.getByRole("status")).toHaveTextContent("Live · delayed");
	});

	test("a range beyond the live limit has no cue", async () => {
		await open({});
		await ready();
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	test("the note states retention and warns when the range reaches further back", async () => {
		window.localStorage.setItem("nebulactrl:metrics-range", "30d");
		await open({
			metrics: {
				body: serviceMetrics([servicePoint()], {
					retentionDays: 14,
					stepSeconds: 300,
				}),
			},
		});
		expect(
			await screen.findByText("History kept for 14 days."),
		).toBeInTheDocument();
		expect(screen.getByText("One point per 5 min.")).toBeInTheDocument();
		expect(
			screen.getByText(
				/30d reaches further back than the history kept, so only the last 14 days show/,
			),
		).toBeInTheDocument();
	});
});

describe("ServiceMetrics replicas", () => {
	const twoReplicas = () =>
		serviceMetrics([
			servicePoint({
				replicaCount: 2,
				cpuPercent: 60,
				cpuPerReplica: 30,
				cpuMax: 70,
				memBytes: 400 * MB,
				memLimitBytes: 1024 * MB,
				memMax: 450 * MB,
				replicas: [
					{ index: 1, cpuPercent: 20, memBytes: 100 * MB },
					{ index: 0, cpuPercent: 40, memBytes: 300 * MB },
				],
			}),
		]);

	test("several replicas show the sum, the per-replica average and a row per replica", async () => {
		await open({ metrics: { body: twoReplicas() } });
		await ready();
		expect(tileValue("CPU now")).toBe("60.0%");
		expect(
			within(tile("CPU now")).getByText("30.0% per replica"),
		).toBeInTheDocument();
		expect(tileValue("Replicas")).toBe("2");
		expect(card("CPU")).toHaveTextContent(
			"Summed over the replicas, percent of one core; 200% is two cores busy.",
		);
		expect(card("Memory")).toHaveTextContent(
			"Working set summed over the replicas, in MiB; page cache excluded.",
		);
		const rows = within(
			screen.getByRole("list", { name: "Per-replica usage now" }),
		).getAllByRole("listitem");
		expect(rows.map((r) => r.textContent)).toEqual([
			expect.stringContaining("replica 0"),
			expect.stringContaining("replica 1"),
		]);
		expect(rows[0]).toHaveTextContent("40.0%");
		expect(rows[0]).toHaveTextContent("300 MiB");
		expect(rows[1]).toHaveTextContent("20.0%");
		expect(screen.getByText("Per replica · now")).toBeInTheDocument();
		expect(screen.getByText(/The newest sample only/)).toBeInTheDocument();
	});

	test("the CPU series switch moves the chart to the per-replica average", async () => {
		await open({ metrics: { body: twoReplicas() } });
		await ready();
		const group = screen.getByRole("group", { name: "CPU series" });
		expect(
			within(group).getByRole("button", { name: "Total" }),
		).toHaveAttribute("aria-pressed", "true");
		await userEvent
			.setup()
			.click(within(group).getByRole("button", { name: "Per replica" }));
		expect(card("CPU")).toHaveTextContent(
			"Average per replica, percent of one core.",
		);
		expect(
			screen.getByRole("img", { name: "CPU per replica over time" }),
		).toBeInTheDocument();
		expect(
			within(group).getByRole("button", { name: "Per replica" }),
		).toHaveAttribute("aria-pressed", "true");
	});

	test("picking one replica narrows the list to it and All brings the rest back", async () => {
		await open({ metrics: { body: twoReplicas() } });
		await ready();
		const user = userEvent.setup();
		const filter = screen.getByRole("list", { name: "Replicas" });
		await user.click(within(filter).getByRole("button", { name: "replica 1" }));
		const list = () =>
			screen.getByRole("list", { name: "Per-replica usage now" });
		expect(within(list()).getAllByRole("listitem")).toHaveLength(1);
		expect(list()).toHaveTextContent("replica 1");
		expect(list()).not.toHaveTextContent("replica 0");
		await user.click(within(filter).getByRole("button", { name: "All" }));
		expect(within(list()).getAllByRole("listitem")).toHaveLength(2);
	});

	test("the replicas tile warns when fewer are ready than wanted", async () => {
		const service = makeService({ id: SERVICE_ID });
		service.scaling = makeScaling({ replicas: 3, ready: 2 });
		await open({ service, metrics: { body: twoReplicas() } });
		await ready();
		expect(
			within(tile("Replicas")).getByText("2 of 3 ready"),
		).toBeInTheDocument();
		expect(tile("Replicas")).toHaveAttribute("data-tone", "warning");
	});

	test("a replica memory limit is the per-replica share of the summed limit", async () => {
		await open({ metrics: { body: twoReplicas() } });
		await ready();
		const rows = within(
			screen.getByRole("list", { name: "Per-replica usage now" }),
		).getAllByRole("listitem");
		expect(within(rows[0] as HTMLElement).getByText("300 MiB")).toHaveAttribute(
			"title",
			"of 512 MiB limit",
		);
	});

	test("a stack names replicas by component and has no CPU series switch", async () => {
		const stack = makeStack({ id: SERVICE_ID });
		await open({
			service: stack,
			metrics: {
				body: serviceMetrics([
					servicePoint({
						replicaCount: 2,
						replicas: [
							{ index: 0, cpuPercent: 10, memBytes: 100 * MB },
							{ index: 1, cpuPercent: 30, memBytes: 200 * MB },
						],
					}),
				]),
			},
		});
		await ready();
		expect(screen.getByText("Per component · now")).toBeInTheDocument();
		const rows = within(
			screen.getByRole("list", { name: "Per-replica usage now" }),
		).getAllByRole("listitem");
		expect(rows[0]).toHaveTextContent("db");
		expect(rows[1]).toHaveTextContent("web");
		expect(card("CPU")).toHaveTextContent("Summed over the components");
		expect(
			screen.queryByRole("group", { name: "CPU series" }),
		).not.toBeInTheDocument();
		expect(within(rows[1] as HTMLElement).getByText("200 MiB")).toHaveAttribute(
			"title",
			"no limit",
		);
	});
});

describe("ServiceMetrics processes", () => {
	const processPoints = () =>
		serviceMetrics([
			servicePoint({
				replicaCount: 2,
				replicas: [
					{ index: 0, cpuPercent: 10, memBytes: 100 * MB },
					{ index: 1, cpuPercent: 30, memBytes: 200 * MB },
				],
			}),
		]);

	test("a service with processes offers the process select, on All processes", async () => {
		await open({
			service: makeProcessService({ id: SERVICE_ID }),
			metrics: { body: processPoints() },
		});
		await ready();
		expect(screen.getByRole("combobox", { name: "Process" })).toHaveTextContent(
			"All processes",
		);
		expect(card("CPU")).toHaveTextContent("Summed over the processes");
		expect(card("Memory")).toHaveTextContent("summed over the processes");
	});

	test("the total of several processes lists no replicas", async () => {
		await open({
			service: makeProcessService({ id: SERVICE_ID }),
			metrics: { body: processPoints() },
		});
		await ready();
		expect(
			screen.queryByRole("list", { name: "Per-replica usage now" }),
		).not.toBeInTheDocument();
	});

	test("a service without processes has no process select", async () => {
		await open({});
		await ready();
		expect(
			screen.queryByRole("combobox", { name: "Process" }),
		).not.toBeInTheDocument();
	});

	test("the process in the address narrows the request and names the replicas after it", async () => {
		const { api } = await open({
			service: makeProcessService({ id: SERVICE_ID }),
			metrics: { body: processPoints() },
			path: "/?process=worker",
		});
		await ready();
		expect(
			metricsCalls(api).some((c) => c.query.get("process") === "worker"),
		).toBe(true);
		expect(screen.getByRole("combobox", { name: "Process" })).toHaveTextContent(
			"worker",
		);
		const rows = within(
			screen.getByRole("list", { name: "Per-replica usage now" }),
		).getAllByRole("listitem");
		expect(rows[0]).toHaveTextContent("worker · replica 0");
		expect(rows[1]).toHaveTextContent("worker · replica 1");
	});

	test("choosing a process in the select refetches for that process", async () => {
		const { api } = await open({
			service: makeProcessService({ id: SERVICE_ID }),
			metrics: { body: processPoints() },
		});
		await ready();
		const user = userEvent.setup();
		await user.click(screen.getByRole("combobox", { name: "Process" }));
		await user.click(await screen.findByRole("option", { name: "worker" }));
		await screen.findByRole("combobox", { name: "Process", expanded: false });
		await within(screen.getByRole("combobox", { name: "Process" })).findByText(
			"worker",
		);
		expect(
			metricsCalls(api).some((c) => c.query.get("process") === "worker"),
		).toBe(true);
	});

	test("a process that no longer exists falls back to the service total", async () => {
		const { api } = await open({
			service: makeProcessService({ id: SERVICE_ID }),
			metrics: { body: processPoints() },
			path: "/?process=gone",
		});
		await ready();
		expect(screen.getByRole("combobox", { name: "Process" })).toHaveTextContent(
			"All processes",
		);
		const last = metricsCalls(api).at(-1);
		expect(last?.query.has("process")).toBe(false);
	});

	test("one process shows its own scaling in the replicas tile and its own limits", async () => {
		const service = makeProcessService({ id: SERVICE_ID });
		service.processes = [
			makeProcess({ name: "web", default: true, port: 3000 }),
			makeProcess({
				name: "worker",
				scaling: makeScaling({ replicas: 2, ready: 1 }),
			}),
		];
		service.config.processes = [
			{
				name: "worker",
				memoryMb: 256,
				cpus: 0.5,
				healthcheck: service.config.healthcheck,
				startCommand: [],
			} as never,
		];
		await open({
			service,
			metrics: {
				body: serviceMetrics([
					servicePoint({
						memLimitBytes: 0,
						replicaCount: 2,
						replicas: [
							{ index: 0, cpuPercent: 5, memBytes: 10 * MB },
							{ index: 1, cpuPercent: 5, memBytes: 10 * MB },
						],
					}),
				]),
			},
			path: "/?process=worker",
		});
		await ready();
		expect(
			within(tile("Replicas")).getByText("1 of 2 ready"),
		).toBeInTheDocument();
		expect(
			within(tile("Replicas")).getByText("1 of 2 ready").closest("div"),
		).toHaveAttribute("data-tone", "warning");
		expect(
			within(tile("Memory now")).getByText("of 512 MiB limit"),
		).toBeInTheDocument();
	});

	test("the all-processes replicas tile reads the agent's observed counts", async () => {
		const service = makeProcessService({ id: SERVICE_ID });
		service.observed = {
			containerId: "c",
			health: "healthy",
			imageId: "x",
			replicas: [],
			processes: [],
			replicasDesired: 4,
			replicasReady: 3,
			startedAt: null,
			state: "running",
		};
		await open({ service, metrics: { body: processPoints() } });
		await ready();
		expect(
			within(tile("Replicas")).getByText("3 of 4 ready"),
		).toBeInTheDocument();
	});

	test("without observation the total sums the running processes' scaling", async () => {
		const service = makeProcessService({ id: SERVICE_ID });
		service.processes = [
			makeProcess({
				name: "web",
				default: true,
				port: 3000,
				scaling: makeScaling({ replicas: 2, ready: 2 }),
			}),
			makeProcess({
				name: "worker",
				scaling: makeScaling({ replicas: 3, ready: 1 }),
			}),
			makeProcess({
				name: "pending",
				pending: true,
				scaling: makeScaling({ replicas: 9, ready: 0 }),
			}),
		];
		await open({ service, metrics: { body: processPoints() } });
		await ready();
		expect(
			within(tile("Replicas")).getByText("3 of 5 ready"),
		).toBeInTheDocument();
	});
});
