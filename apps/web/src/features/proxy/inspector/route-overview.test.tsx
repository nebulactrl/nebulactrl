import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ServiceView } from "#/api/gen/types.gen";
import { toService } from "#/api/models";
import {
	type InspectorActions,
	InspectorContext,
} from "#/features/canvas/inspector/inspector-context";
import type { InspectorTab } from "#/features/canvas/inspector/tab-ids";
import { planWire } from "#/features/deployments/test-fixtures";
import { type ApiReply, type ApiRoutes, stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { UDP_WARNING } from "../lib/l4";
import { domainView, routeView } from "./route-fixtures";
import { RouteOverviewTab } from "./route-overview";

const SERVICES = "GET /environments/:environmentId/services";
const DEPLOYMENTS = "GET /services/:serviceId/deployments";
const PLAN = "POST /services/:serviceId/deployments/plan";
const PENDING = "GET /services/:serviceId/pending-changes";

const emptyPlan = planWire({ strategy: "route_only" });

interface Setup {
	siblings?: ServiceView[];
	plan?: ApiReply;
	pending?: ApiReply;
	extra?: ApiRoutes;
}

async function renderOverview(view: ServiceView, setup: Setup = {}) {
	const opened: InspectorTab[] = [];
	const actions: InspectorActions = {
		close: () => {},
		openTab: (tab) => opened.push(tab),
	};
	const api = stubApi({
		[SERVICES]: {
			body: make("PageServiceView", { items: setup.siblings ?? [view] }),
		},
		[DEPLOYMENTS]: { body: { items: [] } },
		[PLAN]: setup.plan ?? { body: emptyPlan },
		[PENDING]: setup.pending ?? { body: { items: [] } },
		...setup.extra,
	});
	const { queryClient } = await renderWithProviders(
		<InspectorContext.Provider value={actions}>
			<RouteOverviewTab
				service={toService(view)}
				section={undefined}
				onSectionChange={() => {}}
			/>
		</InspectorContext.Provider>,
	);
	await waitFor(() => expect(queryClient.isFetching()).toBe(0));
	return { api, opened };
}

const section = (name: string) => screen.getByRole("region", { name });

/** The staged-changes alert (the Copy buttons have their own status regions). */
async function stagedAlert(): Promise<HTMLElement> {
	const text = await screen.findByText(/Deploy to publish this route/);
	const alert = text.closest('[role="status"]');
	if (!(alert instanceof HTMLElement)) throw new Error("no staged alert");
	return alert;
}

describe("RouteOverviewTab: http route", () => {
	test("shows the target, scheme, server and each domain with its status", async () => {
		await renderOverview(
			routeView(
				{
					domains: [
						domainView({ id: "d1", hostname: "app.example.com" }),
						domainView({
							id: "d2",
							hostname: "old.example.com",
							pathPrefix: "/legacy",
							kind: "redirect",
							redirectTo: "https://app.example.com",
							status: "pending",
						}),
						domainView({
							id: "d3",
							hostname: "bad.example.com",
							status: "error",
						}),
					],
				},
				{ scheme: "https" },
			),
		);
		const route = within(section("Route"));
		expect(route.getByText("HTTP")).toBeInTheDocument();
		expect(route.getByText("https to the service")).toBeInTheDocument();
		expect(route.getByText("192.168.1.20:8123")).toBeInTheDocument();
		expect(route.getByText("Upstream")).toBeInTheDocument();
		expect(route.getByText("edge-1")).toBeInTheDocument();
		expect(
			route.getByRole("button", { name: "Copy 192.168.1.20:8123" }),
		).toBeInTheDocument();

		const domains = within(section("Domains"));
		const links = domains.getAllByRole("link");
		expect(links.map((link) => link.getAttribute("href"))).toEqual([
			"https://app.example.com",
			"https://old.example.com/legacy",
			"https://bad.example.com",
		]);
		const rows = domains.getAllByRole("listitem");
		expect(rows[0]).toHaveTextContent("active");
		expect(rows[1]).toHaveTextContent("→ https://app.example.com");
		expect(rows[1]).toHaveTextContent("pending");
		expect(rows[2]).toHaveTextContent("error");
	});

	test("says the route has no public address without a hostname", async () => {
		await renderOverview(routeView());
		expect(
			within(section("Domains")).getByText(/No hostname yet/),
		).toBeInTheDocument();
	});

	test("defaults the scheme to http and lists several upstreams under a plural label", async () => {
		await renderOverview(
			routeView(
				{},
				{
					scheme: undefined as never,
					upstreams: [{ address: "10.0.0.1:80" }, { address: "10.0.0.2:80" }],
				},
			),
		);
		const route = within(section("Route"));
		expect(route.getByText("http to the service")).toBeInTheDocument();
		expect(route.getByText("Upstreams")).toBeInTheDocument();
		expect(route.getByText("10.0.0.1:80")).toBeInTheDocument();
		expect(route.getByText("10.0.0.2:80")).toBeInTheDocument();
	});

	test("names a managed upstream service and its port", async () => {
		const target = routeView({ id: "svc-api", name: "api", kind: "app" });
		await renderOverview(
			routeView({}, { upstreams: [{ serviceId: "svc-api", port: 3000 }] }),
			{ siblings: [target] },
		);
		expect(await screen.findByText("api:3000")).toBeInTheDocument();
	});

	test("falls back to a generic name while the managed upstream is unknown", async () => {
		await renderOverview(
			routeView({}, { upstreams: [{ serviceId: "svc-gone", port: 3000 }] }),
		);
		expect(screen.getByText("managed service:3000")).toBeInTheDocument();
	});

	test("warns that the upstream certificate is not verified", async () => {
		await renderOverview(
			routeView({}, { scheme: "https", insecureSkipVerify: true }),
		);
		expect(
			screen.getByText("Upstream certificate is not verified"),
		).toBeInTheDocument();
	});

	test("shows no TLS row when verification is on", async () => {
		await renderOverview(routeView({}, { scheme: "https" }));
		expect(screen.queryByText("TLS")).toBeNull();
	});

	test("shows the health path only when one is set", async () => {
		await renderOverview(routeView({}, { healthPath: "/healthz" }));
		expect(screen.getByText("Health path")).toBeInTheDocument();
		expect(screen.getByText("/healthz")).toBeInTheDocument();
	});

	test("has no health path row without one", async () => {
		await renderOverview(routeView());
		expect(screen.queryByText("Health path")).toBeNull();
	});

	test("shows Not placed for a route without a server and does not ask for a plan", async () => {
		const { api } = await renderOverview(
			routeView({ serverId: "", server: null }),
		);
		expect(
			within(section("Route")).getByText("Not placed"),
		).toBeInTheDocument();
		expect(api.calls.some((call) => call.method === "POST")).toBe(false);
	});

	test("the section links open the Settings and Deployments tabs", async () => {
		const { opened } = await renderOverview(routeView());
		const user = userEvent.setup();
		await user.click(
			within(section("Route")).getByRole("button", { name: /Settings/ }),
		);
		await user.click(
			within(section("Latest deployments")).getByRole("button", {
				name: /View all/,
			}),
		);
		expect(opened).toEqual(["settings", "deployments"]);
	});

	test("says nothing has been deployed when there are no deployments", async () => {
		await renderOverview(routeView());
		expect(
			await screen.findByText("Nothing has been deployed yet."),
		).toBeInTheDocument();
	});
});

describe("RouteOverviewTab: tcp and udp routes", () => {
	test("a tcp route shows its port, its allow and deny lists and no domains", async () => {
		await renderOverview(
			routeView(
				{},
				{
					protocol: "tcp",
					listenPort: 25565,
					allowCidrs: ["10.0.0.0/8", "192.168.0.0/16"],
					denyCidrs: ["10.1.0.0/16"],
					upstreams: [{ address: "192.168.1.30:25565" }],
				},
			),
		);
		const route = within(section("Route"));
		expect(route.getByText("TCP")).toBeInTheDocument();
		expect(route.getByText("port 25565 on edge-1")).toBeInTheDocument();
		expect(route.getByText("Access")).toBeInTheDocument();
		expect(
			route.getByText("Allow 10.0.0.0/8 +1, deny 10.1.0.0/16"),
		).toBeInTheDocument();
		expect(screen.queryByRole("region", { name: "Domains" })).toBeNull();
	});

	test("an open tcp port says so", async () => {
		await renderOverview(
			routeView(
				{},
				{ protocol: "tcp", listenPort: 2222, openToInternet: true },
			),
		);
		expect(
			within(section("Route")).getByText("Open to the internet"),
		).toBeInTheDocument();
	});

	test("a udp route carries the no-IP-filter caution and no TLS row", async () => {
		await renderOverview(
			routeView(
				{},
				{
					protocol: "udp",
					listenPort: 30000,
					insecureSkipVerify: true,
					upstreams: [{ address: "10.0.0.5:19132" }],
				},
			),
		);
		const route = within(section("Route"));
		expect(route.getByText("UDP")).toBeInTheDocument();
		expect(route.getByText(UDP_WARNING)).toBeInTheDocument();
		expect(route.queryByText("TLS")).toBeNull();
	});

	test("shows a question mark for a port that is not configured yet", async () => {
		await renderOverview(
			routeView({}, { protocol: "tcp", openToInternet: true }),
		);
		expect(screen.getByText("port ? on edge-1")).toBeInTheDocument();
	});
});

describe("RouteOverviewTab: staged changes", () => {
	test("a new route is not published yet and says how many changes wait", async () => {
		await renderOverview(
			routeView({
				hasStagedChanges: true,
				stagedKind: "new",
				pendingChangeCounts: { settings: 2, variables: 0 },
			}),
			{
				pending: {
					body: {
						items: [
							{ field: "name", from: "", to: "a" },
							{ field: "config.route", from: "", to: "b" },
						],
					},
				},
			},
		);
		const alert = await stagedAlert();
		expect(alert).toHaveTextContent("Not published yet");
		await waitFor(() =>
			expect(alert).toHaveTextContent(
				"Deploy to publish this route: 2 changes waiting. No build runs: the proxy on edge-1 receives the new route table.",
			),
		);
	});

	test("a single staged change is singular on a deployed route", async () => {
		await renderOverview(
			routeView({
				hasStagedChanges: true,
				stagedKind: "edited",
				pendingChangeCounts: { settings: 1, variables: 0 },
			}),
			{ pending: { body: { items: [{ field: "name", from: "a", to: "b" }] } } },
		);
		const alert = await stagedAlert();
		expect(alert).toHaveTextContent("Staged changes");
		await waitFor(() => expect(alert).toHaveTextContent(": 1 change waiting."));
	});

	test("an unplaced staged route names the server generically", async () => {
		await renderOverview(
			routeView({
				hasStagedChanges: true,
				stagedKind: "new",
				serverId: "",
				server: null,
			}),
		);
		expect(await stagedAlert()).toHaveTextContent(
			"the proxy on the server receives the new route table",
		);
	});

	test("shows no staged alert for a clean route and does not fetch pending changes", async () => {
		const { api } = await renderOverview(routeView());
		expect(screen.queryByText(/Deploy to publish this route/)).toBeNull();
		expect(
			api.calls.some((call) => call.path.endsWith("/pending-changes")),
		).toBe(false);
	});
});

describe("RouteOverviewTab: deploy plan", () => {
	test("lists the plan's reasons, warnings and blockers before the deploy", async () => {
		await renderOverview(routeView(), {
			plan: {
				body: {
					...emptyPlan,
					reasons: ["The route table changed."],
					warnings: ["Port 25565 is new: the proxy restarts once."],
					blocking: [{ code: "port_in_use", message: "Port 25565 is taken." }],
				},
			},
		});
		const plan = within(
			await screen.findByRole("region", { name: "Before you deploy" }),
		);
		expect(plan.getByText("The route table changed.")).toBeInTheDocument();
		expect(
			plan.getByText("Port 25565 is new: the proxy restarts once."),
		).toBeInTheDocument();
		expect(plan.getByText("Port 25565 is taken.")).toBeInTheDocument();
	});

	test("omits the section when the plan has nothing to say", async () => {
		const { api } = await renderOverview(routeView());
		expect(
			api.calls.some((call) => call.path.endsWith("/deployments/plan")),
		).toBe(true);
		expect(
			screen.queryByRole("region", { name: "Before you deploy" }),
		).toBeNull();
	});

	test("omits the section when the plan call fails", async () => {
		await renderOverview(routeView(), {
			plan: {
				status: 500,
				body: { status: 500, code: "boom", detail: "no plan" },
			},
		});
		expect(
			screen.queryByRole("region", { name: "Before you deploy" }),
		).toBeNull();
	});
});

describe("RouteOverviewTab: latest deployment", () => {
	test("a failed rollback shows the recovery banner", async () => {
		await renderOverview(
			routeView({
				latestDeployment: {
					id: "dep-9",
					createdAt: "2026-10-31T11:00:00Z",
					currentStep: null,
					failedStep: "verify",
					finishedAt: "2026-10-31T11:05:00Z",
					message: null,
					releaseId: "",
					status: "rollback_failed",
					statusMessage: "",
					strategy: "route_only",
					trigger: "manual",
				},
			}),
			{
				extra: {
					"GET /deployments/:deploymentId": {
						body: make("DeploymentView", {
							id: "dep-9",
							serviceId: "svc-route",
							releaseId: "",
							serverId: "srv-1",
							trigger: "manual",
							actorType: "user",
							actorLabel: "Robin",
							status: "rollback_failed",
							strategy: "route_only",
							statusMessage: "",
							message: "",
							rolledBackFrom: "",
							generation: 7,
							createdAt: "2026-10-31T11:00:00Z",
						}),
					},
				},
			},
		);
		expect(
			await screen.findByText(
				/Rollback failed: homeassistant may not be serving/,
			),
		).toBeInTheDocument();
	});
});
