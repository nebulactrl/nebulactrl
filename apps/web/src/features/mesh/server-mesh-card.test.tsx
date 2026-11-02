import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ServerMeshView } from "#/api/gen/types.gen";
import { meQuery } from "#/api/hooks";
import type { ServerView } from "#/features/servers/model";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ServerMeshCard } from "./server-mesh-card";
import { meBody, meshView, node } from "./test-data";

const server = { id: "srv-1", name: "web-02" } as ServerView;

function view(overrides: Partial<ServerMeshView> = {}): ServerMeshView {
	return {
		agentCapable: true,
		enabled: true,
		links: [],
		node: node({ serverId: "srv-1", serverName: "web-02" }),
		paused: false,
		tunDevice: true,
		...overrides,
	};
}

const WRITER = { "GET /me": { body: meBody(["servers:write"]) } };

/** Renders, and waits for the principal: until then nothing may be changed. */
async function renderCard() {
	const result = await renderWithProviders(<ServerMeshCard server={server} />);
	await waitFor(() =>
		expect(result.queryClient.getQueryData(meQuery().queryKey)).toBeDefined(),
	);
	return result;
}

describe("ServerMeshCard", () => {
	test("shows the node's state, address, peers, shares and devices once the mesh is enabled", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": {
				body: view({
					node: node({
						devices: 2,
						message: "Healthy per agent",
						exposures: [
							{
								meshPort: 15432,
								purpose: "service",
								serviceId: "svc-pg",
								serviceName: "postgres",
							},
						],
						peers: [{ direct: true, ips: [], name: "db-01", online: true }],
					}),
				}),
			},
		});
		await renderCard();
		expect(await screen.findByText("Online")).toBeInTheDocument();
		expect(screen.getByText("As the agent last reported.")).toBeInTheDocument();
		expect(screen.getByText("Healthy per agent")).toBeInTheDocument();
		expect(screen.getByText("100.64.0.1")).toBeInTheDocument();
		expect(screen.getByText("web-01.tail1234.ts.net")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Copy mesh address" }),
		).toBeInTheDocument();
		expect(screen.getByText("1 peer online · 1 direct")).toBeInTheDocument();
		expect(screen.getByText("postgres:15432")).toBeInTheDocument();
		expect(screen.getByText("2 devices")).toBeInTheDocument();
	});

	test("a node with no address, peers or shares says so", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": {
				body: view({
					node: node({ ips: null, fqdn: "", peers: null, exposures: null }),
				}),
			},
		});
		await renderCard();
		expect(await screen.findByText("Not assigned yet")).toBeInTheDocument();
		expect(screen.getByText("No peers")).toBeInTheDocument();
		expect(screen.getByText("Nothing yet")).toBeInTheDocument();
	});

	test("a node removed from the tailnet tells the person to re-provision it", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": {
				body: view({
					node: node({ needsReprovision: true, state: "needs_attention" }),
				}),
			},
		});
		await renderCard();
		expect(
			await screen.findByText(
				"The node was removed from the tailnet. Re-provision it.",
			),
		).toBeInTheDocument();
		expect(screen.getByText("Needs attention")).toBeInTheDocument();
	});

	test("a server not in the mesh reads 'Not enabled' and offers Enable mesh to a writer", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": {
				body: view({ enabled: false }),
			},
		});
		await renderCard();
		expect(await screen.findByText("Not enabled")).toBeInTheDocument();
		expect(
			screen.getByText("Not in the mesh. The plan shows what enabling adds."),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("button", { name: "Enable mesh…" }),
		).toBeEnabled();
		expect(
			screen.queryByRole("button", { name: "Test" }),
		).not.toBeInTheDocument();
		expect(screen.queryByText("Mesh address")).not.toBeInTheDocument();
	});

	test("Enable mesh… opens the plan dialog for this server", async () => {
		const api = stubApi({
			...WRITER,
			"GET /servers/:id/mesh": {
				body: view({ enabled: false }),
			},
			"GET /mesh": { body: meshView() },
			"PUT /servers/:id/mesh": {
				status: 202,
				body: {
					applied: false,
					enabled: false,
					plan: {
						blockers: [],
						devicesAdded: 1,
						tunDevice: true,
						warnings: [],
					},
				},
			},
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(
			await screen.findByRole("button", { name: "Enable mesh…" }),
		);
		expect(
			await screen.findByText("Enable the mesh on web-02?"),
		).toBeInTheDocument();
		await screen.findByText("Adds 1 device");
		expect(api.calls.find((c) => c.method === "PUT")?.query.get("dryRun")).toBe(
			"true",
		);
	});

	test("without servers:write a server not in the mesh only explains the missing scope", async () => {
		stubApi({
			"GET /me": { body: meBody([]) },
			"GET /servers/:id/mesh": {
				body: view({ enabled: false }),
			},
		});
		await renderCard();
		expect(
			await screen.findByText(
				"Enabling the mesh needs the servers:write scope.",
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Enable mesh…" }),
		).not.toBeInTheDocument();
	});

	test("an agent that is too old cannot enable or re-provision and gets the upgrade banner", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": {
				body: view({ enabled: false, agentCapable: false }),
			},
		});
		await renderCard();
		expect(await screen.findByTestId("agent-too-old")).toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Enable mesh…" }),
			).toBeDisabled(),
		);
	});

	test("an enabled node on an old agent cannot be re-provisioned", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": {
				body: view({ agentCapable: false }),
			},
		});
		await renderCard();
		expect(await screen.findByTestId("agent-too-old")).toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Re-provision" }),
			).toBeDisabled(),
		);
	});

	test("a paused mesh is announced", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": { body: view({ paused: true }) },
		});
		await renderCard();
		expect(
			await screen.findByText(
				/The mesh is paused, so changes here wait until it resumes/,
			),
		).toBeInTheDocument();
	});

	test("a host without /dev/net/tun says it can join but not host a link", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": {
				body: view({ tunDevice: false }),
			},
		});
		await renderCard();
		expect(
			await screen.findByText("Links to other servers"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/It can join the mesh and be reached, but it cannot host a link/,
			),
		).toBeInTheDocument();
	});

	test("a team with no tailnet points to the team settings", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": problem(409, {
				code: "mesh_not_configured",
				detail: "none",
			}),
		});
		await renderCard();
		const open = await screen.findByRole("link", {
			name: "Open Team settings → Server mesh",
		});
		expect(open).toHaveAttribute("href", "/settings/server-mesh");
		expect(screen.getByText(/No tailnet is connected yet/)).toBeInTheDocument();
	});

	test("a failed load offers Try again, which recovers", async () => {
		let attempts = 0;
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json(view());
			},
		});
		const user = userEvent.setup();
		await renderCard();
		expect(
			await screen.findByText(
				"The mesh status did not load. Nothing was changed.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(await screen.findByText("Online")).toBeInTheDocument();
	});

	test("Test runs the connectivity check and lists each peer by name with outcome, latency and message", async () => {
		const api = stubApi({
			...WRITER,
			"GET /servers/:id/mesh": { body: view() },
			"GET /servers": {
				body: make("PageServer", {
					items: [make("Server", { id: "srv-db", name: "db-01" })],
				}),
			},
			"POST /servers/:id/mesh/check": {
				body: {
					results: [
						{
							outcome: "connected",
							peerServerId: "srv-db",
							latencyMs: 12,
						},
						{
							outcome: "refused",
							peerServerId: "srv-gone",
							message: "port closed",
						},
						{ outcome: "timeout" },
					],
				},
			},
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(await screen.findByRole("button", { name: "Test" }));
		expect(await screen.findByText("Connected")).toBeInTheDocument();
		expect(await screen.findByText("db-01")).toBeInTheDocument();
		expect(screen.getByText("12 ms")).toBeInTheDocument();
		// A peer the control plane has no name for is shown by id.
		expect(screen.getByText("srv-gone")).toBeInTheDocument();
		expect(screen.getByText("Refused")).toBeInTheDocument();
		expect(screen.getByText("port closed")).toBeInTheDocument();
		expect(screen.getByText("This server")).toBeInTheDocument();
		expect(screen.getByText("Timed out")).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/servers/srv-1/mesh/check");
		expect(post?.body).toEqual({});
	});

	test("a check with no peers says there is nothing to test yet", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": { body: view() },
			"GET /servers": { body: { items: [] } },
			"POST /servers/:id/mesh/check": { body: { results: null } },
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(await screen.findByRole("button", { name: "Test" }));
		expect(
			await screen.findByText(
				/No peer to test yet\. Enable the mesh on another server first\./,
			),
		).toBeInTheDocument();
	});

	test("a failed check reports the reason in a toast", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": { body: view() },
			"POST /servers/:id/mesh/check": problem(409, {
				code: "mesh_agent_too_old",
				detail: "old",
			}),
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(await screen.findByRole("button", { name: "Test" }));
		expect(
			await screen.findByText("Could not test the mesh"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/The agent on this server is too old for the mesh/),
		).toBeInTheDocument();
	});

	test("Test is available without write access, but Re-provision and Disable are not", async () => {
		stubApi({
			"GET /me": { body: meBody([]) },
			"GET /servers/:id/mesh": { body: view() },
		});
		await renderCard();
		expect(
			await screen.findByRole("button", { name: "Test" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Re-provision" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Disable mesh" }),
		).not.toBeInTheDocument();
	});

	test("Re-provision confirms first, then posts with an idempotency key and says it is under way", async () => {
		let key: string | null = null;
		const api = stubApi({
			...WRITER,
			"GET /servers/:id/mesh": { body: view() },
			"POST /servers/:id/mesh/reprovision": (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json({});
			},
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(
			await screen.findByRole("button", { name: "Re-provision" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Re-provision the mesh node?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/web-02's node is deleted from your tailnet/),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Re-provision" }),
		);
		expect(await screen.findByText("Re-provisioning")).toBeInTheDocument();
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/servers/srv-1/mesh/reprovision",
		);
	});

	test("a failed re-provision keeps the confirmation open and toasts the reason", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": { body: view() },
			"POST /servers/:id/mesh/reprovision": problem(409, {
				code: "mesh_paused",
				detail: "paused",
			}),
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(
			await screen.findByRole("button", { name: "Re-provision" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Re-provision" }),
		);
		expect(
			await screen.findByText("Could not re-provision"),
		).toBeInTheDocument();
		expect(
			screen.getByText("The mesh is paused. Changes wait until it resumes."),
		).toBeInTheDocument();
	});

	test("Disable mesh confirms, sends enabled:false, and says the server left", async () => {
		let sent: { body: unknown; key: string | null } | undefined;
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": { body: view() },
			"PUT /servers/:id/mesh": async (request) => {
				sent = {
					body: await request.json(),
					key: request.headers.get("Idempotency-Key"),
				};
				return Response.json({});
			},
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(
			await screen.findByRole("button", { name: "Disable mesh" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Take web-02 out of the mesh?"),
		).toBeInTheDocument();
		expect(sent).toBeUndefined();
		await user.click(
			within(dialog).getByRole("button", { name: "Disable mesh" }),
		);
		expect(await screen.findByText("web-02 left the mesh")).toBeInTheDocument();
		expect(sent?.body).toEqual({ enabled: false });
		expect(sent?.key).toMatch(/^[0-9a-f-]{36}$/);
	});

	test("a refused disable shows the mesh_server_in_use sentence inside the confirmation", async () => {
		stubApi({
			...WRITER,
			"GET /servers/:id/mesh": { body: view() },
			"PUT /servers/:id/mesh": problem(409, {
				code: "mesh_server_in_use",
				detail: "in use",
			}),
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(
			await screen.findByRole("button", { name: "Disable mesh" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Disable mesh" }),
		);
		expect(
			await within(dialog).findByText(
				/Replicas or links to other servers still use this server's mesh node/,
			),
		).toBeInTheDocument();
		expect(screen.queryByText("web-02 left the mesh")).not.toBeInTheDocument();
	});
});
