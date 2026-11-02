import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { RouteConfig, ServiceView } from "#/api/gen/types.gen";
import { toService } from "#/api/models";
import { type ApiReply, type ApiRoutes, stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { FIREWALL_NOTE, RESTART_WARNING, UDP_WARNING } from "../lib/l4";
import { domainView, routeView } from "./route-fixtures";
import { RouteSettingsTab } from "./route-settings";

const SERVICES = "GET /environments/:environmentId/services";
const PORTS = "GET /servers/:serverId/ports";
const PROXY = "GET /servers/:serverId/proxy";
const PENDING = "GET /services/:serviceId/pending-changes";
const PATCH = "PATCH /routes/:routeId";

interface Setup {
	section?: string;
	siblings?: ServiceView[];
	ports?: {
		protocol: "tcp" | "udp";
		port: number;
		serviceId: string;
		serviceName: string;
	}[];
	patch?: ApiReply;
	extra?: ApiRoutes;
}

function Harness({
	view,
	initial,
	onSection,
}: {
	view: ServiceView;
	initial: string | undefined;
	onSection: (section: string) => void;
}) {
	const [section, setSection] = useState(initial);
	return (
		<RouteSettingsTab
			service={toService(view)}
			section={section}
			onSectionChange={(next) => {
				onSection(next);
				setSection(next);
			}}
		/>
	);
}

async function renderSettings(view: ServiceView, setup: Setup = {}) {
	const sections: string[] = [];
	const patches: { ifMatch: string | null; body: unknown }[] = [];
	const api = stubApi({
		[SERVICES]: {
			body: make("PageServiceView", { items: setup.siblings ?? [view] }),
		},
		[PORTS]: {
			body: make("PortsOutputBody", {
				items: (setup.ports ?? []).map((port) => ({
					...port,
					deployed: true,
					projectId: "prj-1",
				})),
			}),
		},
		// No tailnet integration: the device picker renders nothing.
		[PROXY]: {
			body: make("ServerProxy", {
				lastDeploy: undefined,
				tailnet: { integrationConfigured: false, mode: "off" },
			}),
		},
		[PENDING]: { body: { items: [] } },
		// The add-domain form asks whether Cloudflare DNS is connected.
		"GET /integrations/cloudflare": {
			body: make("CloudflareConnectionView", {
				configured: false,
				accounts: [],
			}),
		},
		[PATCH]:
			setup.patch ??
			(async (request) => {
				patches.push({
					ifMatch: request.headers.get("If-Match"),
					body: await request.clone().json(),
				});
				return new Response(null, { status: 204 });
			}),
		...setup.extra,
	});
	const { queryClient } = await renderWithProviders(
		<Harness
			view={view}
			initial={setup.section}
			onSection={(section) => sections.push(section)}
		/>,
	);
	await waitFor(() => expect(queryClient.isFetching()).toBe(0));
	return { api, sections, patches };
}

function tcpView(
	route: Overrides<RouteConfig> = {},
	overrides: Overrides<ServiceView> = {},
) {
	return routeView(overrides, {
		protocol: "tcp",
		listenPort: 25565,
		allowCidrs: ["10.0.0.0/8"],
		upstreams: [{ address: "192.168.1.30:25565" }],
		...route,
	});
}

const saveButton = () => screen.getByRole("button", { name: "Save changes" });
const discardButton = () => screen.getByRole("button", { name: "Discard" });
const status = () =>
	screen.getByText(/All changes saved|Unsaved changes|Fix the highlighted/);

describe("RouteSettingsTab: save bar", () => {
	test("starts clean: everything saved, nothing to save or discard", async () => {
		await renderSettings(routeView());
		expect(screen.getByText("All changes saved")).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
		expect(discardButton()).toBeDisabled();
	});

	test("an edit makes the bar dirty and Discard restores the saved value", async () => {
		await renderSettings(routeView());
		const user = userEvent.setup();
		const name = screen.getByLabelText("Name");
		await user.clear(name);
		await user.type(name, "ha");
		expect(status()).toHaveTextContent("Unsaved changes");
		expect(saveButton()).toBeEnabled();
		await user.click(discardButton());
		expect(screen.getByLabelText("Name")).toHaveValue("homeassistant");
		expect(status()).toHaveTextContent("All changes saved");
	});

	test("an empty name is flagged and cannot be saved", async () => {
		await renderSettings(routeView());
		const user = userEvent.setup();
		await user.clear(screen.getByLabelText("Name"));
		expect(screen.getByText("Give the route a name.")).toBeInTheDocument();
		expect(status()).toHaveTextContent("Fix the highlighted fields to save.");
		expect(saveButton()).toBeDisabled();
		expect(screen.getByLabelText("Name")).toBeInvalid();
	});

	test("saves only what changed, with If-Match set to the opened generation", async () => {
		const { patches, api } = await renderSettings(routeView());
		const user = userEvent.setup();
		const name = screen.getByLabelText("Name");
		await user.clear(name);
		await user.type(name, "ha-prod");
		await user.click(saveButton());
		expect(await screen.findByText("Route saved")).toBeInTheDocument();
		expect(patches).toEqual([{ ifMatch: '"7"', body: { name: "ha-prod" } }]);
		expect(api.unmatched).toEqual([]);
	});

	test("after a save the form follows the service again", async () => {
		await renderSettings(routeView());
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Name"), "x");
		await user.click(saveButton());
		await screen.findByText("Route saved");
		await waitFor(() => expect(saveButton()).toBeDisabled());
		expect(status()).toHaveTextContent("All changes saved");
	});
});

describe("RouteSettingsTab: save errors", () => {
	test("shows the server's reason and keeps the edit", async () => {
		await renderSettings(routeView(), {
			patch: {
				status: 422,
				body: {
					status: 422,
					code: "name_taken",
					detail: "That name is taken.",
				},
			},
		});
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Name"), "x");
		await user.click(saveButton());
		expect(await screen.findByText("That name is taken.")).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("homeassistantx");
		expect(saveButton()).toBeEnabled();
	});

	test("a port problem jumps to Networking and shows under the port field", async () => {
		const { sections } = await renderSettings(tcpView(), {
			section: "general",
			patch: {
				status: 422,
				body: {
					status: 422,
					code: "port_in_use",
					detail: "Port 25570 is in use by another process.",
				},
			},
		});
		const user = userEvent.setup();
		const port = screen.getByLabelText("Port on the server");
		await user.clear(port);
		await user.type(port, "25570");
		await user.click(saveButton());
		await waitFor(() => expect(sections).toContain("networking"));
		const messages = await screen.findAllByText(
			"Port 25570 is in use by another process.",
		);
		expect(messages.length).toBeGreaterThanOrEqual(1);
		expect(port).toBeInvalid();
	});

	test("an upstream problem jumps to Networking and shows under the upstream list", async () => {
		const { sections } = await renderSettings(routeView(), {
			section: "general",
			patch: {
				status: 422,
				body: {
					status: 422,
					code: "upstream_denied",
					detail: "That address is not allowed.",
				},
			},
		});
		const user = userEvent.setup();
		const address = screen.getByLabelText("Upstream address 1");
		await user.clear(address);
		await user.type(address, "192.168.1.21:8123");
		await user.click(saveButton());
		// Under the upstream list and in the save bar.
		await waitFor(() =>
			expect(screen.getAllByText("That address is not allowed.")).toHaveLength(
				2,
			),
		);
		expect(sections).toContain("networking");
	});

	test("an unrelated error does not move the user to another category", async () => {
		const { sections } = await renderSettings(routeView(), {
			section: "general",
			patch: {
				status: 500,
				body: { status: 500, code: "boom", detail: "Something broke." },
			},
		});
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Name"), "x");
		await user.click(saveButton());
		expect(await screen.findByText("Something broke.")).toBeInTheDocument();
		expect(sections).toEqual([]);
	});

	test("a lost race drops the edit and shows no error", async () => {
		await renderSettings(routeView(), {
			patch: {
				status: 412,
				body: {
					status: 412,
					code: "generation_conflict",
					detail: "Someone else changed this service.",
				},
			},
		});
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Name"), "x");
		await user.click(saveButton());
		await waitFor(() =>
			expect(screen.getByLabelText("Name")).toHaveValue("homeassistant"),
		);
		expect(screen.queryByText("Someone else changed this service.")).toBeNull();
		expect(status()).toHaveTextContent("All changes saved");
	});

	test("an agent that is too old gets the upgrade banner instead of an error line", async () => {
		await renderSettings(routeView(), {
			patch: {
				status: 422,
				body: { status: 422, code: "agent_too_old", detail: "Agent too old." },
			},
		});
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Name"), "x");
		await user.click(saveButton());
		const banner = await screen.findByTestId("agent-too-old");
		expect(banner).toHaveTextContent("The agent on edge-1 is too old for this");
		expect(screen.queryByText("Agent too old.")).toBeNull();
		expect(status()).toHaveTextContent("Unsaved changes");
	});
});

describe("RouteSettingsTab: http upstreams", () => {
	test("edits an address and sends the replaced upstream list", async () => {
		const { patches } = await renderSettings(routeView());
		const user = userEvent.setup();
		const address = screen.getByLabelText("Upstream address 1");
		await user.clear(address);
		await user.type(address, "192.168.1.21:8123");
		await user.click(saveButton());
		await screen.findByText("Route saved");
		expect(patches[0]?.body).toEqual({
			upstreams: [{ address: "192.168.1.21:8123" }],
		});
	});

	test.each([
		["", "Enter host:port."],
		["myhost", "Use host:port, e.g. 192.168.1.20:8080."],
		["myhost:99999", "The port must be between 1 and 65535."],
		["fd00::1:80", "Put IPv6 addresses in brackets: [fd7a::1]:80."],
	])(
		"rejects the address %j with a specific message",
		async (value, message) => {
			await renderSettings(routeView());
			const user = userEvent.setup();
			const address = screen.getByLabelText("Upstream address 1");
			await user.clear(address);
			if (value) await user.type(address, value);
			expect(screen.getByText(message)).toBeInTheDocument();
			expect(address).toBeInvalid();
			expect(saveButton()).toBeDisabled();
		},
	);

	test("adds a second upstream, which then can be removed", async () => {
		const { patches } = await renderSettings(routeView());
		const user = userEvent.setup();
		expect(
			screen.queryByRole("button", { name: /Remove upstream/ }),
		).toBeNull();
		await user.click(screen.getByRole("button", { name: "Add upstream" }));
		const second = screen.getByLabelText("Upstream address 2");
		expect(second).toHaveValue("");
		// An empty row is invalid until filled in.
		expect(saveButton()).toBeDisabled();
		await user.type(second, "192.168.1.22:8123");
		await user.click(saveButton());
		await screen.findByText("Route saved");
		expect(patches[0]?.body).toEqual({
			upstreams: [
				{ address: "192.168.1.20:8123" },
				{ address: "192.168.1.22:8123" },
			],
		});
	});

	test("removing a row of two leaves the other one", async () => {
		await renderSettings(
			routeView(
				{},
				{ upstreams: [{ address: "10.0.0.1:80" }, { address: "10.0.0.2:80" }] },
			),
		);
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: "Remove upstream 1" }));
		expect(screen.getByLabelText("Upstream address 1")).toHaveValue(
			"10.0.0.2:80",
		);
		expect(screen.queryByLabelText("Upstream address 2")).toBeNull();
		expect(
			screen.queryByRole("button", { name: /Remove upstream/ }),
		).toBeNull();
	});

	test("stops offering Add upstream at the limit of 20", async () => {
		const upstreams = Array.from({ length: 20 }, (_, i) => ({
			address: `10.0.0.${i + 1}:80`,
		}));
		await renderSettings(routeView({}, { upstreams }));
		expect(screen.queryByRole("button", { name: "Add upstream" })).toBeNull();
		expect(screen.getByLabelText("Upstream address 20")).toBeInTheDocument();
	});

	test("a managed upstream shows its service name and edits only the port", async () => {
		const target = routeView({ id: "svc-api", name: "api", kind: "app" });
		const { patches } = await renderSettings(
			routeView({}, { upstreams: [{ serviceId: "svc-api", port: 3000 }] }),
			{ siblings: [target] },
		);
		expect(await screen.findByText("api")).toBeInTheDocument();
		const user = userEvent.setup();
		const port = screen.getByLabelText("Port of api");
		await user.clear(port);
		await user.type(port, "8080");
		await user.click(saveButton());
		await screen.findByText("Route saved");
		expect(patches[0]?.body).toEqual({
			upstreams: [{ serviceId: "svc-api", port: 8080 }],
		});
	});

	test("a managed upstream port outside 1-65535 is rejected", async () => {
		const target = routeView({ id: "svc-api", name: "api", kind: "app" });
		await renderSettings(
			routeView({}, { upstreams: [{ serviceId: "svc-api", port: 3000 }] }),
			{ siblings: [target] },
		);
		const user = userEvent.setup();
		const port = await screen.findByLabelText("Port of api");
		await user.clear(port);
		await user.type(port, "70000");
		expect(screen.getByText("Use a port from 1 to 65535.")).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
	});

	test("an unknown managed service is named generically", async () => {
		await renderSettings(
			routeView({}, { upstreams: [{ serviceId: "svc-gone", port: 3000 }] }),
		);
		expect(screen.getByText("Managed service")).toBeInTheDocument();
		expect(screen.getByLabelText("Port of the service")).toHaveValue("3000");
	});
});

describe("RouteSettingsTab: http options", () => {
	test("https reveals the TLS skip option, which warns and saves", async () => {
		const { patches } = await renderSettings(routeView());
		const user = userEvent.setup();
		expect(screen.queryByText("Skip TLS verification")).toBeNull();
		await user.click(screen.getByRole("button", { name: "HTTPS" }));
		await user.click(
			screen.getByRole("checkbox", { name: "Skip TLS verification" }),
		);
		expect(
			screen.getByText(/accepts any certificate from this service/),
		).toBeInTheDocument();
		await user.click(saveButton());
		await screen.findByText("Route saved");
		expect(patches[0]?.body).toEqual({
			scheme: "https",
			insecureSkipVerify: true,
		});
	});

	test("switching back to http hides the TLS option again", async () => {
		await renderSettings(routeView({}, { scheme: "https" }));
		const user = userEvent.setup();
		expect(
			screen.getByRole("checkbox", { name: "Skip TLS verification" }),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "HTTP" }));
		expect(screen.queryByText("Skip TLS verification")).toBeNull();
	});

	test("the health path must start with a slash", async () => {
		await renderSettings(routeView());
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Health path"), "health");
		expect(screen.getByText("Start with /, e.g. /health.")).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
	});

	test("saves a health path, the host header and the verify checkbox", async () => {
		const { patches } = await renderSettings(routeView());
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Health path"), "/healthz");
		await user.click(
			screen.getByRole("checkbox", { name: "Pass the original Host header" }),
		);
		await user.click(
			screen.getByRole("checkbox", { name: "Skip the post-deploy check" }),
		);
		await user.click(saveButton());
		await screen.findByText("Route saved");
		expect(patches[0]?.body).toEqual({
			healthPath: "/healthz",
			passHost: false,
			verifySkip: true,
		});
	});

	test("lists the route's domains and an add form under Networking", async () => {
		await renderSettings(routeView({ domains: [domainView()] }), {
			extra: {
				"GET /servers": { body: { items: [] } },
				"GET /services/:serviceId/domains": { body: { items: [] } },
			},
		});
		const domains = screen.getByRole("region", { name: "Domains" });
		expect(within(domains).getByText("app.example.com")).toBeInTheDocument();
		expect(within(domains).queryByText(/No hostname yet/)).toBeNull();
	});

	test("invites adding a first hostname when there is none", async () => {
		await renderSettings(routeView());
		const domains = screen.getByRole("region", { name: "Domains" });
		expect(
			within(domains).getByText(/No hostname yet\. Add one/),
		).toBeInTheDocument();
	});

	test("has no port section on an http route", async () => {
		await renderSettings(routeView());
		expect(
			screen.queryByRole("region", { name: "Port and access" }),
		).toBeNull();
		expect(screen.queryByText(FIREWALL_NOTE)).toBeNull();
	});
});

describe("RouteSettingsTab: tcp", () => {
	test("shows the port, the firewall note and the PROXY protocol choice", async () => {
		await renderSettings(tcpView({ proxyProtocol: 1 }));
		expect(screen.getByLabelText("Port on the server")).toHaveValue("25565");
		expect(screen.getByText(FIREWALL_NOTE)).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "v1" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.queryByRole("region", { name: "Domains" })).toBeNull();
		expect(screen.getByRole("region", { name: "Target" })).toBeInTheDocument();
	});

	test("choosing PROXY protocol v2 is saved as 2", async () => {
		const { patches } = await renderSettings(tcpView());
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: "v2" }));
		await user.click(saveButton());
		await screen.findByText("Route saved");
		expect(patches[0]?.body).toEqual({ proxyProtocol: 2 });
	});

	test("changing the port warns about the proxy restart and saves the number", async () => {
		const { patches } = await renderSettings(tcpView());
		const user = userEvent.setup();
		expect(screen.queryByText(RESTART_WARNING)).toBeNull();
		const port = screen.getByLabelText("Port on the server");
		await user.clear(port);
		await user.type(port, "25570");
		expect(screen.getByText(RESTART_WARNING)).toBeInTheDocument();
		await user.click(saveButton());
		await screen.findByText("Route saved");
		expect(patches[0]?.body).toEqual({ listenPort: 25570 });
	});

	test.each([
		["80", "Choose a port between 1024 and 65535."],
		["9000", "Port 9000 is reserved for the proxy."],
		["70000", "Choose a port between 1024 and 65535."],
	])("rejects port %s", async (value, message) => {
		await renderSettings(tcpView());
		const user = userEvent.setup();
		const port = screen.getByLabelText("Port on the server");
		await user.clear(port);
		await user.type(port, value);
		expect(screen.getByText(message)).toBeInTheDocument();
		expect(port).toBeInvalid();
		expect(saveButton()).toBeDisabled();
		// An invalid port is not "a new port": no restart warning.
		expect(screen.queryByText(RESTART_WARNING)).toBeNull();
	});

	test("a port another service already publishes is refused by name", async () => {
		await renderSettings(tcpView(), {
			ports: [
				{
					protocol: "tcp",
					port: 25570,
					serviceId: "svc-other",
					serviceName: "mumble",
				},
			],
		});
		const user = userEvent.setup();
		const port = screen.getByLabelText("Port on the server");
		await user.clear(port);
		await user.type(port, "25570");
		expect(
			screen.getByText("TCP 25570 is already published by mumble."),
		).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
	});

	test("the route's own port does not clash with itself", async () => {
		await renderSettings(tcpView(), {
			ports: [
				{
					protocol: "tcp",
					port: 25565,
					serviceId: "svc-route",
					serviceName: "homeassistant",
				},
			],
		});
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Name"), "x");
		expect(screen.queryByText(/already published/)).toBeNull();
		expect(saveButton()).toBeEnabled();
	});

	test("a udp holder of the same number does not block a tcp port", async () => {
		await renderSettings(tcpView(), {
			ports: [
				{
					protocol: "udp",
					port: 25570,
					serviceId: "svc-other",
					serviceName: "voice",
				},
			],
		});
		const user = userEvent.setup();
		const port = screen.getByLabelText("Port on the server");
		await user.clear(port);
		await user.type(port, "25570");
		expect(screen.queryByText(/already published/)).toBeNull();
		expect(saveButton()).toBeEnabled();
	});

	test("adds an allowed network and sends the new allow list", async () => {
		const { patches } = await renderSettings(tcpView());
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(
			screen.getByLabelText("Allowed network 2"),
			"192.168.0.0/16",
		);
		await user.click(saveButton());
		await screen.findByText("Route saved");
		expect(patches[0]?.body).toEqual({
			allowCidrs: ["10.0.0.0/8", "192.168.0.0/16"],
		});
	});

	test("an invalid network is flagged and blocks the save", async () => {
		await renderSettings(tcpView());
		const user = userEvent.setup();
		const first = screen.getByLabelText("Allowed network 1");
		await user.clear(first);
		await user.type(first, "not-a-network");
		expect(first).toBeInvalid();
		expect(saveButton()).toBeDisabled();
	});

	test("opening the port to the internet needs an empty allow list and is saved as such", async () => {
		const { patches } = await renderSettings(
			tcpView({ allowCidrs: [], openToInternet: false }),
		);
		// Nothing allowed and not open: the form says what to do, but nothing is dirty yet.
		const open = screen.getByRole("checkbox", { name: /Open to the internet/ });
		expect(open).not.toBeChecked();
		const user = userEvent.setup();
		await user.click(open);
		expect(open).toBeChecked();
		await user.click(saveButton());
		await screen.findByText("Route saved");
		expect(patches[0]?.body).toEqual({ openToInternet: true });
	});

	test("the internet checkbox is locked while a network is allowed", async () => {
		await renderSettings(tcpView());
		expect(
			screen.getByRole("checkbox", { name: /Open to the internet/ }),
		).toHaveAttribute("aria-disabled", "true");
	});

	test("allowing a network closes an open port", async () => {
		const { patches } = await renderSettings(
			tcpView({ allowCidrs: [], openToInternet: true }),
		);
		const user = userEvent.setup();
		const open = screen.getByRole("checkbox", { name: /Open to the internet/ });
		expect(open).toBeChecked();
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(screen.getByLabelText("Allowed network 1"), "10.0.0.0/8");
		expect(
			screen.getByRole("checkbox", { name: /Open to the internet/ }),
		).not.toBeChecked();
		await user.click(saveButton());
		await screen.findByText("Route saved");
		expect(patches[0]?.body).toEqual({
			allowCidrs: ["10.0.0.0/8"],
			openToInternet: false,
		});
	});

	test("a denied network is saved as the deny list", async () => {
		const { patches } = await renderSettings(tcpView());
		const user = userEvent.setup();
		await user.click(
			screen.getByRole("button", { name: "Add denied network" }),
		);
		await user.type(screen.getByLabelText("Denied network 1"), "10.1.0.0/16");
		await user.click(saveButton());
		await screen.findByText("Route saved");
		expect(patches[0]?.body).toEqual({ denyCidrs: ["10.1.0.0/16"] });
	});
});

describe("RouteSettingsTab: udp", () => {
	test("states that UDP cannot be filtered and offers no network or PROXY protocol controls", async () => {
		await renderSettings(
			routeView(
				{},
				{
					protocol: "udp",
					listenPort: 30000,
					upstreams: [{ address: "10.0.0.5:19132" }],
				},
			),
		);
		expect(screen.getByText(UDP_WARNING)).toBeInTheDocument();
		expect(screen.queryByLabelText("Allowed network 1")).toBeNull();
		expect(
			screen.queryByRole("checkbox", { name: /Open to the internet/ }),
		).toBeNull();
		expect(screen.queryByRole("button", { name: "v2" })).toBeNull();
		expect(screen.getByLabelText("Port on the server")).toHaveValue("30000");
	});

	test("a udp port clashes only with another udp route", async () => {
		await renderSettings(
			routeView(
				{},
				{
					protocol: "udp",
					listenPort: 30000,
					upstreams: [{ address: "10.0.0.5:19132" }],
				},
			),
			{
				ports: [
					{
						protocol: "udp",
						port: 30001,
						serviceId: "svc-o",
						serviceName: "voice",
					},
				],
			},
		);
		const user = userEvent.setup();
		const port = screen.getByLabelText("Port on the server");
		await user.clear(port);
		await user.type(port, "30001");
		expect(
			screen.getByText("UDP 30001 is already published by voice."),
		).toBeInTheDocument();
	});
});

describe("RouteSettingsTab: navigation", () => {
	test("offers a way to the first category with a problem when it is not the one shown", async () => {
		const { sections } = await renderSettings(routeView(), {
			section: "general",
		});
		const user = userEvent.setup();
		const address = screen.getByLabelText("Upstream address 1");
		await user.clear(address);
		const jump = screen.getByRole("button", { name: "Show in Networking" });
		await user.click(jump);
		expect(sections).toEqual(["networking"]);
	});

	test("offers no jump when the problem is on the category in view", async () => {
		await renderSettings(routeView(), { section: "general" });
		const user = userEvent.setup();
		await user.clear(screen.getByLabelText("Name"));
		expect(screen.queryByRole("button", { name: /Show in/ })).toBeNull();
	});

	test("an unknown section falls back to the first category", async () => {
		await renderSettings(routeView(), { section: "nonsense" });
		expect(
			screen.getByRole("heading", { name: "General" }),
		).toBeInTheDocument();
	});

	test("the Danger category renders the danger zone", async () => {
		await renderSettings(routeView(), { section: "danger" });
		expect(
			screen.getByText(
				/Deleting homeassistant removes its route from the proxy/,
			),
		).toBeInTheDocument();
	});

	test("fetches staged changes only when the service has staged settings", async () => {
		const { api } = await renderSettings(
			routeView({
				pendingChangeCounts: { settings: 1, variables: 0 },
				hasStagedChanges: true,
			}),
		);
		expect(
			api.calls.some((call) => call.path.endsWith("/pending-changes")),
		).toBe(true);
	});

	test("does not fetch ports for an http route", async () => {
		const { api } = await renderSettings(routeView());
		expect(api.calls.some((call) => call.path.endsWith("/ports"))).toBe(false);
	});

	test("fetches the server's ports for a tcp route", async () => {
		const { api } = await renderSettings(tcpView());
		expect(api.calls.some((call) => call.path === "/servers/srv-1/ports")).toBe(
			true,
		);
	});
});
