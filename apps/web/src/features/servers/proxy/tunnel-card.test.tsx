import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { TunnelView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { TunnelCard } from "./tunnel-card";

const tunnel = (over: Partial<TunnelView> = {}): TunnelView => ({
	accountReady: true,
	active: false,
	agentSupported: true,
	cloudflareConnected: true,
	connections: 0,
	enabled: false,
	ingressHosts: 0,
	ingressSyncedAt: null,
	message: "",
	observedAt: null,
	state: "off",
	upstream: "http://nebulactrl-traefik:80",
	...over,
});

const online = (over: Partial<TunnelView> = {}): TunnelView =>
	tunnel({
		enabled: true,
		state: "online",
		active: true,
		connections: 4,
		ingressHosts: 3,
		tunnelName: "nebula-prod",
		cname: "abc123.cfargotunnel.com",
		...over,
	});

const meOf = (scopes: string[]) => ({
	body: {
		id: "u1",
		name: "Ada",
		email: "ada@example.com",
		instanceAdmin: false,
		workspaceId: "w1",
		createdAt: "2026-01-01T00:00:00Z",
		principal: { type: "user", label: "Ada", scopes },
	},
});
const ADMIN = meOf(["admin"]);
const MEMBER = meOf([]);

const problem = (status: number, detail: string, code = "bad_request") => ({
	status,
	body: { status, title: "Error", detail, code },
});

const renderCard = () =>
	renderWithProviders(<TunnelCard serverId="s1" serverName="edge-1" />);

describe("TunnelCard loading", () => {
	test("offers a retry when the tunnel did not load, and recovers", async () => {
		let attempts = 0;
		stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json(tunnel());
			},
		});
		const user = userEvent.setup();
		await renderCard();
		expect(
			await screen.findByText("The tunnel did not load"),
		).toBeInTheDocument();
		expect(screen.queryByText("Enable tunnel")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(
			await screen.findByRole("button", { name: "Enable tunnel" }),
		).toBeInTheDocument();
		expect(attempts).toBe(2);
	});
});

describe("TunnelCard when the tunnel is off", () => {
	test("an administrator can enable a ready team; the confirm posts with an idempotency key", async () => {
		let key: string | null = null;
		const api = stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": { body: tunnel() },
			"POST /servers/:serverId/tunnel": (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json(tunnel({ enabled: true, state: "provisioning" }));
			},
		});
		const user = userEvent.setup();
		await renderCard();
		expect(await screen.findByText(/needs the permission/)).toBeInTheDocument();
		const enable = await screen.findByRole("button", {
			name: "Enable tunnel",
		});
		await waitFor(() => expect(enable).toBeEnabled());
		await user.click(enable);

		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Enable a Cloudflare Tunnel on edge-1?"),
		).toBeInTheDocument();
		// Nothing is sent until the dialog is confirmed.
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Enable tunnel" }),
		);

		expect(await screen.findByText("Creating the tunnel")).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/servers/s1/tunnel");
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		expect(api.unmatched).toEqual([]);
	});

	test("cancelling the enable dialog sends nothing", async () => {
		const api = stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": { body: tunnel() },
		});
		const user = userEvent.setup();
		await renderCard();
		const enable = await screen.findByRole("button", {
			name: "Enable tunnel",
		});
		await waitFor(() => expect(enable).toBeEnabled());
		await user.click(enable);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});

	test("a failed enable keeps the dialog open and shows the server's message", async () => {
		stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": { body: tunnel() },
			"POST /servers/:serverId/tunnel": problem(
				409,
				"Cloudflare refused the token",
				"cloudflare_denied",
			),
		});
		const user = userEvent.setup();
		await renderCard();
		const enable = await screen.findByRole("button", {
			name: "Enable tunnel",
		});
		await waitFor(() => expect(enable).toBeEnabled());
		await user.click(enable);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Enable tunnel" }),
		);
		expect(
			await screen.findAllByText("Cloudflare refused the token"),
		).not.toHaveLength(0);
		expect(screen.queryByText("Creating the tunnel")).not.toBeInTheDocument();
	});

	test("without a Cloudflare connection the button is disabled and links to the integrations", async () => {
		stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": {
				body: tunnel({ cloudflareConnected: false }),
			},
		});
		await renderCard();
		expect(
			await screen.findByText(/Cloudflare is not connected\./),
		).toBeInTheDocument();
		expect(screen.getByRole("link")).toHaveAttribute(
			"href",
			"/settings/integrations",
		);
		expect(screen.getByRole("link")).toHaveTextContent(
			"Open Team settings → Integrations",
		);
		expect(
			screen.getByRole("button", { name: "Enable tunnel" }),
		).toBeDisabled();
		expect(screen.queryByText(/needs the permission/)).not.toBeInTheDocument();
	});

	test("a connection without an account says so and links to the integrations", async () => {
		stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": {
				body: tunnel({ accountReady: false }),
			},
		});
		await renderCard();
		expect(await screen.findByText(/no account selected/)).toBeInTheDocument();
		expect(screen.getByRole("link")).toHaveAttribute(
			"href",
			"/settings/integrations",
		);
		expect(
			screen.getByRole("button", { name: "Enable tunnel" }),
		).toBeDisabled();
	});

	test("an agent that is too old blocks enabling without an integrations link", async () => {
		stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": {
				body: tunnel({ agentSupported: false }),
			},
		});
		await renderCard();
		expect(await screen.findByText(/agent is too old/)).toBeInTheDocument();
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Enable tunnel" }),
		).toBeDisabled();
	});

	test("a member who is not an administrator sees no enable button", async () => {
		stubApi({
			"GET /me": MEMBER,
			"GET /servers/:serverId/tunnel": { body: tunnel() },
		});
		await renderCard();
		expect(
			await screen.findByText("Only an administrator can enable a tunnel."),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Enable tunnel" }),
		).not.toBeInTheDocument();
	});
});

describe("TunnelCard when the tunnel is on", () => {
	test("shows the connector state, connections, CNAME, hostnames and upstream", async () => {
		stubApi({
			"GET /me": MEMBER,
			"GET /servers/:serverId/tunnel": {
				body: online({
					observedAt: "2026-01-01T00:00:00Z",
					ingressSyncedAt: "2026-01-01T00:00:00Z",
				}),
			},
		});
		await renderCard();
		const status = await screen.findByRole("region", {
			name: "Tunnel status",
		});
		expect(within(status).getByText("Connected")).toBeInTheDocument();
		expect(
			within(status).getByText("4 connections to Cloudflare"),
		).toBeInTheDocument();
		expect(within(status).getByText(/reported/)).toBeInTheDocument();
		expect(
			within(status).getByText("abc123.cfargotunnel.com"),
		).toBeInTheDocument();
		expect(
			within(status).getByRole("button", { name: "Copy tunnel CNAME target" }),
		).toBeInTheDocument();
		expect(status).toHaveTextContent(
			"nebula-prod · 3 hostnames → http://nebulactrl-traefik:80",
		);
		expect(status).toHaveTextContent("synced");
		expect(
			screen.getByText(/Ports 80 and 443 may stay closed\./),
		).toBeInTheDocument();
		// Not an administrator: no controls at all.
		expect(
			screen.queryByRole("button", { name: "Disable tunnel" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Re-sync" }),
		).not.toBeInTheDocument();
	});

	test("omits the CNAME, report time and sync time when the connector has not reported", async () => {
		stubApi({
			"GET /me": MEMBER,
			"GET /servers/:serverId/tunnel": {
				body: tunnel({
					enabled: true,
					state: "provisioning",
					ingressHosts: 1,
					upstream: "http://traefik:80",
				}),
			},
		});
		await renderCard();
		const status = await screen.findByRole("region", {
			name: "Tunnel status",
		});
		expect(within(status).getByText("Connecting")).toBeInTheDocument();
		expect(
			within(status).getByText("No connection to Cloudflare yet"),
		).toBeInTheDocument();
		expect(status).toHaveTextContent("1 hostname → http://traefik:80");
		expect(status).not.toHaveTextContent("reported");
		expect(status).not.toHaveTextContent("synced");
		expect(
			within(status).queryByRole("button", { name: /Copy/ }),
		).not.toBeInTheDocument();
		expect(screen.getByText(/The connector is starting\./)).toBeInTheDocument();
	});

	test("an ingress error is shown as an alert in the status", async () => {
		stubApi({
			"GET /me": MEMBER,
			"GET /servers/:serverId/tunnel": {
				body: online({ ingressError: "ingress push rejected" }),
			},
		});
		await renderCard();
		const status = await screen.findByRole("region", {
			name: "Tunnel status",
		});
		expect(within(status).getByRole("alert")).toHaveTextContent(
			"ingress push rejected",
		);
	});

	test("a message on a needs-attention connector is an alert; on other states plain text", async () => {
		stubApi({
			"GET /me": MEMBER,
			"GET /servers/:serverId/tunnel": {
				body: online({
					state: "needs_attention",
					message: "cloudflared keeps restarting",
				}),
			},
		});
		const first = await renderCard();
		const status = await screen.findByRole("region", {
			name: "Tunnel status",
		});
		expect(within(status).getByRole("alert")).toHaveTextContent(
			"cloudflared keeps restarting",
		);
		first.unmount();

		stubApi({
			"GET /me": MEMBER,
			"GET /servers/:serverId/tunnel": {
				body: online({ state: "offline", message: "last seen 5m ago" }),
			},
		});
		await renderCard();
		const second = await screen.findByRole("region", {
			name: "Tunnel status",
		});
		expect(within(second).getByText("last seen 5m ago")).toBeInTheDocument();
		expect(within(second).queryByRole("alert")).not.toBeInTheDocument();
	});

	test("Re-sync posts to the sync endpoint and confirms", async () => {
		let key: string | null = null;
		const api = stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": { body: online() },
			"POST /servers/:serverId/tunnel/sync": (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json(online());
			},
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(await screen.findByRole("button", { name: "Re-sync" }));
		expect(await screen.findByText("Re-syncing")).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/servers/s1/tunnel/sync");
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		expect(api.unmatched).toEqual([]);
	});

	test("a failed re-sync shows the error under the card", async () => {
		stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": { body: online() },
			"POST /servers/:serverId/tunnel/sync": problem(
				502,
				"Cloudflare is unreachable",
				"upstream",
			),
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(await screen.findByRole("button", { name: "Re-sync" }));
		expect(
			await screen.findByText("Cloudflare is unreachable"),
		).toBeInTheDocument();
		expect(screen.queryByText("Re-syncing")).not.toBeInTheDocument();
	});

	test("Disable tunnel asks first, then sends DELETE without force", async () => {
		const api = stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": { body: online() },
			"DELETE /servers/:serverId/tunnel": {
				status: 202,
				body: online({ state: "removing" }),
			},
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(
			await screen.findByRole("button", { name: "Disable tunnel" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Disable the tunnel on edge-1?"),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Disable tunnel" }),
		);
		expect(await screen.findByText("Removing the tunnel")).toBeInTheDocument();
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.path).toBe("/servers/s1/tunnel");
		expect(del?.query.get("force")).toBe("false");
	});

	test("a failed disable shows the error in the dialog", async () => {
		stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": { body: online() },
			"DELETE /servers/:serverId/tunnel": problem(
				500,
				"could not delete the tunnel",
				"internal",
			),
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(
			await screen.findByRole("button", { name: "Disable tunnel" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Disable tunnel" }),
		);
		expect(
			(await screen.findAllByText("could not delete the tunnel")).length,
		).toBeGreaterThan(0);
		expect(screen.queryByText("Removing the tunnel")).not.toBeInTheDocument();
	});

	test("a stuck removal offers Retry removal and Force remove, not Re-sync", async () => {
		const api = stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": {
				body: online({
					state: "removing",
					message: "Cloudflare did not delete the tunnel",
				}),
			},
			"DELETE /servers/:serverId/tunnel": { status: 202, body: tunnel() },
		});
		const user = userEvent.setup();
		await renderCard();
		const force = await screen.findByRole("button", {
			name: "Force remove",
		});
		expect(screen.queryByRole("button", { name: "Re-sync" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Disable tunnel" })).toBeNull();
		expect(screen.getByRole("button", { name: "Retry removal" })).toBeEnabled();
		const status = screen.getByRole("region", { name: "Tunnel status" });
		expect(within(status).getByRole("alert")).toHaveTextContent(
			"Cloudflare did not delete the tunnel",
		);

		await user.click(force);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Force-remove the tunnel?"),
		).toBeInTheDocument();
		await user.click(
			within(dialog).getByRole("button", { name: "Force remove" }),
		);
		expect(await screen.findByText("Tunnel removed")).toBeInTheDocument();
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.query.get("force")).toBe("true");
	});

	test("Retry removal retries without force", async () => {
		const api = stubApi({
			"GET /me": ADMIN,
			"GET /servers/:serverId/tunnel": {
				body: online({ state: "removing", message: "" }),
			},
			"DELETE /servers/:serverId/tunnel": { status: 202, body: tunnel() },
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(
			await screen.findByRole("button", { name: "Retry removal" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Retry removing the tunnel?"),
		).toBeInTheDocument();
		// Without a teardown message there is nothing to force past.
		expect(screen.queryByRole("button", { name: "Force remove" })).toBeNull();
		await user.click(within(dialog).getByRole("button", { name: "Retry" }));
		expect(await screen.findByText("Removing the tunnel")).toBeInTheDocument();
		expect(
			api.calls.find((c) => c.method === "DELETE")?.query.get("force"),
		).toBe("false");
	});
});
