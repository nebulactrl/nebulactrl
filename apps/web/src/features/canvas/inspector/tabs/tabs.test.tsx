import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import type { Service } from "#/api/models";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeObserved, makeService } from "../../lib/fixtures";
import { InspectorContext } from "../inspector-context";
import type { InspectorTab } from "../tab-ids";
import { AnalyticsTab } from "./analytics";
import { BackupsTab } from "./backups";
import { DeploymentsTab } from "./deployments";
import { LogsTab } from "./logs";
import { MetricsTab } from "./metrics";
import { SettingsTab } from "./settings";
import { VariablesTab } from "./variables";

const empty = { items: [] };
const fail = problem(500, { title: "boom" });

const service = (overrides: Partial<Service> = {}) =>
	makeService({
		id: "svc-t",
		name: "api",
		slug: "api",
		currentDeploymentId: "dep-1",
		observed: makeObserved(),
		...overrides,
	});

const props = (svc: Service, onSectionChange = (_section: string) => {}) => ({
	service: svc,
	section: undefined,
	onSectionChange,
});

function host(ui: ReactNode, opened: InspectorTab[] = []) {
	return renderWithProviders(
		<InspectorContext.Provider
			value={{ close: () => {}, openTab: (tab) => opened.push(tab) }}
		>
			{ui}
		</InspectorContext.Provider>,
		{ path: "/p/p1/staging", routePath: "/p/$projectId/$envSlug" },
	);
}

const paths = (api: ReturnType<typeof stubApi>) => api.calls.map((c) => c.path);

describe("tab bodies of one service", () => {
	test("Metrics is a labelled section that charts this service", async () => {
		const api = stubApi({
			"GET /services/:serviceId/metrics": {
				body: make("MetricsOutputBody", { points: [] }),
			},
		});
		await host(<MetricsTab {...props(service())} />);
		const section = screen.getByRole("region", { name: "Metrics" });
		expect(section).toHaveAttribute("data-tab", "metrics");
		expect(section).toHaveAttribute("data-service-id", "svc-t");
		await waitFor(() =>
			expect(paths(api)).toContain("/services/svc-t/metrics"),
		);
	});

	test("Analytics is a labelled section that reads this service's traffic", async () => {
		const api = stubApi({
			"GET /services/:serviceId": fail,
		});
		await host(<AnalyticsTab {...props(service())} />);
		const section = screen.getByRole("region", { name: "Analytics" });
		expect(section).toHaveAttribute("data-tab", "analytics");
		expect(section).toHaveAttribute("data-service-id", "svc-t");
		await waitFor(() => expect(paths(api)).toContain("/services/svc-t"));
	});

	test("Logs tails this service's stream", async () => {
		const api = stubApi({
			"GET /services/:serviceId/logs": () =>
				new Response(
					`data: ${JSON.stringify({ line: "listening on :8080" })}\n\nevent: end\ndata: {}\n\n`,
					{ headers: { "Content-Type": "text/event-stream" } },
				),
		});
		await host(<LogsTab {...props(service())} />);
		await waitFor(() => expect(paths(api)).toContain("/services/svc-t/logs"));
		expect(
			api.calls.find((c) => c.path.endsWith("/logs"))?.query.get("follow"),
		).toBe("true");
	});

	test("Deployments lists this service's history", async () => {
		const api = stubApi({
			"GET /services/:serviceId/deployments": { body: empty },
		});
		await host(<DeploymentsTab {...props(service())} />);
		await waitFor(() =>
			expect(paths(api)).toContain("/services/svc-t/deployments"),
		);
	});

	test("Variables lists this service's variables", async () => {
		const api = stubApi({
			"GET /services/:serviceId/variables": {
				body: make("VariableList", { items: [] }),
			},
		});
		await host(<VariablesTab {...props(service())} />);
		await waitFor(() =>
			expect(paths(api)).toContain("/services/svc-t/variables"),
		);
	});

	test("Settings hands the service, section and section changes to the settings panel", async () => {
		stubApi({});
		const sections: string[] = [];
		await host(
			<SettingsTab
				service={service()}
				section="deploy"
				onSectionChange={(section) => sections.push(section)}
			/>,
		);
		// The panel opens on the category the address names.
		const headings = await screen.findAllByRole("heading", { name: /Deploy/ });
		expect(headings.length).toBeGreaterThan(0);
		expect(sections).toEqual([]);
	});
});

describe("BackupsTab", () => {
	test("a managed database lists its dumps", async () => {
		const api = stubApi({
			"GET /services/:serviceId/database/replication": {
				body: {
					role: "standalone",
					replicas: [],
					canPromote: false,
					rebuildable: false,
				},
			},
			"GET /backup-destinations": { body: empty },
			"GET /services/:serviceId/backups": { body: empty },
			"GET /services/:serviceId/backup-schedules": { body: empty },
			"GET /services/:serviceId/backup-restores": { body: empty },
		});
		await host(
			<BackupsTab
				{...props(
					service({
						kind: "database",
						config: {
							...makeService().config,
							database: { engine: "postgres" } as never,
						},
					}),
				)}
			/>,
		);
		const section = screen.getByRole("region", { name: "Backups" });
		expect(section).toHaveAttribute("data-service-id", "svc-t");
		expect(
			await screen.findByRole("button", { name: "Back up now" }),
		).toBeInTheDocument();
		expect(paths(api)).toContain("/services/svc-t/backups");
		expect(paths(api)).not.toContain("/services/svc-t/volume-backups");
	});

	test("a read replica says backups run on the primary, and does not offer its own", async () => {
		stubApi({
			"GET /services/:serviceId/database/replication": {
				body: {
					role: "replica",
					primary: { id: "svc-primary", slug: "main-db" },
					replicas: [],
					canPromote: false,
					rebuildable: false,
				},
			},
			"GET /backup-destinations": { body: empty },
			"GET /services/:serviceId/backups": { body: empty },
			"GET /services/:serviceId/backup-schedules": { body: empty },
			"GET /services/:serviceId/backup-restores": { body: empty },
		});
		await host(
			<BackupsTab
				{...props(
					service({
						kind: "database",
						config: {
							...makeService().config,
							database: { engine: "postgres" } as never,
						},
					}),
				)}
			/>,
		);
		expect(
			await screen.findByText("Backups run on the primary"),
		).toBeInTheDocument();
		const link = screen.getByRole("link", { name: "main-db" });
		expect(link).toHaveAttribute(
			"href",
			expect.stringContaining("service=svc-primary"),
		);
		expect(link).toHaveAttribute(
			"href",
			expect.stringContaining("tab=backups"),
		);
		// Once the replica is known, the dump panel's own controls are gone.
		expect(screen.queryByRole("button", { name: "Back up now" })).toBeNull();
	});

	test("an engine other than Postgres never asks about replication", async () => {
		const api = stubApi({
			"GET /backup-destinations": { body: empty },
			"GET /services/:serviceId/backups": { body: empty },
			"GET /services/:serviceId/backup-schedules": { body: empty },
			"GET /services/:serviceId/backup-restores": { body: empty },
		});
		await host(
			<BackupsTab
				{...props(
					service({
						kind: "database",
						config: {
							...makeService().config,
							database: { engine: "redis" } as never,
						},
					}),
				)}
			/>,
		);
		await waitFor(() =>
			expect(paths(api)).toContain("/services/svc-t/backups"),
		);
		expect(paths(api).some((p) => p.endsWith("/replication"))).toBe(false);
	});

	test("an app with a volume gets volume backups", async () => {
		const api = stubApi({
			"GET /services/:serviceId/volume-backups": { body: empty },
			"GET /backup-destinations": { body: empty },
		});
		await host(
			<BackupsTab
				{...props(
					service({ volumes: [{ name: "data", path: "/data" } as never] }),
				)}
			/>,
		);
		await waitFor(() =>
			expect(paths(api)).toContain("/services/svc-t/volume-backups"),
		);
		expect(paths(api)).not.toContain("/services/svc-t/backups");
	});

	test("a service with neither renders nothing", async () => {
		const api = stubApi({});
		const view = await host(<BackupsTab {...props(service())} />);
		expect(within(view.container).queryByRole("region")).toBeNull();
		expect(api.calls).toEqual([]);
	});
});
