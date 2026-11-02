import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { routeCreate } from "../create-store";
import { RouteCreateHost } from "./route-create-host";
import { production, serverView, serviceView } from "./test-fixtures";

afterEach(() => routeCreate.close());

const ROUTE = "/p/$projectId/$envSlug";
const PATH = "/p/proj-1/production";

function stubs(extra: Parameters<typeof stubApi>[0] = {}) {
	return stubApi({
		"GET /projects": {
			body: make("PageProject", {
				items: [make("Project", { id: "proj-1", name: "Home" })],
			}),
		},
		"GET /projects/:id/environments": { body: { items: [production] } },
		"GET /servers": {
			body: make("PageServer", {
				items: [serverView({ id: "s-edge", name: "edge" })],
			}),
		},
		"GET /servers/:id/proxy": {
			body: make("ServerProxy", {
				tailnet: { integrationConfigured: false, mode: "off" },
			}),
		},
		"GET /servers/:id/ports": { body: { items: [] } },
		"GET /environments/:id/services": {
			body: make("PageServiceView", {
				items: [
					serviceView({
						id: "svc-mc",
						name: "minecraft",
						serverId: "s-edge",
						serverName: "edge",
						port: 25565,
					}),
				],
			}),
		},
		...extra,
	});
}

describe("RouteCreateHost", () => {
	test("shows nothing until a create is requested", async () => {
		stubs();
		await renderWithProviders(<RouteCreateHost />, {
			path: PATH,
			routePath: ROUTE,
		});
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	test("an external request opens the External service dialog on the environment's servers", async () => {
		const api = stubs();
		await renderWithProviders(<RouteCreateHost />, {
			path: PATH,
			routePath: ROUTE,
		});
		act(() => routeCreate.open({ mode: "external" }));
		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveTextContent("External service");
		// The server list arrives and the only online v2 server is preselected.
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Published by" }),
			).toHaveTextContent("edge"),
		);
		expect(api.unmatched).toEqual([]);
	});

	test("a port request opens the TCP/UDP port dialog for the environment, preselecting its target", async () => {
		stubs();
		await renderWithProviders(<RouteCreateHost />, {
			path: PATH,
			routePath: ROUTE,
		});
		act(() =>
			routeCreate.open({
				mode: "port",
				target: { serviceId: "svc-mc", port: 25565 },
			}),
		);
		expect(await screen.findByRole("dialog")).toHaveTextContent("TCP/UDP port");
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Service" }),
			).toHaveTextContent("minecraft"),
		);
		expect(screen.getByLabelText("Its port")).toHaveValue("25565");
	});

	test("created routes land in the environment on screen, at the requested position", async () => {
		const posted: { path: string; body: unknown }[] = [];
		const placed: unknown[] = [];
		stubs({
			"POST /environments/:id/routes": async (request) => {
				posted.push({
					path: new URL(request.url).pathname,
					body: await request.clone().json(),
				});
				return Response.json(
					serviceView({
						id: "svc-ha",
						name: "ha",
						kind: "route",
						serverId: "s-edge",
					}),
					{ status: 201 },
				);
			},
			"GET /services/:id": {
				body: serviceView({ id: "svc-ha", name: "ha" }),
			},
			"GET /projects/:p/environments/:e/canvas": {
				body: make("Canvas", {
					version: 1,
					layout: { nodes: {}, groups: [] },
				}),
			},
			"PUT /projects/:p/environments/:e/canvas": async (request) => {
				placed.push(await request.clone().json());
				return Response.json({ version: 2, layout: { nodes: {}, groups: [] } });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<RouteCreateHost />, {
			path: PATH,
			routePath: ROUTE,
		});
		act(() =>
			routeCreate.open({ mode: "external", position: { x: 40, y: 60 } }),
		);
		await screen.findByRole("dialog");
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Published by" }),
			).toHaveTextContent("edge"),
		);
		await user.type(screen.getByLabelText("Name"), "ha");
		await user.type(screen.getByLabelText("Where it listens"), "10.0.0.5:8123");
		await user.click(screen.getByRole("button", { name: /Create route/ }));

		await waitFor(() => expect(placed).toHaveLength(1));
		expect(posted[0]?.path).toBe("/api/v1/environments/env-prod/routes");
		expect((placed[0] as { layout: { nodes: unknown } }).layout.nodes).toEqual({
			"svc-ha": { x: 40, y: 60 },
		});
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("cancelling closes the dialog", async () => {
		stubs();
		const user = userEvent.setup();
		await renderWithProviders(<RouteCreateHost />, {
			path: PATH,
			routePath: ROUTE,
		});
		act(() => routeCreate.open({ mode: "external" }));
		await screen.findByRole("dialog");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("Escape closes the dialog", async () => {
		stubs();
		const user = userEvent.setup();
		await renderWithProviders(<RouteCreateHost />, {
			path: PATH,
			routePath: ROUTE,
		});
		act(() => routeCreate.open({ mode: "port" }));
		await screen.findByRole("dialog");
		await user.keyboard("{Escape}");
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("outside a project there is no environment, so no dialog opens", async () => {
		const api = stubs();
		await renderWithProviders(<RouteCreateHost />, {
			path: "/",
			routePath: "$",
		});
		act(() => routeCreate.open({ mode: "external" }));
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/projects")).toBe(false);
	});

	test("an environment that does not exist in the project opens no dialog", async () => {
		stubs();
		await renderWithProviders(<RouteCreateHost />, {
			path: "/p/proj-1/ghost",
			routePath: ROUTE,
		});
		act(() => routeCreate.open({ mode: "external" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("switching from a port to an external request swaps the form", async () => {
		stubs();
		await renderWithProviders(<RouteCreateHost />, {
			path: PATH,
			routePath: ROUTE,
		});
		act(() => routeCreate.open({ mode: "port" }));
		expect(await screen.findByRole("dialog")).toHaveTextContent("TCP/UDP port");
		act(() => routeCreate.open({ mode: "external" }));
		await waitFor(() =>
			expect(screen.getByRole("dialog")).toHaveTextContent("External service"),
		);
		expect(screen.getByRole("dialog")).not.toHaveTextContent("TCP/UDP port");
	});
});
