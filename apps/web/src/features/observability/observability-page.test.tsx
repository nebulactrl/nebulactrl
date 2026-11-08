import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DeploymentRef, ServiceView } from "#/api/gen/types.gen";
import { serviceWire } from "#/features/canvas/lib/wire-fixtures";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { ObservabilityPage } from "./observability-page";

const env = {
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
};

const serviceView = (
	id: string,
	name: string,
	patch: Partial<ServiceView> = {},
) =>
	serviceWire({
		id,
		environmentId: "env1",
		projectId: "p1",
		name,
		slug: name,
		config: {
			healthcheck: {
				type: "http",
				intervalSeconds: 0,
				timeoutSeconds: 0,
				startPeriodSeconds: 0,
				deadlineSeconds: 0,
				successThreshold: 1,
			},
			imageRetention: 5,
			stopGraceSeconds: 30,
			processes: [],
		},
		...patch,
	});

const failedDeployment: DeploymentRef = {
	id: "d1",
	status: "failed",
	trigger: "manual",
	strategy: "rolling",
	statusMessage: "",
	createdAt: "2026-10-29T10:00:00Z",
	finishedAt: "2026-10-29T10:01:00Z",
	releaseId: "",
	failedStep: null,
	currentStep: null,
	message: null,
};

const uptimeItem = (name: string, state: string, patch = {}) => ({
	serviceId: `id-${name}`,
	slug: name,
	name,
	kind: "app",
	state,
	monitored: true,
	availability30d: 100,
	days: [],
	lastCheckAt: "2026-10-29T10:00:00Z",
	lastError: "",
	latencyMs: 10,
	since: null,
	...patch,
});

const problem = (status: number, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code: "internal" },
});

function routes(
	overrides: Record<string, unknown> = {},
	services = [serviceView("s1", "api"), serviceView("s2", "worker")],
) {
	return {
		"GET /projects/:id/environments": { body: { items: [env] } },
		"GET /environments/:id/services": { body: { items: services } },
		"GET /environments/:id/uptime": {
			body: { items: [uptimeItem("api", "up"), uptimeItem("worker", "up")] },
		},
		"GET /analytics/traffic": problem(500, "traffic store down"),
		...overrides,
	} as Parameters<typeof stubApi>[0];
}

const render = () =>
	renderWithProviders(<ObservabilityPage projectId="p1" envSlug="prod" />);

const section = (name: string) =>
	screen.getByRole("heading", { level: 2, name: new RegExp(`^${name}`) });

describe("ObservabilityPage", () => {
	test("titles the page and describes the environment", async () => {
		stubApi(routes());
		await render();
		expect(
			screen.getByRole("heading", { level: 1, name: "Observability" }),
		).toBeInTheDocument();
		expect(
			await screen.findByText(
				"Uptime, traffic and resource use of Production.",
			),
		).toBeInTheDocument();
	});

	test("shows a loading placeholder until the services arrive", async () => {
		stubApi(
			routes({
				"GET /environments/:id/services": () =>
					new Promise<Response>(() => undefined),
			}),
		);
		await render();
		// The environment resolves first; the page's own placeholder follows.
		await screen.findByText("Uptime, traffic and resource use of Production.");
		expect(
			screen.getByRole("status", { name: "Loading observability" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("heading", { level: 2 })).toBeNull();
	});

	test("lays out Uptime, Traffic and Services, in that order", async () => {
		stubApi(routes());
		await render();
		await screen.findByRole("link", { name: "worker" });
		const headings = screen
			.getAllByRole("heading", { level: 2 })
			.map((h) => h.textContent ?? "");
		expect(headings).toHaveLength(3);
		expect(headings[0]).toStartWith("Uptime");
		expect(headings[1]).toStartWith("Traffic");
		expect(headings[2]).toStartWith("Services");
		expect(section("Traffic")).toHaveTextContent(
			"Requests through the proxies of this environment's services.",
		);
	});

	test("no longer renders the log and links to the Logs page instead", async () => {
		stubApi(routes());
		await render();
		await screen.findByRole("link", { name: "worker" });
		expect(screen.queryByRole("log")).toBeNull();
		expect(screen.queryByLabelText("Search environment logs")).toBeNull();
		expect(screen.getByRole("button", { name: "Open logs" })).toHaveAttribute(
			"href",
			"/p/p1/prod/logs",
		);
	});

	test("the Services section counts services and says none are failing", async () => {
		stubApi(routes());
		await render();
		await screen.findByRole("link", { name: "worker" });
		expect(section("Services")).toHaveTextContent("2 services, none failing");
	});

	test("it counts a failed service and uses the singular for one", async () => {
		stubApi(
			routes({}, [
				serviceView("s1", "api", { latestDeployment: failedDeployment }),
			]),
		);
		await render();
		await waitFor(() =>
			expect(section("Services")).toHaveTextContent("1 service, 1 failing"),
		);
	});

	test("a resource card per service sits in the Services section", async () => {
		stubApi(routes());
		await render();
		await screen.findByText("2 services, none failing");
		const services = section("Services").closest("section") as HTMLElement;
		expect(
			within(services)
				.getAllByRole("link")
				.map((a) => a.textContent),
		).toEqual(["api", "worker"]);
		expect(within(services).getAllByText("image")).toHaveLength(2);
	});

	test("Uptime lists the environment's services and a calm note when all is well", async () => {
		stubApi(routes());
		await render();
		expect(
			await screen.findByText(
				"Checked every 30 seconds from each service's server.",
			),
		).toBeInTheDocument();
		expect(screen.getAllByText("Up").length).toBeGreaterThanOrEqual(2);
	});

	test("Uptime counts down and degraded services in its note", async () => {
		stubApi(
			routes({
				"GET /environments/:id/uptime": {
					body: {
						items: [
							uptimeItem("api", "down", { lastError: "HTTP 502" }),
							uptimeItem("worker", "degraded", { lastError: "slow" }),
						],
					},
				},
			}),
		);
		await render();
		expect(await screen.findByText("2 down or degraded")).toBeInTheDocument();
		expect(screen.getByText("HTTP 502")).toBeInTheDocument();
	});

	test("an uptime failure shows an error with a retry and leaves the rest of the page", async () => {
		let attempts = 0;
		stubApi(
			routes({
				"GET /environments/:id/uptime": () => {
					attempts += 1;
					return attempts === 1
						? Response.json(
								{ status: 500, title: "x", detail: "boom", code: "internal" },
								{ status: 500 },
							)
						: Response.json({ items: [uptimeItem("api", "up")] });
				},
			}),
		);
		const user = userEvent.setup();
		await render();
		expect(
			await screen.findByText(
				"Uptime did not load. The rest of the page is unaffected.",
			),
		).toBeInTheDocument();
		expect(section("Services")).toBeInTheDocument();
		const failed = screen
			.getByText("Uptime did not load. The rest of the page is unaffected.")
			.closest("section") as HTMLElement;
		await user.click(within(failed).getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByText(
				"Checked every 30 seconds from each service's server.",
			),
		).toBeInTheDocument();
		expect(attempts).toBe(2);
	});

	test("asks for uptime and services, and no longer opens the log stream", async () => {
		const api = stubApi(routes());
		await render();
		await screen.findByRole("link", { name: "worker" });
		const paths = api.calls.map((c) => c.path);
		expect(paths).toContain("/environments/env1/services");
		expect(paths).toContain("/environments/env1/uptime");
		expect(paths).not.toContain("/environments/env1/logs");
	});

	test("an environment with no services offers to open the canvas", async () => {
		stubApi(routes({}, []));
		await render();
		expect(
			await screen.findByText("Create a service to see it here"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Resource use and traffic of Production appear here once it has a deployed service.",
			),
		).toBeInTheDocument();
		const open = screen.getByRole("button", { name: "Open the canvas" });
		expect(open.getAttribute("href")).toBe("/p/p1/prod");
		expect(screen.getByText("How monitoring works")).toBeInTheDocument();
		// Nothing below the empty state is mounted: no sections, no logs link.
		expect(screen.queryByRole("heading", { level: 2 })).toBeNull();
		expect(screen.queryByRole("button", { name: "Open logs" })).toBeNull();
	});

	test("services that did not load say so and Try again fetches them", async () => {
		let attempts = 0;
		stubApi(
			routes({
				"GET /environments/:id/services": () => {
					attempts += 1;
					return attempts === 1
						? Response.json(
								{
									status: 500,
									title: "x",
									detail: "db down",
									code: "internal",
								},
								{ status: 500 },
							)
						: Response.json({ items: [serviceView("s1", "api")] });
				},
			}),
		);
		const user = userEvent.setup();
		await render();
		expect(
			await screen.findByText("Services did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(
			await screen.findByText("1 service, none failing"),
		).toBeInTheDocument();
		expect(screen.queryByText("Services did not load")).toBeNull();
	});

	test("an unknown environment says so instead of the sections", async () => {
		stubApi(routes());
		await renderWithProviders(
			<ObservabilityPage projectId="p1" envSlug="nope" />,
		);
		expect(
			await screen.findByText("Environment not found"),
		).toBeInTheDocument();
		expect(screen.queryByRole("heading", { level: 2 })).toBeNull();
	});

	test("a project that did not load shows the error", async () => {
		stubApi({
			"GET /projects/:id/environments": problem(500, "no db"),
		});
		await render();
		expect(
			await screen.findByText("Couldn't load this project"),
		).toBeInTheDocument();
	});
});
