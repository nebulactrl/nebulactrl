import { describe, expect, it } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DashboardAccessView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DashboardAccessSection } from "./dashboard-access-section";

function access(over: Partial<DashboardAccessView> = {}): DashboardAccessView {
	return {
		allowCidrs: [],
		certificate: {
			method: "http-01",
			status: "unknown",
			message: "Served by Let's Encrypt.",
		},
		clientIp: "203.0.113.7",
		clientIpAllowed: true,
		dnsResolver: "",
		enabled: true,
		host: "nebula.example.com",
		proxyDeployRequired: false,
		proxyServerId: "srv-cp",
		resolverAvailable: [],
		source: "admin",
		updatedAt: null,
		viaDashboardRoute: true,
		...over,
	};
}

const PATH = "GET /instance/dashboard-access";
const PUT = "PUT /instance/dashboard-access";

const saveButton = () => screen.getByRole("button", { name: /^Save/ });

async function loaded(
	view: DashboardAccessView,
	extra: Parameters<typeof stubApi>[0] = {},
) {
	const api = stubApi({ [PATH]: { body: view }, ...extra });
	const rendered = await renderWithProviders(<DashboardAccessSection />);
	await screen.findByTestId("dashboard-access");
	return { api, ...rendered };
}

describe("DashboardAccessSection", () => {
	it("explains that a dashboard without a domain has nothing to restrict", async () => {
		await loaded(access({ enabled: false }));
		expect(screen.getByText(/served without a domain/)).toBeInTheDocument();
		expect(screen.getByText(/--domain nebula.example.com/)).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /^Save/ })).toBeNull();
	});

	it("shows an error with a retry when the settings do not load", async () => {
		const user = userEvent.setup();
		let calls = 0;
		stubApi({
			[PATH]: () => {
				calls++;
				return calls === 1
					? Response.json(
							{ detail: "x" },
							{
								status: 500,
								headers: { "Content-Type": "application/problem+json" },
							},
						)
					: Response.json(access());
			},
		});
		await renderWithProviders(<DashboardAccessSection />);
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Dashboard access did not load");
		await user.click(screen.getByRole("button", { name: /retry|try again/i }));
		expect(await screen.findByTestId("dashboard-access")).toBeInTheDocument();
		expect(calls).toBe(2);
	});

	it("shows the host, the open-to-everybody summary and the caller's address; nothing to save yet", async () => {
		await loaded(access());
		expect(screen.getByText("nebula.example.com")).toBeInTheDocument();
		expect(screen.getByText(/No restriction: everybody/)).toBeInTheDocument();
		expect(screen.getByText("203.0.113.7")).toBeInTheDocument();
		expect(screen.getByText("Served by Let's Encrypt.")).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
		expect(
			screen.queryByRole("button", { name: "Discard changes" }),
		).toBeNull();
		expect(screen.queryByTestId("dashboard-lockout")).toBeNull();
	});

	it("says the allow list does not apply when connected directly, and offers no current IP", async () => {
		await loaded(access({ viaDashboardRoute: false, clientIp: "" }));
		expect(
			screen.getByText(/connected directly to the control plane/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Add my current IP" }),
		).toBeDisabled();
	});

	it("saves a list that includes the caller without a lockout prompt", async () => {
		const user = userEvent.setup();
		let current = access();
		const { api } = await loaded(current, {
			[PUT]: (req) =>
				req.json().then((body: { allowCidrs: string[] }) => {
					current = access({ allowCidrs: body.allowCidrs });
					return Response.json(current);
				}),
			[PATH]: () => Response.json(current),
		});
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(
			screen.getByLabelText("Allowed network 1"),
			"203.0.113.0/24",
		);
		expect(
			screen.getByText(/Only this network may open the dashboard/),
		).toBeInTheDocument();
		expect(screen.queryByTestId("dashboard-lockout")).toBeNull();
		await user.click(saveButton());
		expect(
			await screen.findByText("Dashboard access saved"),
		).toBeInTheDocument();
		expect(screen.getByText("The new rules are live.")).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.body).toEqual({
			allowCidrs: ["203.0.113.0/24"],
			dnsResolver: "",
		});
		// The saved list is now the baseline: nothing left to save.
		await waitFor(() => expect(saveButton()).toBeDisabled());
	});

	it("blocks a list that leaves the caller out until the lockout is confirmed, then sends confirmLockout", async () => {
		const user = userEvent.setup();
		const { api } = await loaded(access(), {
			[PUT]: { body: access({ allowCidrs: ["10.0.0.0/8"] }) },
		});
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(screen.getByLabelText("Allowed network 1"), "10.0.0.0/8");
		const warning = await screen.findByTestId("dashboard-lockout");
		expect(warning).toHaveTextContent("This list locks you out");
		expect(warning).toHaveTextContent("203.0.113.7");
		const lockedOut = screen.getByRole("button", {
			name: "Save and lock myself out",
		});
		expect(lockedOut).toBeDisabled();
		await user.click(screen.getByRole("checkbox"));
		expect(lockedOut).toBeEnabled();
		await user.click(lockedOut);
		await screen.findByText("Dashboard access saved");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			allowCidrs: ["10.0.0.0/8"],
			dnsResolver: "",
			confirmLockout: true,
		});
	});

	it("withdraws the confirmation when the list changes again", async () => {
		const user = userEvent.setup();
		await loaded(access());
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(screen.getByLabelText("Allowed network 1"), "10.0.0.0/8");
		await user.click(await screen.findByRole("checkbox"));
		expect(screen.getByRole("checkbox")).toBeChecked();
		await user.type(screen.getByLabelText("Allowed network 1"), "0");
		expect(screen.getByRole("checkbox")).not.toBeChecked();
		expect(
			screen.getByRole("button", { name: "Save and lock myself out" }),
		).toBeDisabled();
	});

	it("does not treat a direct connection as a lockout", async () => {
		const user = userEvent.setup();
		await loaded(access({ viaDashboardRoute: false, clientIp: "" }));
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(screen.getByLabelText("Allowed network 1"), "10.0.0.0/8");
		expect(screen.queryByTestId("dashboard-lockout")).toBeNull();
		expect(saveButton()).toBeEnabled();
	});

	it("Add my current IP adds the caller's address once", async () => {
		const user = userEvent.setup();
		await loaded(
			access({ allowCidrs: ["10.0.0.0/8"], clientIp: "198.51.100.4" }),
		);
		const add = screen.getByRole("button", { name: "Add my current IP" });
		expect(add).toBeEnabled();
		await user.click(add);
		expect(screen.getByLabelText("Allowed network 2")).toHaveValue(
			"198.51.100.4",
		);
		expect(add).toBeDisabled();
		expect(screen.queryByTestId("dashboard-lockout")).toBeNull();
	});

	it("Tailnet only fills the Tailscale ranges; Allow everybody empties the list", async () => {
		const user = userEvent.setup();
		await loaded(access());
		await user.click(screen.getByRole("button", { name: "Tailnet only" }));
		expect(screen.getByLabelText("Allowed network 1")).toHaveValue(
			"100.64.0.0/10",
		);
		expect(screen.getByLabelText("Allowed network 2")).toHaveValue(
			"fd7a:115c:a1e0::/48",
		);
		expect(screen.getByText(/these 2 networks/)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Allow everybody" }));
		expect(screen.queryByLabelText("Allowed network 1")).toBeNull();
		expect(screen.getByText(/No restriction/)).toBeInTheDocument();
	});

	it("flags an invalid network and refuses to save it", async () => {
		const user = userEvent.setup();
		const { api } = await loaded(
			access({ viaDashboardRoute: false, clientIp: "" }),
		);
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(screen.getByLabelText("Allowed network 1"), "not-an-ip");
		expect(screen.getByLabelText("Allowed network 1")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(saveButton()).toBeDisabled();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	it("Discard changes restores the saved list", async () => {
		const user = userEvent.setup();
		await loaded(access({ allowCidrs: ["203.0.113.7"] }));
		await user.click(screen.getByRole("button", { name: "Allow everybody" }));
		expect(saveButton()).toBeEnabled();
		await user.click(screen.getByRole("button", { name: "Discard changes" }));
		expect(screen.getByLabelText("Allowed network 1")).toHaveValue(
			"203.0.113.7",
		);
		expect(saveButton()).toBeDisabled();
		expect(
			screen.queryByRole("button", { name: "Discard changes" }),
		).toBeNull();
	});

	it("shows the server's refusal under Not saved", async () => {
		const user = userEvent.setup();
		await loaded(access(), {
			[PUT]: problem(409, {
				code: "dashboard_lockout",
				detail: "That list would lock you out.",
			}),
		});
		await user.click(screen.getByRole("button", { name: "Tailnet only" }));
		await user.click(screen.getByRole("checkbox"));
		await user.click(
			screen.getByRole("button", { name: "Save and lock myself out" }),
		);
		const alert = await screen.findByText("That list would lock you out.");
		expect(alert.closest("[role=alert]")).toHaveTextContent("Not saved");
		expect(screen.queryByText("Dashboard access saved")).toBeNull();
	});

	it("without a DNS-01 resolver says where to add one and links to the proxy page", async () => {
		await loaded(access());
		expect(
			screen.getByText(/No DNS-01 resolver on the control-plane server yet/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Servers, Proxy" }),
		).toHaveAttribute("href", "/servers/srv-cp/proxy");
	});

	it("names the proxy page without a link when the control-plane server is unknown", async () => {
		await loaded(access({ proxyServerId: "" }));
		expect(screen.queryByRole("link", { name: "Servers, Proxy" })).toBeNull();
		expect(
			screen.getByText(/Servers, Proxy, deploy the proxy/),
		).toBeInTheDocument();
	});

	it("switches to a DNS-01 resolver and says the proxy must be deployed", async () => {
		const user = userEvent.setup();
		const resolvers = [
			{ provider: "cloudflare", providerName: "Cloudflare", applied: false },
		];
		let current = access({ resolverAvailable: resolvers });
		const { api } = await loaded(current, {
			[PUT]: () => {
				current = access({
					dnsResolver: "cloudflare",
					proxyDeployRequired: true,
					resolverAvailable: resolvers,
				});
				return Response.json(current);
			},
			[PATH]: () => Response.json(current),
		});
		expect(screen.queryByText(/No DNS-01 resolver/)).toBeNull();
		await user.click(screen.getByRole("button", { name: "DNS-01 Cloudflare" }));
		expect(
			screen.getByText(
				/keeps its HTTP-01 certificate until the proxy runs the resolver/,
			),
		).toBeInTheDocument();
		await user.click(saveButton());
		expect(
			await screen.findByText(
				"The allow list is live. The DNS-01 certificate waits for Deploy proxy.",
			),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			allowCidrs: [],
			dnsResolver: "cloudflare",
		});
		const deploy = await screen.findByTestId("dashboard-deploy-proxy");
		expect(deploy).toHaveTextContent(
			"The cloudflare resolver is staged, not running",
		);
		expect(deploy.querySelector("a")).toHaveAttribute(
			"href",
			"/servers/srv-cp/proxy",
		);
	});

	it("keeps a resolver the server lost visible but not selectable", async () => {
		await loaded(access({ dnsResolver: "route53" }));
		const lost = screen.getByRole("button", {
			name: "DNS-01 route53 (resolver missing)",
		});
		expect(lost).toBeDisabled();
	});
});
