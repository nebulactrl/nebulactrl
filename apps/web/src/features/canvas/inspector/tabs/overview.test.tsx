import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import { stubApi } from "#/test/api";
import type { ApiSchemas } from "#/test/api-schemas.gen";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeDeployment, makeObserved, makeService } from "../../lib/fixtures";
import { InspectorContext } from "../inspector-context";
import type { InspectorTab } from "../tab-ids";
import { OverviewTab } from "./overview";

const empty = { items: [] };
const sse = () =>
	new Response("event: end\ndata: {}\n\n", {
		headers: { "Content-Type": "text/event-stream" },
	});

const uptime = {
	serviceId: "svc-o",
	state: "unknown",
	since: null,
	range: "1h",
	granularity: "minute",
	availability: { "24h": null, "7d": null, "30d": null, "90d": null },
	consecutiveFailures: 0,
	failureThreshold: 3,
	incidents: [],
	lastCheckAt: null,
	lastError: "",
	lastOkAt: null,
	latencyMs: null,
	monitor: {
		enabled: true,
		alerts: true,
		failureThreshold: 3,
		recoveryThreshold: 2,
		reminderMinutes: 0,
	},
	series: [],
};

/** What the Overview's cards read. */
function overviewApi(extra: Parameters<typeof stubApi>[0] = {}) {
	return stubApi({
		"GET /services/:serviceId/uptime": { body: uptime },
		"GET /services/:serviceId/incidents": { body: empty },
		"GET /services/:serviceId/deployments": { body: empty },
		"GET /services/:serviceId/variables": {
			body: make("VariableList", { items: [] }),
		},
		"GET /services/:serviceId/metrics": {
			body: make("MetricsOutputBody", { points: [] }),
		},
		"GET /services/:serviceId/logs": sse,
		...extra,
	});
}

const deployed = (overrides: Partial<Service> = {}) =>
	makeService({
		id: "svc-o",
		name: "api",
		currentDeploymentId: "dep-1",
		latestDeployment: makeDeployment("succeeded", { id: "dep-1" }),
		observed: makeObserved(),
		...overrides,
	});

function renderOverview(service: Service, opened: InspectorTab[] = []) {
	return renderWithProviders(
		<InspectorContext.Provider
			value={{ close: () => {}, openTab: (tab) => opened.push(tab) }}
		>
			<OverviewTab
				service={service}
				section={undefined}
				onSectionChange={() => {}}
			/>
		</InspectorContext.Provider>,
	);
}

const deploymentWire = (
	status: string,
	overrides: Overrides<ApiSchemas["DeploymentView"]> = {},
) =>
	make("DeploymentView", {
		id: "dep-1",
		serviceId: "svc-o",
		status,
		...overrides,
	});

describe("OverviewTab layout", () => {
	test("is the Overview section of the service with its cards under headings", async () => {
		overviewApi();
		await renderOverview(deployed());
		const overview = await screen.findByRole("region", { name: "Overview" });
		expect(overview).toHaveAttribute("data-tab", "overview");
		expect(overview).toHaveAttribute("data-service-id", "svc-o");
		for (const title of [
			"Health",
			"Resources",
			"Scaling",
			"Latest deployments",
			"Recent logs",
			"Variables",
		]) {
			expect(
				within(overview).getByRole("region", { name: title }),
			).toBeInTheDocument();
		}
		expect(
			await screen.findByRole("region", { name: "Uptime" }),
		).toBeInTheDocument();
	});

	test("the section links open the tab that goes deeper", async () => {
		overviewApi();
		const opened: InspectorTab[] = [];
		await renderOverview(deployed(), opened);
		const user = userEvent.setup();
		const region = (name: string) => screen.getByRole("region", { name });
		await user.click(
			within(region("Resources")).getByRole("button", { name: "Metrics" }),
		);
		await user.click(
			within(region("Latest deployments")).getByRole("button", {
				name: "View all",
			}),
		);
		await user.click(
			within(region("Recent logs")).getByRole("button", { name: "Open" }),
		);
		await user.click(
			within(region("Variables")).getByRole("button", { name: "Open" }),
		);
		expect(opened).toEqual(["metrics", "deployments", "logs", "variables"]);
	});

	test("a service with no staged changes shows no alert", async () => {
		overviewApi();
		await renderOverview(deployed());
		await screen.findByRole("region", { name: "Overview" });
		expect(screen.queryByText(/Staged changes/)).toBeNull();
	});

	test("without processes it has no Processes or Components section", async () => {
		overviewApi();
		await renderOverview(deployed());
		await screen.findByRole("region", { name: "Overview" });
		expect(screen.queryByRole("region", { name: "Processes" })).toBeNull();
		expect(screen.queryByRole("region", { name: "Components" })).toBeNull();
	});
});

describe("OverviewTab staged changes", () => {
	test("lists what the next deploy will apply, counting them", async () => {
		const api = overviewApi({
			"GET /services/:serviceId/pending-changes": {
				body: {
					items: [
						{ field: "config.port", from: "80", to: "8080" },
						{ field: "variables.API_KEY", from: "old", to: "" },
					],
				},
			},
		});
		await renderOverview(deployed({ hasStagedChanges: true }));
		expect(await screen.findByText("Staged changes (2)")).toBeInTheDocument();
		expect(
			screen.getByText(/These apply the next time api is deployed/),
		).toBeInTheDocument();
		const alert = screen.getByRole("status");
		const items = within(alert).getAllByRole("listitem");
		expect(items[0]).toHaveTextContent("~ config.port → 8080");
		expect(items[0]).toHaveAttribute("title", "config.port: 80 → 8080");
		// A removed value reads as removed, not as an empty string.
		expect(items[1]).toHaveTextContent("~ variables.API_KEY → (removed)");
		expect(
			api.calls.some((c) => c.path === "/services/svc-o/pending-changes"),
		).toBe(true);
	});

	test("shows the first four changes only", async () => {
		overviewApi({
			"GET /services/:serviceId/pending-changes": {
				body: {
					items: ["a", "b", "c", "d", "e"].map((f) => ({
						field: `config.${f}`,
						from: "1",
						to: "2",
					})),
				},
			},
		});
		await renderOverview(deployed({ hasStagedChanges: true }));
		expect(await screen.findByText("Staged changes (5)")).toBeInTheDocument();
		expect(
			within(screen.getByRole("status")).getAllByRole("listitem"),
		).toHaveLength(4);
	});

	test("a compose change is summarised, not dumped", async () => {
		overviewApi({
			"GET /services/:serviceId/pending-changes": {
				body: {
					items: [
						{
							field: "config.stack.compose",
							from: "services:\n  web:\n    image: a\n",
							to: "services:\n  web:\n    image: b\n  db:\n    image: c\n",
						},
					],
				},
			},
		});
		await renderOverview(deployed({ hasStagedChanges: true }));
		const item = await screen.findByRole("listitem");
		expect(item).toHaveTextContent("compose file: +3 −1 lines");
		expect(item).not.toHaveTextContent("image: a");
	});

	test("while the list loads the alert is there without a count", async () => {
		overviewApi({
			"GET /services/:serviceId/pending-changes": () => new Promise(() => {}),
		});
		await renderOverview(deployed({ hasStagedChanges: true }));
		expect(await screen.findByText("Staged changes")).toBeInTheDocument();
	});

	test("when the server lists none the alert goes away", async () => {
		const api = overviewApi({
			"GET /services/:serviceId/pending-changes": { body: empty },
		});
		await renderOverview(deployed({ hasStagedChanges: true }));
		await waitFor(() =>
			expect(api.calls.some((c) => c.path.endsWith("/pending-changes"))).toBe(
				true,
			),
		);
		await waitFor(() =>
			expect(screen.queryByText(/Staged changes/)).toBeNull(),
		);
	});

	test("a service without staged changes never asks", async () => {
		const api = overviewApi();
		await renderOverview(deployed({ hasStagedChanges: false }));
		await screen.findByRole("region", { name: "Overview" });
		expect(api.calls.some((c) => c.path.endsWith("/pending-changes"))).toBe(
			false,
		);
	});
});

describe("OverviewTab recovery banner", () => {
	test("a failed rollback raises the serious banner", async () => {
		overviewApi({
			"GET /deployments/:deploymentId": {
				body: deploymentWire("rollback_failed"),
			},
		});
		await renderOverview(
			deployed({
				latestDeployment: makeDeployment("rollback_failed", { id: "dep-1" }),
			}),
		);
		expect(
			await screen.findByText("Rollback failed: api may not be serving"),
		).toBeInTheDocument();
	});

	test("a deployment waiting on an offline server says so", async () => {
		overviewApi({
			"GET /deployments/:deploymentId": {
				body: deploymentWire("queued", {
					waitingFor: {
						serverId: "s1",
						serverName: "hetzner-01",
						offlineSince: "2026-10-31T09:00:00Z",
					},
				}),
			},
		});
		await renderOverview(
			deployed({ latestDeployment: makeDeployment("queued", { id: "dep-1" }) }),
		);
		expect(
			await screen.findByText(/Waiting for hetzner-01/),
		).toBeInTheDocument();
	});

	test("with no deployment yet it asks for none", async () => {
		const api = overviewApi();
		await renderOverview(makeService({ id: "svc-o", name: "api" }));
		await screen.findByRole("region", { name: "Overview" });
		expect(api.calls.some((c) => c.path.startsWith("/deployments/"))).toBe(
			false,
		);
	});
});

describe("OverviewTab by kind", () => {
	test("a stack lists its components, relabels Variables and has no Scaling card", async () => {
		overviewApi();
		await renderOverview(
			deployed({
				kind: "stack",
				config: {
					...makeService().config,
					stack: {
						components: [{ name: "web", image: "nginx" }],
					} as never,
				},
			}),
		);
		expect(
			await screen.findByRole("region", { name: "Components" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("region", { name: "Variables for interpolation" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("region", { name: "Scaling" })).toBeNull();
		expect(screen.queryByRole("region", { name: "Variables" })).toBeNull();
	});

	test("a service with processes lists them", async () => {
		overviewApi();
		await renderOverview(
			deployed({
				processes: [
					{
						name: "web",
						default: true,
						command: [],
						port: 8080,
						stopGraceSeconds: 30,
						pending: false,
						removed: false,
						singleton: false,
						observed: {} as never,
						scaling: makeService().scaling,
					},
				],
			}),
		);
		const processes = await screen.findByRole("region", { name: "Processes" });
		expect(within(processes).getByText("web")).toBeInTheDocument();
	});

	test("a managed database shows its section, and opens the connection variables from it", async () => {
		overviewApi({
			"GET /services/:serviceId/database": problem(500, { title: "boom" }),
		});
		await renderOverview(
			deployed({
				kind: "database",
				config: {
					...makeService().config,
					database: { engine: "postgres" } as never,
				},
			}),
		);
		expect(
			await screen.findByText("The database details did not load"),
		).toBeInTheDocument();
	});
});
