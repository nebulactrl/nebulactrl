import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import type { ServiceMeshView } from "#/api/gen/types.gen";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { MeshSection } from "./mesh-section";

const server = (name: string) => ({ id: `srv-${name}`, name });

const service = makeService({ id: "svc-mesh" });

const mesh = (overrides: Partial<ServiceMeshView> = {}): ServiceMeshView => ({
	consumers: [],
	exposures: [],
	routes: [],
	...overrides,
});

describe("MeshSection", () => {
	test("loads the mesh of this service and shows a placeholder meanwhile", async () => {
		const api = stubApi({ "GET /services/:serviceId/mesh": { body: mesh() } });
		await renderWithProviders(<MeshSection service={service} />);
		expect(screen.getByRole("status")).toBeInTheDocument();
		await screen.findByText(/This service does not use the mesh/);
		expect(api.calls).toEqual([
			{
				method: "GET",
				path: "/services/svc-mesh/mesh",
				query: expect.any(URLSearchParams),
				body: undefined,
			},
		]);
		expect(api.unmatched).toEqual([]);
	});

	test("explains that the service stays local when it has no links", async () => {
		stubApi({
			"GET /services/:serviceId/mesh": {
				body: { consumers: null, exposures: null, routes: null },
			},
		});
		await renderWithProviders(<MeshSection service={service} />);
		expect(
			await screen.findByText(/This service does not use the mesh/),
		).toBeInTheDocument();
		expect(screen.queryByText("Shared with other servers")).toBeNull();
	});

	test("lists exposures, routes and consumers with their states", async () => {
		stubApi({
			"GET /services/:serviceId/mesh": {
				body: mesh({
					exposures: [
						{
							id: "exp-1",
							alias: "db",
							containerPort: 5432,
							meshPort: 15432,
							purpose: "replication",
							server: server("hetzner-01"),
						},
					],
					routes: [
						{
							alias: "cache",
							consumerId: "svc-mesh",
							consumerSlug: "web",
							devicesAdded: 0,
							keys: null,
							port: 6379,
							provider: "redis",
							providerId: "svc-redis",
							server: server("hetzner-02"),
							source: "edge",
							sources: null,
							state: "online",
						},
						{
							alias: "queue",
							consumerId: "svc-mesh",
							consumerSlug: "web",
							devicesAdded: 0,
							keys: null,
							notExposable: "no port",
							port: 0,
							provider: "rabbit",
							providerId: "svc-rabbit",
							server: server("hetzner-03"),
							source: "ref",
							sources: null,
							state: "granted",
						},
					],
					consumers: [
						{
							server: server("hetzner-04"),
							serviceId: "svc-cron",
							slug: "cron",
							source: "replica",
							state: "needs_attention",
						},
					],
				}),
			},
		});
		await renderWithProviders(<MeshSection service={service} />);

		const shared = await screen.findByRole("list", {
			name: "Shared with other servers",
		});
		expect(within(shared).getByText("db:15432")).toBeInTheDocument();
		expect(
			within(shared).getByText(
				"container port 5432 · replication · on hetzner-01",
			),
		).toBeInTheDocument();

		const routes = screen.getByRole("list", { name: "Reaches over the mesh" });
		const [cache, queue] = within(routes).getAllByRole("listitem");
		expect(within(cache as HTMLElement).getByText("cache:6379")).toBeVisible();
		expect(cache).toHaveTextContent("redis on hetzner-02 · canvas link");
		expect(cache).toHaveTextContent("Online");
		// A route without a shared port shows the bare alias and says why.
		expect(within(queue as HTMLElement).getByText("queue")).toBeVisible();
		expect(queue).toHaveTextContent(
			"rabbit on hetzner-03 · variable reference · no port shared yet · cannot be shared",
		);
		// A granted link is still coming up.
		expect(queue).toHaveTextContent("Provisioning");

		const consumers = screen.getByRole("list", { name: "Reached by" });
		const cron = within(consumers).getByRole("listitem");
		expect(cron).toHaveTextContent("cron");
		expect(cron).toHaveTextContent("on hetzner-04 · replica");
		expect(cron).toHaveTextContent("Needs attention");
	});

	test("says which of the three lists is empty", async () => {
		stubApi({
			"GET /services/:serviceId/mesh": {
				body: mesh({
					consumers: [
						{
							server: server("hetzner-04"),
							serviceId: "svc-cron",
							slug: "cron",
							source: "edge",
							state: "online",
						},
					],
				}),
			},
		});
		await renderWithProviders(<MeshSection service={service} />);
		expect(await screen.findByText("Not shared.")).toBeInTheDocument();
		expect(screen.getByText("Nothing on another server.")).toBeInTheDocument();
		expect(screen.queryByText("Nobody.")).toBeNull();
		expect(
			screen.getByRole("list", { name: "Reached by" }),
		).toBeInTheDocument();
	});

	test("points to Server mesh settings when no tailnet is connected", async () => {
		stubApi({
			"GET /services/:serviceId/mesh": {
				status: 409,
				body: {
					status: 409,
					code: "mesh_not_configured",
					title: "Conflict",
					detail: "mesh is not configured",
				},
			},
		});
		await renderWithProviders(<MeshSection service={service} />);
		expect(
			await screen.findByText(/No tailnet is connected/),
		).toBeInTheDocument();
		const link = screen.getByRole("link", {
			name: "Open Team settings → Server mesh",
		});
		expect(link).toHaveAttribute("href", "/settings/server-mesh");
	});

	test("reports a load failure without claiming the mesh is empty", async () => {
		stubApi({
			"GET /services/:serviceId/mesh": {
				status: 500,
				body: { status: 500, code: "internal", title: "Internal Server Error" },
			},
		});
		await renderWithProviders(<MeshSection service={service} />);
		expect(
			await screen.findByText(
				"The mesh status did not load. Nothing was changed.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText(/does not use the mesh/)).toBeNull();
		expect(screen.queryByText(/No tailnet is connected/)).toBeNull();
	});
});
