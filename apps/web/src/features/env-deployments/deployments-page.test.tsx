import { describe, expect, test } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { EnvDeploymentView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	type DeploymentFilterChange,
	DeploymentsPage,
} from "./deployments-page";

const env = make("Environment", {
	id: "env1",
	projectId: "p1",
	slug: "prod",
	name: "Production",
	kind: "standard",
	isProduction: true,
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
	createdAt: "2026-01-01T00:00:00Z",
});

/** The wire shape of a service, with only what the page needs set. */
const serviceView = (id: string, name: string) =>
	make("ServiceView", {
		id,
		environmentId: "env1",
		projectId: "p1",
		name,
		slug: name,
		kind: "app",
		sourceType: "image",
		config: { healthcheck: { type: "http" }, processes: [] },
		serverId: "",
		server: null,
		generation: 1,
		appliedGeneration: 1,
		hasStagedChanges: false,
		currentDeploymentId: "",
		latestDeployment: null,
		currentRelease: null,
		observed: null,
		observedAt: null,
		stale: false,
		domains: [],
		privateHost: "",
		gitTokenSet: false,
		registryAuthSet: false,
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		desiredState: "running",
		volumes: [],
		scaling: {
			autoscaling: {},
			blockedReason: null,
			eligible: true,
			ineligibleReason: null,
			lastScaleEvent: null,
			ready: 1,
			replicas: 1,
		},
		processes: [],
		pendingChangeCounts: {},
		stagedKind: "",
		maintenance: {},
		git: null,
		tailnet: null,
		networks: [],
	});

const dep = (id: string, patch: Overrides<EnvDeploymentView> = {}) =>
	make("EnvDeploymentView", {
		id,
		serviceId: "s1",
		serviceName: "api",
		serviceSlug: "api",
		status: "succeeded",
		trigger: "manual",
		strategy: "rolling",
		actorType: "user",
		actorLabel: "Ada",
		message: "Ship it",
		statusMessage: "",
		generation: 1,
		createdAt: "2026-10-29T10:00:00Z",
		startedAt: "2026-10-29T10:00:00Z",
		finishedAt: "2026-10-29T10:02:05Z",
		release: null,
		releaseId: "",
		serverId: "",
		buildServerId: "",
		rolledBackFrom: "",
		build: null,
		commit: null,
		waitingFor: null,
		cancelRequestedAt: null,
		cancelRequestedBy: "",
		canceledAtStep: "",
		...patch,
	});

function baseRoutes(items: EnvDeploymentView[] = [dep("d1")], extra = {}) {
	return {
		"GET /projects/:id/environments": {
			body: make("PageEnvironment", { items: [env] }),
		},
		"GET /environments/:id/services": {
			body: make("PageServiceView", {
				items: [serviceView("s1", "api"), serviceView("s2", "worker")],
			}),
		},
		"GET /environments/:id/deployments": {
			body: make("PageEnvDeploymentView", { items }),
		},
		"GET /environments/:id/deployments/daily": { body: { days: [] } },
		...extra,
	};
}

interface Props {
	status?: string;
	serviceId?: string;
	buildsOnly?: boolean;
	changes?: DeploymentFilterChange[];
}

function Where() {
	const location = useRouterState({ select: (s) => s.location });
	return (
		<output aria-label="where">
			{location.pathname}
			{location.searchStr}
		</output>
	);
}

const render = ({
	status,
	serviceId,
	buildsOnly = false,
	changes = [],
}: Props = {}) =>
	renderWithProviders(
		<>
			<DeploymentsPage
				projectId="p1"
				envSlug="prod"
				status={status}
				serviceId={serviceId}
				buildsOnly={buildsOnly}
				onFilterChange={(next) => changes.push(next)}
			/>
			<Where />
		</>,
	);

const listCalls = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((c) => c.path === "/environments/env1/deployments");

describe("DeploymentsPage", () => {
	test("titles the page and describes the environment", async () => {
		stubApi(baseRoutes());
		await render();
		expect(
			screen.getByRole("heading", { name: "Deployments", level: 1 }),
		).toBeInTheDocument();
		expect(
			await screen.findByText(
				"Every release of every service in Production, newest first.",
			),
		).toBeInTheDocument();
	});

	test("lists a deployment with its status, service, note, trigger, actor and duration", async () => {
		stubApi(baseRoutes());
		await render();
		const row = (await screen.findByText("Ship it")).closest(
			"tr",
		) as HTMLElement;
		expect(within(row).getByText("Succeeded")).toBeInTheDocument();
		expect(within(row).getByRole("link", { name: "api" })).toBeInTheDocument();
		expect(within(row).getByText("Deploy")).toBeInTheDocument();
		expect(within(row).getByText("Ada")).toBeInTheDocument();
		expect(within(row).getByText("2m 05s")).toBeInTheDocument();
	});

	test("a service-token actor reads 'type · label'", async () => {
		stubApi(
			baseRoutes([dep("d1", { actorType: "token", actorLabel: "gate" })]),
		);
		await render();
		expect(await screen.findByText("token · gate")).toBeInTheDocument();
	});

	test("an in-flight deployment says 'running' instead of a finish time", async () => {
		stubApi(
			baseRoutes([
				dep("d1", { status: "building", finishedAt: null, message: null }),
			]),
		);
		await render();
		const row = (await screen.findByText("Building")).closest(
			"tr",
		) as HTMLElement;
		expect(within(row).getByText("running")).toBeInTheDocument();
	});

	test("a failed deployment shows why instead of a note", async () => {
		stubApi(
			baseRoutes([
				dep("d1", {
					status: "failed",
					message: null,
					statusMessage: "health check timed out",
				}),
			]),
		);
		await render();
		const row = (await screen.findByText("Failed")).closest(
			"tr",
		) as HTMLElement;
		expect(within(row).getByText("health check timed out")).toBeInTheDocument();
	});

	test("a row links to the service's Deployments tab, and a click on the row does the same", async () => {
		stubApi(baseRoutes());
		const user = userEvent.setup();
		await render();
		const link = await screen.findByRole("link", { name: "api" });
		expect(link.getAttribute("href")).toBe(
			"/p/p1/prod?service=s1&tab=deployments",
		);
		await user.click(screen.getByText("Ada"));
		expect(await screen.findByLabelText("where")).toHaveTextContent(
			"/p/p1/prod?service=s1&tab=deployments",
		);
	});

	test("asks for 50 deployments with no filter by default", async () => {
		const api = stubApi(baseRoutes());
		await render();
		await screen.findByText("Ship it");
		const call = listCalls(api)[0];
		expect(call?.query.get("limit")).toBe("50");
		expect(call?.query.has("status")).toBe(false);
		expect(call?.query.has("serviceId")).toBe(false);
	});

	test("the status and service filters go to the request", async () => {
		const api = stubApi(baseRoutes());
		await render({ status: "failed", serviceId: "s2" });
		await screen.findByText("Ship it");
		const call = listCalls(api)[0];
		expect(call?.query.get("status")).toBe("failed,rollback_failed");
		expect(call?.query.get("serviceId")).toBe("s2");
	});

	test("an unknown status in the URL counts as all statuses", async () => {
		const api = stubApi(baseRoutes());
		await render({ status: "bogus" });
		await screen.findByText("Ship it");
		expect(listCalls(api)[0]?.query.has("status")).toBe(false);
		expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
	});

	test("an empty environment says nothing is deployed yet", async () => {
		stubApi(baseRoutes([]));
		await render();
		expect(await screen.findByText("Nothing deployed yet")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
	});

	test("an empty filtered list says no deployment matches and clears every filter", async () => {
		stubApi(baseRoutes([]));
		const changes: DeploymentFilterChange[] = [];
		const user = userEvent.setup();
		await render({ status: "failed", changes });
		expect(
			await screen.findByText("No deployment matches"),
		).toBeInTheDocument();
		const clear = screen.getAllByRole("button", { name: "Clear filters" });
		expect(clear).toHaveLength(2);
		await user.click(clear[1] as HTMLElement);
		expect(changes).toEqual([
			{ status: undefined, service: undefined, type: undefined },
		]);
	});

	test("the toolbar's Clear filters resets all three filters", async () => {
		stubApi(baseRoutes());
		const changes: DeploymentFilterChange[] = [];
		const user = userEvent.setup();
		await render({ serviceId: "s1", buildsOnly: true, changes });
		// The toolbar button comes first; the narrowed-to-nothing list has its own.
		const [toolbar] = await screen.findAllByRole("button", {
			name: "Clear filters",
		});
		await user.click(toolbar as HTMLElement);
		expect(changes.at(-1)).toEqual({
			status: undefined,
			service: undefined,
			type: undefined,
		});
	});

	test("choosing a status reports it; choosing All statuses reports undefined", async () => {
		stubApi(baseRoutes());
		const changes: DeploymentFilterChange[] = [];
		const user = userEvent.setup();
		await render({ changes });
		await screen.findByText("Ship it");
		await user.click(
			screen.getByRole("combobox", { name: "Filter by status" }),
		);
		await user.click(
			await screen.findByRole("option", { name: "Rolled back" }),
		);
		expect(changes).toEqual([{ status: "rolled_back" }]);
	});

	test("choosing a service reports its id, and the options are the environment's services", async () => {
		stubApi(baseRoutes());
		const changes: DeploymentFilterChange[] = [];
		const user = userEvent.setup();
		await render({ changes });
		await screen.findByText("Ship it");
		await user.click(
			screen.getByRole("combobox", { name: "Filter by service" }),
		);
		const options = (await screen.findAllByRole("option")).map(
			(o) => o.textContent,
		);
		expect(options).toEqual(["All services", "api", "worker"]);
		await user.click(screen.getByRole("option", { name: "worker" }));
		expect(changes).toEqual([{ service: "s2" }]);
	});

	test("choosing Builds reports the type filter", async () => {
		stubApi(baseRoutes());
		const changes: DeploymentFilterChange[] = [];
		const user = userEvent.setup();
		await render({ changes });
		await screen.findByText("Ship it");
		await user.click(screen.getByRole("combobox", { name: "Filter by type" }));
		await user.click(await screen.findByRole("option", { name: "Builds" }));
		expect(changes).toEqual([{ type: "builds" }]);
	});

	test("with Builds on, only deployments that built an image are listed", async () => {
		stubApi(
			baseRoutes([
				dep("d1", { message: "built one", build: { method: "dockerfile" } }),
				dep("d2", { message: "plain redeploy" }),
			]),
		);
		await render({ buildsOnly: true });
		expect(await screen.findByText("built one")).toBeInTheDocument();
		expect(screen.queryByText("plain redeploy")).toBeNull();
	});

	test("with Builds on and none on this page, older pages are fetched until one is found", async () => {
		const api = stubApi(
			baseRoutes([], {
				"GET /environments/:id/deployments": (request: Request) => {
					const cursor = new URL(request.url).searchParams.get("cursor");
					return Response.json(
						cursor
							? {
									items: [
										dep("d9", {
											message: "old build",
											build: { method: "buildpack" },
										}),
									],
								}
							: {
									items: [dep("d1", { message: "plain" })],
									nextCursor: "c2",
								},
					);
				},
			}),
		);
		await render({ buildsOnly: true });
		expect(await screen.findByText("old build")).toBeInTheDocument();
		expect(screen.queryByText("plain")).toBeNull();
		expect(listCalls(api).map((c) => c.query.get("cursor"))).toEqual([
			null,
			"c2",
		]);
	});

	test("Load older deployments fetches the next cursor page and appends it", async () => {
		const api = stubApi(
			baseRoutes([], {
				"GET /environments/:id/deployments": (request: Request) => {
					const cursor = new URL(request.url).searchParams.get("cursor");
					return Response.json(
						cursor
							? { items: [dep("d2", { message: "second page" })] }
							: {
									items: [dep("d1", { message: "first page" })],
									nextCursor: "c2",
								},
					);
				},
			}),
		);
		const user = userEvent.setup();
		await render();
		await screen.findByText("first page");
		await user.click(
			screen.getByRole("button", { name: "Load older deployments" }),
		);
		expect(await screen.findByText("second page")).toBeInTheDocument();
		expect(screen.getByText("first page")).toBeInTheDocument();
		expect(listCalls(api)[1]?.query.get("cursor")).toBe("c2");
		expect(
			screen.queryByRole("button", { name: "Load older deployments" }),
		).toBeNull();
	});

	test("the last page has no Load older button", async () => {
		stubApi(baseRoutes());
		await render();
		await screen.findByText("Ship it");
		expect(
			screen.queryByRole("button", { name: "Load older deployments" }),
		).toBeNull();
	});

	test("shows the server's reason when deployments did not load, and retry recovers", async () => {
		let attempts = 0;
		stubApi(
			baseRoutes([], {
				"GET /environments/:id/deployments": () => {
					attempts += 1;
					return attempts === 1
						? Response.json(
								{
									status: 500,
									title: "x",
									detail: "db unavailable",
									code: "internal",
								},
								{ status: 500 },
							)
						: Response.json({ items: [dep("d1", { message: "back again" })] });
				},
			}),
		);
		const user = userEvent.setup();
		await render();
		expect(
			await screen.findByText("Deployments did not load"),
		).toBeInTheDocument();
		expect(screen.getByText(/db unavailable/)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByText("back again")).toBeInTheDocument();
	});

	test("an unknown environment says so", async () => {
		stubApi(baseRoutes());
		await renderWithProviders(
			<DeploymentsPage
				projectId="p1"
				envSlug="nope"
				status={undefined}
				serviceId={undefined}
				buildsOnly={false}
				onFilterChange={() => undefined}
			/>,
		);
		expect(
			await screen.findByText("Environment not found"),
		).toBeInTheDocument();
		expect(
			screen.getByText("This project has no environment called “nope”."),
		).toBeInTheDocument();
	});

	test("a project that did not load shows the error with a retry", async () => {
		stubApi({
			"GET /projects/:id/environments": {
				status: 500,
				body: { status: 500, title: "x", detail: "no db", code: "internal" },
			},
		});
		await render();
		expect(
			await screen.findByText("Couldn't load this project"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /try again|retry/i }),
		).toBeInTheDocument();
	});
});
