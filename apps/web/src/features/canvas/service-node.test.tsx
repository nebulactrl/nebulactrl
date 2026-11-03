import { describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import { ReactFlow, ReactFlowProvider } from "@xyflow/react";
import type { Service } from "#/api/models";
import { makeProcessService } from "#/features/processes/fixtures";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	type CanvasUi,
	CanvasUiContext,
	DIMMED_OPACITY,
} from "./canvas-context";
import type { Plugs } from "./lib/cable";
import {
	makeDeployment,
	makeNoConfigSource,
	makeObserved,
	makeService,
} from "./lib/fixtures";
import { ServiceNode } from "./service-node";

const nodeTypes = { service: ServiceNode };
const NO_PLUGS: Plugs = { left: false, right: false, top: false };

function context(matchIds: ReadonlySet<string> | null = null): CanvasUi {
	return {
		matchIds,
		tick: 0,
		toggleGroup() {},
		renameGroup() {},
		ungroup() {},
		renamingGroupId: null,
		finishRenameRequest() {},
	};
}

const METRICS = (points: unknown[]) => ({
	body: { points, retentionDays: 7, stepSeconds: 15 },
});
const POINT = {
	cpuMax: 0,
	cpuPerReplica: 0,
	cpuPercent: 0,
	memBytes: 256 * 1024 * 1024,
	memLimitBytes: 512 * 1024 * 1024,
	memMax: 0,
	replicaCount: 1,
	ts: "2026-01-01T00:00:00Z",
};

type Routes = Parameters<typeof stubApi>[0];

/** Mounts one card inside React Flow, with the environment's config-source answered empty unless the case says. */
async function card(
	service: Service,
	{
		plugs = NO_PLUGS,
		matchIds = null,
		routes = {},
	}: {
		plugs?: Plugs;
		matchIds?: ReadonlySet<string> | null;
		routes?: Routes;
	} = {},
) {
	const api = stubApi({
		"GET /environments/:environmentId/config-source": {
			body: makeNoConfigSource(),
		},
		"GET /database-engines": { body: { engines: [] } },
		...routes,
	});
	const view = await renderWithProviders(
		<div style={{ width: 800, height: 600 }}>
			<CanvasUiContext.Provider value={context(matchIds)}>
				<ReactFlowProvider>
					<ReactFlow
						nodes={[
							{
								id: service.id,
								type: "service",
								position: { x: 0, y: 0 },
								width: 288,
								height: 160,
								data: { service, plugs },
							},
						]}
						edges={[]}
						nodeTypes={nodeTypes}
					/>
				</ReactFlowProvider>
			</CanvasUiContext.Provider>
		</div>,
	);
	return { ...view, api };
}

/** A service's config without its image, as a git, database or route service has it. */
function configWithout(
	service: Service,
	extra: Partial<Record<keyof Service["config"], unknown>> = {},
): Service["config"] {
	const { image: _image, ...rest } = service.config;
	return { ...rest, ...extra } as Service["config"];
}

const liveWeb = () =>
	makeService({
		name: "Storefront",
		currentDeploymentId: "dep-1",
		latestDeployment: makeDeployment("succeeded", {
			finishedAt: new Date().toISOString(),
		}),
		observed: makeObserved(),
	});

describe("ServiceNode: a running image service", () => {
	test("shows its name, image, server, live pill and replica count", async () => {
		await card(liveWeb());
		expect(screen.getByText("Storefront")).toBeInTheDocument();
		expect(screen.getByText("traefik/whoami:latest")).toBeInTheDocument();
		expect(screen.getByText("hetzner-01")).toBeInTheDocument();
		expect(screen.getAllByText("Live").length).toBeGreaterThan(0);
		expect(screen.getByText("1/1")).toBeInTheDocument();
		expect(screen.getByText("replicas")).toBeInTheDocument();
	});

	test("an image service has nothing to build: its LED strip says Build skipped", async () => {
		await card(liveWeb());
		expect(
			screen.getByRole("img", {
				name: "Build skipped, Release done, Verify done",
			}),
		).toBeInTheDocument();
	});

	test("a git service's Build LED is lit when the deploy built it", async () => {
		const web = liveWeb();
		await card({
			...web,
			sourceType: "git",
			config: configWithout(web, {
				gitUrl: "https://github.com/acme/shop.git",
				gitBranch: "main",
			}),
		});
		expect(
			screen.getByRole("img", {
				name: "Build done, Release done, Verify done",
			}),
		).toBeInTheDocument();
		expect(screen.getByText("acme/shop@main")).toBeInTheDocument();
	});

	test("the caption under the LEDs carries the card's status sentence as its title", async () => {
		await card(liveWeb());
		expect(screen.getByTitle(/^Deployed .* via image$/)).toHaveTextContent(
			/^Live/,
		);
	});

	test("the server chip says where the service runs", async () => {
		await card(liveWeb());
		expect(screen.getByTitle("Runs on hetzner-01")).toBeInTheDocument();
	});
});

describe("ServiceNode: status", () => {
	test("a build in flight shows Building with the running Build LED", async () => {
		await card(
			makeService({
				name: "Api",
				sourceType: "git",
				latestDeployment: makeDeployment("building", {
					createdAt: new Date().toISOString(),
				}),
			}),
		);
		expect(
			screen.getByText("Building", {
				selector: "[data-slot='status-pill'] *, [data-slot='status-pill']",
			}),
		).toBeInTheDocument();
		expect(
			screen.getByRole("img", {
				name: "Build running, Release waiting, Verify waiting",
			}),
		).toBeInTheDocument();
		expect(screen.getByText(/^Building · \d+:\d\d$/)).toBeInTheDocument();
	});

	test("a failed deploy names the phase that failed", async () => {
		await card(
			makeService({
				latestDeployment: makeDeployment("failed", { failedStep: "health" }),
			}),
		);
		expect(screen.getByText("Failed at Verify")).toBeInTheDocument();
		expect(
			screen.getByRole("img", {
				name: "Build skipped, Release done, Verify failed",
			}),
		).toBeInTheDocument();
	});

	test("the pre-deploy command is flagged on the Release LED", async () => {
		await card(
			makeService({
				latestDeployment: makeDeployment("releasing", {
					currentStep: "predeploy",
					createdAt: new Date().toISOString(),
				}),
			}),
		);
		expect(screen.getByText("pre-deploy")).toBeInTheDocument();
		expect(
			screen.getByRole("img", { name: /Release running \(pre-deploy\)/ }),
		).toBeInTheDocument();
	});

	test("a stopped service says Stopped", async () => {
		await card(
			makeService({ desiredState: "stopped", currentDeploymentId: "dep-1" }),
		);
		expect(screen.getAllByText("Stopped").length).toBeGreaterThan(0);
	});

	test("a new commit found by checking is hinted on a live card, and only there", async () => {
		const gitPoll = {
			enabled: true,
			intervalMinutes: 5,
			deploy: false,
			headSha: "abc1234def5678",
			headBranch: "main",
			updateAvailable: true,
		} as const;
		const { unmount } = await card({ ...liveWeb(), gitPoll });
		expect(screen.getByText(/ · New commit abc1234$/)).toBeInTheDocument();
		unmount();
		await card({
			...liveWeb(),
			latestDeployment: makeDeployment("failed", { failedStep: "health" }),
			gitPoll,
		});
		expect(screen.getByText("Failed at Verify")).toBeInTheDocument();
		expect(screen.queryByText(/New commit/)).not.toBeInTheDocument();
	});

	test("a never-deployed service says Not deployed and counts wanted replicas", async () => {
		await card(makeService());
		expect(screen.getAllByText("Not deployed").length).toBeGreaterThan(0);
		expect(screen.getByText("replica")).toBeInTheDocument();
	});

	test("replicas wanted, not yet deployed, are pluralised", async () => {
		const service = makeService();
		await card({
			...service,
			scaling: { ...service.scaling, replicas: 3, ready: 0 },
		});
		expect(screen.getByText("replicas")).toBeInTheDocument();
		expect(screen.getByText("3")).toBeInTheDocument();
	});

	test("a deployed service with replicas down is Degraded with the ready count", async () => {
		await card({
			...liveWeb(),
			observed: makeObserved({ replicasDesired: 3, replicasReady: 1 }),
			scaling: { ...liveWeb().scaling, replicas: 3, ready: 1 },
		});
		expect(screen.getByText("1/3 replicas ready")).toBeInTheDocument();
		expect(screen.getByText("1/3")).toBeInTheDocument();
	});

	test("an offline server turns the chip's title on and flags the card Offline", async () => {
		await card({
			...liveWeb(),
			server: {
				id: "server-1",
				name: "hetzner-01",
				status: "offline",
				lastSeenAt: null,
			},
		});
		expect(screen.getAllByText("Offline").length).toBeGreaterThan(0);
		expect(screen.getByText("Server offline")).toBeInTheDocument();
	});

	test("a service on no server says so", async () => {
		await card(makeService({ serverId: null, server: null }));
		expect(screen.getByText("no server")).toBeInTheDocument();
		expect(screen.getByTitle("Not placed on a server")).toBeInTheDocument();
	});

	test("a stale report is flagged", async () => {
		await card({ ...liveWeb(), stale: true });
		expect(screen.getByText("Stale")).toBeInTheDocument();
		expect(
			screen.getByTitle(
				"No recent report from the server: this state may be out of date",
			),
		).toBeInTheDocument();
	});
});

describe("ServiceNode: staged changes and config", () => {
	test("a new, undeployed service wears a Staged chip that says it is new", async () => {
		await card(makeService({ stagedKind: "new" }));
		expect(screen.getByText("Staged")).toHaveAttribute(
			"title",
			"New service: not deployed yet",
		);
	});

	test("a changed service's Staged chip counts its pending changes", async () => {
		await card({
			...liveWeb(),
			stagedKind: "edited",
			pendingChangeCounts: { variables: 2, settings: 1 },
		});
		expect(screen.getByText("Staged")).toHaveAttribute(
			"title",
			"3 staged change(s) waiting to deploy",
		);
	});

	test("no Staged chip on a clean service", async () => {
		await card(liveWeb());
		expect(screen.queryByText("Staged")).not.toBeInTheDocument();
	});

	test("a service the repo's nebula.toml manages shows where it stands", async () => {
		const service = liveWeb();
		await card(service, {
			routes: {
				"GET /environments/:environmentId/config-source": {
					body: make("EnvironmentConfigSource", {
						mode: "explicit",
						services: [
							make("EnvironmentConfigService", {
								slug: service.slug,
								state: "file_changed",
								changes: 2,
							}),
						],
					}),
				},
			},
		});
		expect(await screen.findByText("nebula.toml differs")).toBeInTheDocument();
	});
});

describe("ServiceNode: volumes, database and memory", () => {
	test("lists the service's volumes with their mount paths", async () => {
		await card(
			makeService({
				volumes: [
					{ id: "v1", name: "uploads", mountPath: "/var/uploads" },
				] as Service["volumes"],
			}),
		);
		expect(screen.getByTitle("uploads at /var/uploads")).toHaveTextContent(
			"uploads/var/uploads",
		);
	});

	test("a managed database shows its engine and version in place of an image", async () => {
		const base = makeService({ kind: "database" });
		await card({
			...base,
			config: configWithout(base, {
				database: { engine: "postgres", version: "16" },
			}),
		});
		expect(await screen.findByText("v16")).toBeInTheDocument();
		expect(screen.getByTitle("postgres v16")).toBeInTheDocument();
	});

	test("a running service shows its memory use against the limit", async () => {
		const service = liveWeb();
		await card(service, {
			routes: { "GET /services/:serviceId/metrics": METRICS([POINT]) },
		});
		expect(await screen.findByText("256 MiB / 512 MiB")).toBeInTheDocument();
		expect(
			screen.getByRole("meter", { name: "Memory in use" }),
		).toHaveAttribute("value", "50");
	});

	test("with no limit anywhere it prints the use alone and draws no bar", async () => {
		const service = liveWeb();
		await card(service, {
			routes: {
				"GET /services/:serviceId/metrics": METRICS([
					{ ...POINT, memLimitBytes: 0 },
				]),
			},
		});
		expect(await screen.findByText("256 MiB")).toBeInTheDocument();
		expect(screen.queryByRole("meter")).not.toBeInTheDocument();
	});

	test("a configured memory limit stands in when the sample reports none", async () => {
		const service = liveWeb();
		await card(
			{ ...service, config: { ...service.config, memoryMb: 1024 } },
			{
				routes: {
					"GET /services/:serviceId/metrics": METRICS([
						{ ...POINT, memLimitBytes: 0 },
					]),
				},
			},
		);
		expect(await screen.findByText("256 MiB / 1 GiB")).toBeInTheDocument();
	});

	test("a service that is not running never asks for metrics or shows a memory row", async () => {
		const { api } = await card(makeService());
		expect(screen.queryByText("Memory")).not.toBeInTheDocument();
		expect(api.calls.some((call) => call.path.endsWith("/metrics"))).toBe(
			false,
		);
	});

	test("a sample with no replicas shows no memory row", async () => {
		const { api } = await card(liveWeb(), {
			routes: {
				"GET /services/:serviceId/metrics": METRICS([
					{ ...POINT, replicaCount: 0 },
				]),
			},
		});
		await waitFor(() =>
			expect(api.calls.some((c) => c.path.endsWith("/metrics"))).toBe(true),
		);
		expect(screen.queryByText("Memory")).not.toBeInTheDocument();
	});
});

describe("ServiceNode: jacks, filter and card kinds", () => {
	test("has a left and a right link jack, named after the service", async () => {
		await card(liveWeb());
		expect(
			screen.getByLabelText("Link jack on the left of Storefront"),
		).toBeInTheDocument();
		expect(
			screen.getByLabelText("Link jack on the right of Storefront"),
		).toBeInTheDocument();
		expect(screen.queryByLabelText(/Internet jack/)).not.toBeInTheDocument();
	});

	test("the Internet jack on top appears only when the Internet cable plugs in", async () => {
		await card(liveWeb(), { plugs: { left: false, right: false, top: true } });
		expect(
			screen.getByLabelText("Internet jack on top of Storefront"),
		).toBeInTheDocument();
	});

	test("a card the filter excludes dims and says so to assistive tech", async () => {
		const service = liveWeb();
		const { container } = await card(service, {
			matchIds: new Set(["elsewhere"]),
		});
		expect(screen.getByText("Does not match the filter")).toBeInTheDocument();
		expect(
			container.querySelector<HTMLElement>(`[data-service-id="${service.id}"]`)
				?.style.opacity,
		).toBe(String(DIMMED_OPACITY));
	});

	test("a matching card stays lit", async () => {
		const service = liveWeb();
		const { container } = await card(service, {
			matchIds: new Set([service.id]),
		});
		expect(
			screen.queryByText("Does not match the filter"),
		).not.toBeInTheDocument();
		expect(
			container.querySelector<HTMLElement>(`[data-service-id="${service.id}"]`)
				?.style.opacity,
		).toBe("1");
	});

	test("a service with processes is drawn as a process card", async () => {
		const { container } = await card(makeProcessService());
		expect(container.querySelector("[data-process-card]")).not.toBeNull();
		expect(screen.getByText("worker")).toBeInTheDocument();
	});

	test("a route service is drawn as a route card", async () => {
		const base = makeService({ kind: "route", sourceType: "external" });
		const service: Service = {
			...base,
			config: configWithout(base, {
				route: { protocol: "tcp", listenPort: 5432 },
			}),
		};
		const { container } = await card(service, {
			routes: {
				"GET /environments/:environmentId/services": { body: { items: [] } },
			},
		});
		expect(container.querySelector("[data-route-protocol]")).not.toBeNull();
		expect(
			within(container).queryByLabelText(/Link jack on the left/),
		).toBeInTheDocument();
	});

	test("a compose stack is drawn as a stack card", async () => {
		const { container } = await card(
			makeService({ kind: "stack", sourceType: "git" }),
		);
		expect(container.querySelector("[data-stack]")).not.toBeNull();
	});
});

describe("ServiceNode: a cron service", () => {
	const cronCard = (over: Partial<Service> = {}) => {
		const base = makeService();
		return makeService({
			name: "Nightly",
			kind: "worker",
			config: {
				...base.config,
				oneshot: true,
				cron: { schedule: "* * * * *", timezone: "UTC" },
			},
			currentDeploymentId: "dep-1",
			latestDeployment: makeDeployment("succeeded"),
			cron: {
				taskId: "task-1",
				schedule: "* * * * *",
				timezone: "UTC",
				nextRunAt: new Date(Date.now() + 65_000).toISOString(),
				lastRun: {
					id: "run-1",
					status: "succeeded",
					finishedAt: new Date(Date.now() - 125_000).toISOString(),
				},
			},
			...over,
		});
	};

	test("reads Scheduled with the next and the last run, and counts no replicas", async () => {
		await card(cronCard());
		expect(screen.getAllByText("Scheduled").length).toBeGreaterThan(0);
		expect(screen.getByText("next in 1m · last ok 2m ago")).toBeInTheDocument();
		expect(screen.queryByText(/replica/)).not.toBeInTheDocument();
	});

	test("never shows a memory row, whatever a stale sample says", async () => {
		const { api } = await card(cronCard({ observed: makeObserved() }), {
			routes: { "GET /services/:serviceId/metrics": METRICS([POINT]) },
		});
		await waitFor(() =>
			expect(api.calls.some((c) => c.path.endsWith("/metrics"))).toBe(true),
		);
		// Let the sample land before looking: it must not draw a row.
		await act(async () => {
			await new Promise((resolve) => setTimeout(resolve, 50));
		});
		expect(screen.queryByText("Memory")).not.toBeInTheDocument();
	});

	test("a paused one reads Paused and a failed last run says so", async () => {
		const paused = await card(cronCard({ desiredState: "stopped" }));
		expect(screen.getAllByText("Paused").length).toBeGreaterThan(0);
		paused.unmount();
		const base = cronCard();
		await card(
			cronCard({
				cron: {
					...(base.cron ?? { taskId: "t", schedule: "", timezone: "UTC" }),
					lastRun: { id: "run-2", status: "failed" },
				},
			}),
		);
		expect(screen.getAllByText("Last run failed").length).toBeGreaterThan(0);
	});
});
