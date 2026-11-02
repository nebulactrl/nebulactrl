import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { serverViewsQuery } from "#/features/servers/queries";
import { statusPagesQuery } from "#/features/status-pages/queries";
import { make } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import {
	allowSlowRoutes,
	loadRoute,
	renderApp,
	signedInApi,
} from "./route-harness";

allowSlowRoutes();

// A route's loader starts the page's requests and returns at once, so the
// navigation is never held back and the click renders from the cache.

describe("the servers loader", () => {
	test("puts the server cards in the cache without holding the navigation back", async () => {
		const answer = Promise.withResolvers<Response>();
		const queryClient = testQueryClient();
		// The guard's own /servers call answers at once; the loader's is held open.
		let calls = 0;
		signedInApi({
			"GET /servers": () => {
				calls++;
				return calls === 1 ? Response.json({ items: [] }) : answer.promise;
			},
		});
		const router = await loadRoute("/servers", queryClient);
		// The navigation finished while the loader's request is still open.
		expect(router.state.location.pathname).toBe("/servers");
		expect(
			queryClient.getQueryData(serverViewsQuery().queryKey),
		).toBeUndefined();

		answer.resolve(
			Response.json({
				items: [
					{
						id: "srv-1",
						name: "edge-1",
						status: "online",
						createdAt: "2026-01-01T00:00:00Z",
						serviceCount: 3,
					},
				],
			}),
		);
		await waitFor(() =>
			expect(
				queryClient.getQueryData(serverViewsQuery().queryKey),
			).toHaveLength(1),
		);
	});
});

describe("the status pages loader", () => {
	test("puts the list in the cache", async () => {
		const queryClient = testQueryClient();
		const api = signedInApi({
			"GET /status-pages": {
				body: make("PageListOutputBody", {
					items: [
						make("StatusPageView", { id: "sp-1", title: "Public status" }),
					],
				}),
			},
		});
		const router = await loadRoute("/status-pages", queryClient);
		expect(router.state.location.pathname).toBe("/status-pages");
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/status-pages")).toBe(true),
		);
		await waitFor(() =>
			expect(
				queryClient.getQueryData(statusPagesQuery().queryKey),
			).toBeDefined(),
		);
	});
});

describe("the deployments loader", () => {
	const environments = make("PageEnvironment", {
		items: [
			make("Environment", {
				id: "env-1",
				name: "production",
				slug: "production",
				isProduction: true,
			}),
			make("Environment", {
				id: "env-2",
				name: "staging",
				slug: "staging",
				isProduction: false,
			}),
		],
	});

	test("starts the environment's deployments, its daily strip and its services", async () => {
		const api = signedInApi({
			"GET /projects/:id/environments": { body: environments },
			"GET /environments/:id/deployments": { body: { items: [] } },
			"GET /environments/:id/deployments/daily": { body: { days: [] } },
			"GET /environments/:id/services": { body: { items: [] } },
		});
		await loadRoute("/p/proj-1/staging/deployments");
		await waitFor(() => {
			const paths = api.calls.map((c) => c.path);
			expect(paths).toContain("/environments/env-2/deployments");
			expect(paths).toContain("/environments/env-2/deployments/daily");
			expect(paths).toContain("/environments/env-2/services");
		});
		expect(
			api.calls.some((c) => c.path.startsWith("/environments/env-1/")),
		).toBe(false);
	});

	test("the status and service filters from the address narrow the request", async () => {
		const api = signedInApi({
			"GET /projects/:id/environments": { body: environments },
			"GET /environments/:id/deployments": { body: { items: [] } },
			"GET /environments/:id/deployments/daily": { body: { days: [] } },
			"GET /environments/:id/services": { body: { items: [] } },
		});
		await loadRoute(
			"/p/proj-1/production/deployments?status=failed&service=svc-9",
		);
		await waitFor(() =>
			expect(
				api.calls.some((c) => c.path === "/environments/env-1/deployments"),
			).toBe(true),
		);
		const list = api.calls.find(
			(c) => c.path === "/environments/env-1/deployments",
		);
		expect(list?.query.get("serviceId")).toBe("svc-9");
		expect(list?.query.get("status")).toBe("failed,rollback_failed");
	});

	test("an environment the project does not have starts nothing", async () => {
		const api = signedInApi({
			"GET /projects/:id/environments": { body: environments },
		});
		const router = await loadRoute("/p/proj-1/ghost/deployments");
		await waitFor(() =>
			expect(
				api.calls.some((c) => c.path === "/projects/proj-1/environments"),
			).toBe(true),
		);
		expect(router.state.location.pathname).toBe("/p/proj-1/ghost/deployments");
		expect(api.calls.some((c) => c.path.startsWith("/environments/"))).toBe(
			false,
		);
		expect(api.unmatched).toEqual([]);
	});
});

describe("the deployments route's filters", () => {
	const deploymentsApi = () =>
		signedInApi({
			"GET /projects/:id/environments": {
				body: make("PageEnvironment", {
					items: [
						make("Environment", {
							id: "env-1",
							projectId: "proj-1",
							slug: "production",
							name: "Production",
							isProduction: true,
						}),
					],
				}),
			},
			"GET /environments/:id/deployments": { body: { items: [] } },
			"GET /environments/:id/deployments/daily": { body: { days: [] } },
			"GET /environments/:id/services": { body: { items: [] } },
		});

	test("choosing a status puts it in the address", async () => {
		deploymentsApi();
		const { router } = await renderApp("/p/proj-1/production/deployments");
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("combobox", { name: "Filter by status" }),
		);
		await user.click(await screen.findByRole("option", { name: "Failed" }));
		await waitFor(() =>
			expect(router.state.location.href).toBe(
				"/p/proj-1/production/deployments?status=failed",
			),
		);
	});

	test("the Builds type is kept in the address next to the other filters", async () => {
		deploymentsApi();
		const { router } = await renderApp(
			"/p/proj-1/production/deployments?status=failed",
		);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("combobox", { name: "Filter by type" }),
		);
		await user.click(await screen.findByRole("option", { name: "Builds" }));
		await waitFor(() =>
			expect(router.state.location.search).toMatchObject({
				type: "builds",
				status: "failed",
			}),
		);
	});

	test("an address with filters opens the page already filtered", async () => {
		const api = deploymentsApi();
		await renderApp("/p/proj-1/production/deployments?status=failed");
		expect(
			await screen.findByRole("combobox", { name: "Filter by status" }),
		).toHaveTextContent("Failed");
		await waitFor(() =>
			expect(
				api.calls.some(
					(c) =>
						c.path === "/environments/env-1/deployments" &&
						c.query.get("status") === "failed,rollback_failed",
				),
			).toBe(true),
		);
	});
});
