import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	ProxyRoute,
	ServerCertificate,
	ServerProxy,
} from "#/api/gen/types.gen";
import { type ApiReply, stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import type { ServerView } from "../model";
import { ProxyTab } from "./proxy-tab";

const server = { id: "srv-1", name: "edge-1" } as ServerView;

const off = {
	allowHostGateway: false,
	allowLinkLocal: false,
	allowLoopback: false,
};

const proxy = (over: Partial<ServerProxy> = {}): ServerProxy => ({
	agentTooOld: false,
	appliedGeneration: 3,
	generation: 3,
	appliedUpstreamPolicy: off,
	upstreamPolicy: off,
	capabilities: null,
	certificates: null,
	error: "",
	l4Ports: null,
	ports: {
		agentHttp: 80,
		agentHttps: 443,
		appliedHttp: 80,
		appliedHttps: 443,
		busyOther: null,
		http: 80,
		https: 443,
		reported: true,
		setHttp: null,
		setHttps: null,
		source: "flag",
		staged: false,
		supported: true,
	},
	staged: false,
	status: "ok",
	tailnet: {
		appliedMode: "off",
		hostTailscale: { detected: false, ips: null },
		hostname: "",
		integrationConfigured: false,
		magicDns: false,
		mode: "off",
	},
	...over,
});

const route = (over: Partial<ProxyRoute> = {}): ProxyRoute => ({
	serviceId: "svc-1",
	serviceName: "checkout",
	kind: "service",
	protocol: "http",
	hosts: [
		{
			host: "shop.example.com",
			pathPrefix: "",
			kind: "serve",
			tlsMode: "auto",
			accessList: false,
			basicAuth: false,
			rateLimit: false,
		},
	],
	upstreams: ["10.0.0.2:8080"],
	allowCidrs: null,
	denyCidrs: null,
	...over,
});

const cert = (): ServerCertificate => ({
	id: "cert-1",
	serverId: "srv-1",
	name: "origin-cert",
	status: "active",
	change: "none",
	domains: ["shop.example.com"],
	fingerprint: "aabbccddeeff",
	issuer: "Acme CA",
	notBefore: new Date(Date.now() - 86_400_000).toISOString(),
	notAfter: new Date(Date.now() + 90 * 86_400_000).toISOString(),
	createdAt: "2026-01-01T00:00:00Z",
	appliedAt: "2026-01-02T00:00:00Z",
	daysToExpiry: 90,
	statusMessage: "",
});

const down = {
	status: 500,
	body: { status: 500, title: "Error", detail: "down", code: "internal" },
};

/**
 * The tab's own two endpoints are stubbed per test; the tunnel, DNS-01 and
 * tailnet cards (tested on their own) just get an error so they stay out of
 * the way.
 */
function stubTab(own: { proxy: ApiReply; routes?: ApiReply }) {
	return stubApi({
		"GET /servers/:serverId/proxy": own.proxy,
		"GET /servers/:serverId/proxy/routes": own.routes ?? {
			body: { items: [] },
		},
		"GET /me": {
			body: {
				id: "u",
				name: "Ada",
				email: "a@example.com",
				instanceAdmin: false,
				workspaceId: "w",
				createdAt: "2026-01-01T00:00:00Z",
				principal: { type: "user", label: "Ada", scopes: [] },
			},
		},
		"GET /servers/:serverId/tunnel": down,
		"GET /servers/:serverId/dns01": down,
		"GET /dns/credentials": down,
		"GET /dns/providers": down,
		"GET /integrations/tailscale": down,
		"POST /servers/:serverId/proxy/plan": down,
	});
}

describe("ProxyTab", () => {
	test("shows a loading placeholder until the proxy answers", async () => {
		const gate = Promise.withResolvers<Response>();
		stubTab({ proxy: () => gate.promise });
		await renderWithProviders(<ProxyTab server={server} />);
		expect(
			screen.getByRole("status", { name: "Loading proxy" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("heading", { name: "Routes" }),
		).not.toBeInTheDocument();
		gate.resolve(Response.json(proxy()));
		expect(
			await screen.findByRole("heading", { name: /^Routes/ }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("status", { name: "Loading proxy" }),
		).not.toBeInTheDocument();
	});

	test("a failed load offers a retry that recovers", async () => {
		let attempts = 0;
		const api = stubTab({
			proxy: () => {
				attempts += 1;
				return attempts === 1
					? Response.json(down.body, { status: 500 })
					: Response.json(proxy());
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProxyTab server={server} />);
		expect(
			await screen.findByText("The proxy did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"The control plane did not answer, so nothing was changed.",
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("heading", { name: /^Routes/ }),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(
			await screen.findByRole("heading", { name: /^Routes/ }),
		).toBeInTheDocument();
		expect(
			screen.queryByText("The proxy did not load"),
		).not.toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.path === "/servers/srv-1/proxy"),
		).toHaveLength(2);
	});

	test("lays out every proxy section under its own heading", async () => {
		stubTab({ proxy: { body: proxy() } });
		await renderWithProviders(<ProxyTab server={server} />);
		await screen.findByRole("heading", { name: /^Routes/ });
		for (const [id, title] of [
			["proxy-status", "Proxy"],
			["proxy-ports", "Proxy ports"],
			["proxy-certificates", "Certificates"],
			["proxy-tunnel", "Cloudflare Tunnel"],
			["proxy-dns01", "DNS-01 certificates"],
			["proxy-policy", "Upstream policy"],
			["proxy-tailnet", "Tailnet"],
			["proxy-routes", "Routes"],
		] as const) {
			const section = document.getElementById(id);
			expect(section?.tagName).toBe("SECTION");
			expect(section).toHaveAttribute("aria-labelledby", `${id}-title`);
			expect(
				within(section as HTMLElement).getByRole("heading", { level: 2 }),
			).toHaveTextContent(new RegExp(`^${title}`));
		}
		expect(
			screen.getByText(
				"Traefik on this server, and the TCP/UDP ports it publishes.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText("Acts at once; it is not staged for Deploy proxy."),
		).toBeInTheDocument();
	});

	test.each([
		["ok", "Running"],
		["starting", "Starting"],
		["error", "Error"],
		["", "Not reported"],
		["something-new", "Not reported"],
	])("proxy status %p reads %p", async (status, label) => {
		stubTab({ proxy: { body: proxy({ status }) } });
		await renderWithProviders(<ProxyTab server={server} />);
		await screen.findByRole("heading", { name: /^Routes/ });
		const section = document.getElementById("proxy-status");
		expect(within(section as HTMLElement).getByText(label)).toBeInTheDocument();
	});

	test("shows the agent's error text and the staged badge", async () => {
		stubTab({
			proxy: {
				body: proxy({
					error: "port 443 is in use",
					staged: true,
					generation: 4,
				}),
			},
		});
		await renderWithProviders(<ProxyTab server={server} />);
		expect(await screen.findByText("port 443 is in use")).toBeInTheDocument();
		expect(screen.getByText("Staged changes")).toBeInTheDocument();
	});

	test("without errors or staged changes neither is shown", async () => {
		stubTab({ proxy: { body: proxy() } });
		await renderWithProviders(<ProxyTab server={server} />);
		await screen.findByRole("heading", { name: /^Routes/ });
		expect(screen.queryByText("Staged changes")).not.toBeInTheDocument();
		expect(screen.queryByText("port 443 is in use")).not.toBeInTheDocument();
	});

	test("shows the applied generation, and the staged one while a deploy is pending", async () => {
		stubTab({
			proxy: {
				body: proxy({ appliedGeneration: 3, generation: 5, staged: true }),
			},
		});
		await renderWithProviders(<ProxyTab server={server} />);
		expect(await screen.findByText("generation 3 → 5")).toBeInTheDocument();
	});

	test("shows only the applied generation when nothing is newer", async () => {
		stubTab({
			proxy: { body: proxy({ appliedGeneration: 7, generation: 7 }) },
		});
		await renderWithProviders(<ProxyTab server={server} />);
		expect(await screen.findByText("generation 7")).toBeInTheDocument();
	});

	test("lists the published TCP/UDP ports, or says none are published", async () => {
		stubTab({
			proxy: {
				body: proxy({
					l4Ports: [
						{
							port: 5432,
							protocol: "tcp",
							serviceId: "svc-db",
							serviceName: "postgres",
							deployed: true,
						},
					],
				}),
			},
		});
		await renderWithProviders(<ProxyTab server={server} />);
		const row = await screen.findByRole("row", { name: /postgres/ });
		expect(within(row).getByText("5432")).toBeInTheDocument();
		expect(within(row).getByText("Listening")).toBeInTheDocument();
	});

	test("no published ports shows the hint", async () => {
		stubTab({ proxy: { body: proxy({ l4Ports: null }) } });
		await renderWithProviders(<ProxyTab server={server} />);
		expect(
			await screen.findByText(/No TCP or UDP ports are published/),
		).toBeInTheDocument();
	});

	test("an agent that is too old shows the upgrade banner naming the server", async () => {
		stubTab({ proxy: { body: proxy({ agentTooOld: true }) } });
		await renderWithProviders(<ProxyTab server={server} />);
		await screen.findByRole("heading", { name: /^Routes/ });
		const first =
			document.querySelector('[data-tab="proxy"]')?.firstElementChild;
		expect(first).toHaveAttribute("data-testid", "agent-too-old");
		expect(first).toHaveTextContent("The agent on edge-1 is too old for this");
		expect(screen.getByRole("button", { name: "Upload" })).toBeDisabled();
	});

	test("a current agent shows no banner and can upload certificates", async () => {
		stubTab({ proxy: { body: proxy() } });
		await renderWithProviders(<ProxyTab server={server} />);
		await screen.findByRole("heading", { name: /^Routes/ });
		expect(screen.queryByText(/is too old for this/)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Upload" })).toBeEnabled();
	});

	test("passes the server's certificates to the certificates section", async () => {
		stubTab({ proxy: { body: proxy({ certificates: [cert()] }) } });
		await renderWithProviders(<ProxyTab server={server} />);
		const section = (
			await screen.findByRole("heading", { name: "Certificates" })
		).closest("section");
		expect(
			within(section as HTMLElement).getByText("origin-cert"),
		).toBeInTheDocument();
	});

	test("passes desired and applied upstream policy to the policy section", async () => {
		stubTab({
			proxy: {
				body: proxy({
					upstreamPolicy: { ...off, allowHostGateway: true },
					appliedUpstreamPolicy: off,
					staged: true,
					generation: 4,
				}),
			},
		});
		await renderWithProviders(<ProxyTab server={server} />);
		const host = await screen.findByRole("switch", {
			name: "Reach services on the host",
		});
		expect(host).toBeChecked();
		expect(host.closest("li")).toHaveTextContent("Staged");
		expect(
			screen.getByRole("switch", { name: "Allow loopback upstreams" }),
		).not.toBeChecked();
	});

	test("the routes section lists what the proxy serves", async () => {
		const api = stubTab({
			proxy: { body: proxy() },
			routes: { body: { items: [route()] } },
		});
		await renderWithProviders(<ProxyTab server={server} />);
		const row = await screen.findByRole("row", { name: /checkout/ });
		expect(within(row).getByText("shop.example.com")).toBeInTheDocument();
		expect(within(row).getByText("10.0.0.2:8080")).toBeInTheDocument();
		expect(
			api.calls.some((c) => c.path === "/servers/srv-1/proxy/routes"),
		).toBe(true);
	});

	test("the routes section says so when nothing is routed", async () => {
		stubTab({ proxy: { body: proxy() }, routes: { body: { items: [] } } });
		await renderWithProviders(<ProxyTab server={server} />);
		expect(
			await screen.findByText("Nothing is routed through this proxy yet."),
		).toBeInTheDocument();
	});

	test("routes still loading do not hold back the rest of the tab", async () => {
		const gate = Promise.withResolvers<Response>();
		stubTab({ proxy: { body: proxy() }, routes: () => gate.promise });
		await renderWithProviders(<ProxyTab server={server} />);
		await screen.findByRole("heading", { name: /^Routes/ });
		expect(
			screen.getByRole("heading", { name: "Certificates" }),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Nothing is routed through this proxy yet."),
		).not.toBeInTheDocument();
		gate.resolve(Response.json({ items: [route()] }));
		expect(
			await screen.findByRole("row", { name: /checkout/ }),
		).toBeInTheDocument();
	});

	test("a routes failure shows its own error with a retry, leaving the rest intact", async () => {
		let attempts = 0;
		const api = stubTab({
			proxy: { body: proxy() },
			routes: () => {
				attempts += 1;
				return attempts === 1
					? Response.json(down.body, { status: 500 })
					: Response.json({ items: [route()] });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProxyTab server={server} />);
		expect(await screen.findByText("Routes did not load")).toBeInTheDocument();
		expect(
			screen.getByText("The control plane did not answer."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "Certificates" }),
		).toBeInTheDocument();
		const routes = document.getElementById("proxy-routes") as HTMLElement;
		await user.click(
			within(routes).getByRole("button", { name: /try again|retry/i }),
		);
		expect(
			await screen.findByRole("row", { name: /checkout/ }),
		).toBeInTheDocument();
		expect(screen.queryByText("Routes did not load")).not.toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.path === "/servers/srv-1/proxy/routes"),
		).toHaveLength(2);
	});

	test("staged changes bring up the deploy bar; a settled proxy has none", async () => {
		stubTab({ proxy: { body: proxy({ staged: true, generation: 4 }) } });
		await renderWithProviders(<ProxyTab server={server} />);
		expect(
			await screen.findByRole("region", { name: "Deploy proxy" }),
		).toBeInTheDocument();
	});

	test("a settled proxy shows no deploy bar", async () => {
		stubTab({ proxy: { body: proxy() } });
		await renderWithProviders(<ProxyTab server={server} />);
		await screen.findByRole("heading", { name: /^Routes/ });
		expect(
			screen.queryByRole("region", { name: "Deploy proxy" }),
		).not.toBeInTheDocument();
	});
});
