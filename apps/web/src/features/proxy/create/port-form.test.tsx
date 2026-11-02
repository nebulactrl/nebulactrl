import { afterEach, describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PortView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	type RouteCreateRequest,
	routeCreate,
	useRouteCreateRequest,
} from "../create-store";
import { FIREWALL_NOTE, RESTART_WARNING, UDP_WARNING } from "../lib/l4";
import { PortForm } from "./port-form";
import { DialogFrame, server, serviceView, staging } from "./test-fixtures";

afterEach(() => routeCreate.close());

const edge = server({ id: "s-edge", name: "edge" });
const other = server({ id: "s-other", name: "other" });

const minecraft = serviceView({
	id: "svc-mc",
	name: "minecraft",
	serverId: "s-edge",
	serverName: "edge",
	port: 25565,
});
const db = serviceView({
	id: "svc-db",
	name: "postgres",
	serverId: "s-other",
	serverName: "other",
	port: 5432,
});
const noPortApp = serviceView({
	id: "svc-np",
	name: "worker",
	serverId: "s-edge",
	serverName: "edge",
});
const existingRoute = serviceView({
	id: "svc-route",
	name: "a-route",
	kind: "route",
	serverId: "s-edge",
});
const unplaced = serviceView({ id: "svc-unplaced", name: "unplaced" });
const created = serviceView({
	id: "svc-new",
	name: "new-route",
	kind: "route",
	serverId: "s-edge",
});

type Routes = Parameters<typeof stubApi>[0];

function stubs(extra: Routes = {}, ports: PortView[] = []) {
	return stubApi({
		"GET /environments/:id/services": {
			body: make("PageServiceView", {
				items: [minecraft, db, noPortApp, existingRoute, unplaced],
			}),
		},
		"GET /servers/:id/ports": {
			body: make("PortsOutputBody", { items: ports }),
		},
		"GET /servers/:id/proxy": {
			body: make("ServerProxy", {
				tailnet: { integrationConfigured: false, mode: "off" },
			}),
		},
		"POST /environments/:id/routes": { status: 201, body: created },
		...extra,
	});
}

const posts = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((c) => c.method === "POST");

function Open() {
	return useRouteCreateRequest() ? <p>dialog-open</p> : <p>dialog-closed</p>;
}

async function renderForm(
	request: RouteCreateRequest = {
		mode: "port",
		target: { serviceId: "svc-mc", port: 25565 },
	},
	servers = [edge, other],
) {
	const view = await renderWithProviders(
		<>
			<DialogFrame>
				<PortForm environment={staging} servers={servers} request={request} />
			</DialogFrame>
			<Open />
		</>,
	);
	// The services list decides the preselected target.
	await waitFor(() =>
		expect(
			screen.getByRole("combobox", { name: "Service" }),
		).toBeInTheDocument(),
	);
	return view;
}

type User = ReturnType<typeof userEvent.setup>;

async function addAllowed(user: User, ...cidrs: string[]) {
	for (const [i, cidr] of cidrs.entries()) {
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(screen.getByLabelText(`Allowed network ${i + 1}`), cidr);
	}
}

const listen = () => screen.getByLabelText("Port on the server");
const submit = () => screen.getByRole("button", { name: /Create port/ });

describe("PortForm: creating a TCP port for a managed service", () => {
	test("preselects the target service and port, then posts the exposure", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Service" }),
			).toHaveTextContent("minecraft"),
		);
		expect(screen.getByLabelText("Its port")).toHaveValue("25565");
		await user.type(listen(), "25565");
		await addAllowed(user, "10.0.0.0/8");
		await user.click(submit());

		expect(await screen.findByText("Created new-route")).toBeInTheDocument();
		expect(posts(api)).toHaveLength(1);
		expect(posts(api)[0]).toMatchObject({
			path: "/environments/env-stg/routes",
			body: {
				name: "minecraft-tcp-25565",
				serverId: "s-edge",
				protocol: "tcp",
				listenPort: 25565,
				upstreams: [{ serviceId: "svc-mc", port: 25565 }],
				allowCidrs: ["10.0.0.0/8"],
			},
		});
		expect(posts(api)[0]?.body).not.toHaveProperty("openToInternet");
		expect(posts(api)[0]?.body).not.toHaveProperty("denyCidrs");
		expect(screen.getByText("dialog-closed")).toBeInTheDocument();
	});

	test("checks the port against the chosen server's ports and says it is free", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		await user.type(listen(), "25565");
		expect(
			await screen.findByText("TCP 25565 is free on edge."),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/servers/s-edge/ports")).toBe(
			true,
		);
		expect(
			screen.getByText(/New TCP 25565: the proxy on edge restarts once/),
		).toBeInTheDocument();
	});

	test("a TCP port another route already holds is refused with the holder's name", async () => {
		const api = stubs({}, [
			{
				deployed: true,
				port: 25565,
				projectId: "p",
				protocol: "tcp",
				serviceId: "svc-other",
				serviceName: "survival",
			},
		]);
		const user = userEvent.setup();
		await renderForm();
		await user.type(listen(), "25565");
		await addAllowed(user, "10.0.0.0/8");
		expect(
			await screen.findByText("TCP 25565 is already published by survival."),
		).toBeInTheDocument();
		expect(screen.queryByText(/is free on/)).not.toBeInTheDocument();
		await user.click(submit());
		expect(posts(api)).toEqual([]);
		expect(listen()).toHaveAttribute("aria-invalid", "true");
	});

	test("a holder the caller cannot see is reported without a name", async () => {
		stubs({}, [
			{
				deployed: true,
				port: 25565,
				projectId: "p",
				protocol: "tcp",
				serviceId: "x",
				serviceName: "",
			},
		]);
		const user = userEvent.setup();
		await renderForm();
		await user.type(listen(), "25565");
		expect(
			await screen.findByText("TCP 25565 is already in use on this server."),
		).toBeInTheDocument();
	});

	test("a port outside 1024 to 65535 and a reserved port are explained", async () => {
		stubs();
		const user = userEvent.setup();
		await renderForm();
		await user.type(listen(), "80");
		expect(
			await screen.findByText("Choose a port between 1024 and 65535."),
		).toBeInTheDocument();
		await user.clear(listen());
		await user.type(listen(), "8099");
		expect(
			await screen.findByText("Port 8099 is reserved for the proxy."),
		).toBeInTheDocument();
		await user.clear(listen());
		await user.type(listen(), "abc");
		expect(
			await screen.findByText("Choose a port between 1024 and 65535."),
		).toBeInTheDocument();
	});

	test("no listen error is shown before anything is typed", async () => {
		stubs();
		await renderForm();
		expect(
			screen.queryByText("Choose a port between 1024 and 65535."),
		).not.toBeInTheDocument();
		expect(listen()).toHaveAttribute("aria-invalid", "false");
	});

	test("without any allowed network or the internet option the TCP port is refused", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		await user.type(listen(), "25565");
		await screen.findByText("TCP 25565 is free on edge.");
		expect(
			screen.queryByText(
				"Add an allowed network, or open the port to the internet on purpose.",
			),
		).not.toBeInTheDocument();
		await user.click(submit());
		expect(
			await screen.findByText(
				"Add an allowed network, or open the port to the internet on purpose.",
			),
		).toBeInTheDocument();
		expect(posts(api)).toEqual([]);
	});

	test("opening the port to the internet posts openToInternet and no allow list", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		await user.type(listen(), "25565");
		await user.click(
			screen.getByRole("checkbox", { name: /Open to the internet/ }),
		);
		expect(
			await screen.findByText(
				"Open to the internet: anyone who can reach this server can connect to this port.",
			),
		).toBeInTheDocument();
		await user.click(submit());
		await screen.findByText("Created new-route");
		const body = posts(api)[0]?.body as Record<string, unknown>;
		expect(body.openToInternet).toBe(true);
		expect(body).not.toHaveProperty("allowCidrs");
	});

	test("an allowed network unticks and locks 'open to the internet'", async () => {
		stubs();
		const user = userEvent.setup();
		await renderForm();
		const open = () =>
			screen.getByRole("checkbox", { name: /Open to the internet/ });
		await user.click(open());
		expect(open()).toBeChecked();
		await addAllowed(user, "10.0.0.0/8");
		expect(open()).not.toBeChecked();
		expect(open()).toHaveAttribute("aria-disabled", "true");
	});

	test("denied networks are sent along with the allowed ones", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		await user.type(listen(), "25565");
		await addAllowed(user, "10.0.0.0/8");
		await user.click(
			screen.getByRole("button", { name: "Add denied network" }),
		);
		await user.type(screen.getByLabelText("Denied network 1"), "10.1.0.0/16");
		expect(
			await screen.findByText("1 denied network is refused first."),
		).toBeInTheDocument();
		await user.click(submit());
		await screen.findByText("Created new-route");
		expect(posts(api)[0]?.body).toMatchObject({
			allowCidrs: ["10.0.0.0/8"],
			denyCidrs: ["10.1.0.0/16"],
		});
	});

	test("an invalid network blocks the submit and is flagged on its row", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		await user.type(listen(), "25565");
		await addAllowed(user, "not-a-network");
		await screen.findByText("TCP 25565 is free on edge.");
		expect(screen.getByLabelText("Allowed network 1")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		await user.click(submit());
		expect(posts(api)).toEqual([]);
	});

	test("a custom name overrides the suggested one", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		expect(screen.getByLabelText("Name")).toHaveAttribute(
			"placeholder",
			"minecraft-tcp-port",
		);
		await user.type(listen(), "25565");
		expect(screen.getByLabelText("Name")).toHaveAttribute(
			"placeholder",
			"minecraft-tcp-25565",
		);
		await user.type(screen.getByLabelText("Name"), "  game  ");
		await addAllowed(user, "10.0.0.0/8");
		await user.click(submit());
		await screen.findByText("Created new-route");
		expect(posts(api)[0]?.body).toMatchObject({ name: "game" });
	});

	test("always warns that a new port restarts the proxy and Docker bypasses the host firewall", async () => {
		stubs();
		await renderForm();
		expect(screen.getByText(RESTART_WARNING)).toBeInTheDocument();
		expect(screen.getByText(FIREWALL_NOTE)).toBeInTheDocument();
	});
});

describe("PortForm: choosing the service", () => {
	test("lists only managed services that run on a server, not routes", async () => {
		stubs();
		const user = userEvent.setup();
		await renderForm({ mode: "port" });
		await user.click(screen.getByRole("combobox", { name: "Service" }));
		expect(
			await screen.findByRole("option", { name: /minecraft/ }),
		).toHaveTextContent("on edge");
		expect(screen.getByRole("option", { name: /postgres/ })).toHaveTextContent(
			"on other",
		);
		expect(screen.getByRole("option", { name: /worker/ })).toBeInTheDocument();
		expect(
			screen.queryByRole("option", { name: /a-route/ }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("option", { name: /unplaced/ }),
		).not.toBeInTheDocument();
	});

	test("picking a service fills its port and publishes on that service's server", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm({ mode: "port" });
		await user.click(screen.getByRole("combobox", { name: "Service" }));
		await user.click(await screen.findByRole("option", { name: /postgres/ }));
		expect(screen.getByLabelText("Its port")).toHaveValue("5432");
		await user.type(listen(), "15432");
		expect(
			await screen.findByText("TCP 15432 is free on other."),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/servers/s-other/ports")).toBe(
			true,
		);
		await addAllowed(user, "192.168.0.0/16");
		await user.click(submit());
		await screen.findByText("Created new-route");
		expect(posts(api)[0]?.body).toMatchObject({
			serverId: "s-other",
			upstreams: [{ serviceId: "svc-db", port: 5432 }],
		});
	});

	test("a service that does not say its port leaves the field for the user", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm({ mode: "port" });
		await user.click(screen.getByRole("combobox", { name: "Service" }));
		await user.click(await screen.findByRole("option", { name: /worker/ }));
		expect(screen.getByLabelText("Its port")).toHaveValue("");
		await user.type(listen(), "25565");
		await addAllowed(user, "10.0.0.0/8");
		await user.click(submit());
		expect(
			await screen.findByText(
				"Enter the port the service listens on (1 to 65535).",
			),
		).toBeInTheDocument();
		expect(posts(api)).toEqual([]);
		await user.type(screen.getByLabelText("Its port"), "9000");
		await user.click(submit());
		await screen.findByText("Created new-route");
		expect(posts(api)[0]?.body).toMatchObject({
			upstreams: [{ serviceId: "svc-np", port: 9000 }],
		});
	});

	test("submitting without a service asks for one", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm({ mode: "port" });
		await user.type(listen(), "25565");
		await user.click(submit());
		expect(
			await screen.findByText("Pick the service to expose."),
		).toBeInTheDocument();
		expect(screen.getByRole("combobox", { name: "Service" })).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(posts(api)).toEqual([]);
		// No server is known yet, so no port lookup was made.
		expect(api.calls.some((c) => c.path.endsWith("/ports"))).toBe(false);
	});

	test("a target service that is not in this environment is called out", async () => {
		stubs();
		await renderForm({
			mode: "port",
			target: { serviceId: "svc-elsewhere", port: 80 },
		});
		expect(
			await screen.findByText("That service is not in this environment."),
		).toBeInTheDocument();
	});

	test("the out-of-environment note does not flash while services are loading", async () => {
		stubs({
			"GET /environments/:id/services": async () => {
				await new Promise((resolve) => setTimeout(resolve, 30));
				return Response.json({ items: [minecraft] });
			},
		});
		await renderWithProviders(
			<DialogFrame>
				<PortForm
					environment={staging}
					servers={[edge]}
					request={{
						mode: "port",
						target: { serviceId: "svc-mc", port: 25565 },
					}}
				/>
			</DialogFrame>,
		);
		expect(
			screen.queryByText("That service is not in this environment."),
		).not.toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Service" }),
			).toHaveTextContent("minecraft"),
		);
		expect(
			screen.queryByText("That service is not in this environment."),
		).not.toBeInTheDocument();
	});
});

describe("PortForm: forwarding to an address", () => {
	async function toAddress(user: User, request?: RouteCreateRequest) {
		await renderForm(request ?? { mode: "port" });
		await user.click(screen.getByRole("button", { name: "An address" }));
	}

	test("publishes on the first online v2 server and posts the address", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await toAddress(user);
		expect(
			screen.getByRole("combobox", { name: "Published by" }),
		).toHaveTextContent("edge");
		await user.type(screen.getByLabelText("Address"), " 192.168.1.20:25565 ");
		await user.type(listen(), "25565");
		await addAllowed(user, "10.0.0.0/8");
		await user.click(submit());
		await screen.findByText("Created new-route");
		expect(posts(api)[0]?.body).toEqual({
			name: "port-tcp-25565",
			serverId: "s-edge",
			protocol: "tcp",
			listenPort: 25565,
			upstreams: [{ address: "192.168.1.20:25565" }],
			allowCidrs: ["10.0.0.0/8"],
		});
	});

	test("an empty or malformed address blocks the submit with its reason", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await toAddress(user);
		await user.type(listen(), "25565");
		await addAllowed(user, "10.0.0.0/8");
		await user.click(submit());
		expect(await screen.findByText("Enter host:port.")).toBeInTheDocument();
		await user.type(screen.getByLabelText("Address"), "nas");
		expect(
			await screen.findByText("Use host:port, e.g. 192.168.1.20:8080."),
		).toBeInTheDocument();
		await user.click(submit());
		expect(posts(api)).toEqual([]);
	});

	test("the server can be changed and its ports are checked instead", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await toAddress(user);
		await user.click(screen.getByRole("combobox", { name: "Published by" }));
		await user.click(await screen.findByRole("option", { name: /other/ }));
		await user.type(listen(), "25565");
		expect(
			await screen.findByText("TCP 25565 is free on other."),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/servers/s-other/ports")).toBe(
			true,
		);
	});

	test("hides the service picker and shows the address field", async () => {
		stubs();
		const user = userEvent.setup();
		await toAddress(user);
		expect(
			screen.queryByRole("combobox", { name: "Service" }),
		).not.toBeInTheDocument();
		expect(screen.getByLabelText("Address")).toBeInTheDocument();
	});

	test("with no usable server nothing is preselected and the port cannot be checked", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm({ mode: "port" }, [
			server({ id: "s-old", name: "old", proxy: { v2: false } }),
		]);
		await user.click(screen.getByRole("button", { name: "An address" }));
		expect(
			screen.getByRole("combobox", { name: "Published by" }),
		).toHaveTextContent("Pick a server");
		await user.type(screen.getByLabelText("Address"), "10.0.0.1:80");
		await user.type(listen(), "25565");
		await addAllowed(user, "10.0.0.0/8");
		await user.click(submit());
		expect(posts(api)).toEqual([]);
		expect(
			screen.getByRole("combobox", { name: "Published by" }),
		).toHaveAttribute("aria-invalid", "true");
	});
});

describe("PortForm: UDP", () => {
	test("has no access rules, warns it cannot be restricted, and posts no CIDRs", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		expect(screen.getByText("Allowed networks")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "UDP" }));
		expect(screen.queryByText("Allowed networks")).not.toBeInTheDocument();
		expect(
			screen.queryByRole("checkbox", { name: /Open to the internet/ }),
		).not.toBeInTheDocument();
		expect(screen.getAllByText(UDP_WARNING).length).toBeGreaterThan(0);
		await user.type(listen(), "25565");
		expect(
			await screen.findByText("UDP 25565 is free on edge."),
		).toBeInTheDocument();
		await user.click(submit());
		await screen.findByText("Created new-route");
		expect(posts(api)[0]?.body).toEqual({
			name: "minecraft-udp-25565",
			serverId: "s-edge",
			protocol: "udp",
			listenPort: 25565,
			upstreams: [{ serviceId: "svc-mc", port: 25565 }],
		});
	});

	test("a TCP holder of the port does not block the same UDP port", async () => {
		stubs({}, [
			{
				deployed: true,
				port: 25565,
				projectId: "p",
				protocol: "tcp",
				serviceId: "x",
				serviceName: "survival",
			},
		]);
		const user = userEvent.setup();
		await renderForm();
		await user.click(screen.getByRole("button", { name: "UDP" }));
		await user.type(listen(), "25565");
		expect(
			await screen.findByText("UDP 25565 is free on edge."),
		).toBeInTheDocument();
		expect(screen.queryByText(/already published/)).not.toBeInTheDocument();
	});
});

describe("PortForm: failures and states", () => {
	async function fill(user: User) {
		await user.type(listen(), "25565");
		await addAllowed(user, "10.0.0.0/8");
		await screen.findByText("TCP 25565 is free on edge.");
		await user.click(submit());
	}

	test("the control plane's port_in_use answer lands under the port field", async () => {
		stubs({
			"POST /environments/:id/routes": problem(409, {
				code: "port_in_use",
				detail: "Port 25565 was taken a moment ago.",
			}),
		});
		const user = userEvent.setup();
		await renderForm();
		await fill(user);
		expect(
			await screen.findByText("Port 25565 was taken a moment ago."),
		).toBeInTheDocument();
		expect(
			screen.getAllByText("Port 25565 was taken a moment ago."),
		).toHaveLength(1);
		expect(screen.getByText("dialog-closed")).toBeInTheDocument();
		expect(listen()).toHaveAttribute("aria-invalid", "true");
	});

	test("an undeployed upstream is reported once, near the target", async () => {
		stubs({
			"POST /environments/:id/routes": problem(422, {
				code: "upstream_not_deployed",
				detail: "minecraft has not been deployed yet.",
			}),
		});
		const user = userEvent.setup();
		await renderForm();
		await fill(user);
		expect(
			await screen.findByText("minecraft has not been deployed yet."),
		).toBeInTheDocument();
		expect(
			screen.getAllByText("minecraft has not been deployed yet."),
		).toHaveLength(1);
	});

	test("any other failure is shown as the general error and the form stays usable", async () => {
		stubs({
			"POST /environments/:id/routes": problem(500, {
				code: "internal",
				detail: "The database is unavailable.",
			}),
		});
		const user = userEvent.setup();
		await renderForm();
		await fill(user);
		expect(
			await screen.findByText("The database is unavailable."),
		).toBeInTheDocument();
		await waitFor(() => expect(submit()).toBeEnabled());
	});

	test("an agent that is too old gets the upgrade banner", async () => {
		stubs({
			"POST /environments/:id/routes": problem(422, {
				code: "agent_too_old",
				detail: "agent too old",
			}),
		});
		const user = userEvent.setup();
		await renderForm();
		await fill(user);
		expect(await screen.findByTestId("agent-too-old")).toBeInTheDocument();
		expect(screen.queryByText("agent too old")).not.toBeInTheDocument();
	});

	test("when the server's ports cannot be read the form says so and still allows a submit", async () => {
		const api = stubs({
			"GET /servers/:id/ports": problem(500, { code: "internal" }),
		});
		const user = userEvent.setup();
		await renderForm();
		await user.type(listen(), "25565");
		await addAllowed(user, "10.0.0.0/8");
		expect(
			await screen.findByText(/The server's ports could not be checked/),
		).toBeInTheDocument();
		await user.click(submit());
		await screen.findByText("Created new-route");
		expect(posts(api)).toHaveLength(1);
	});

	test("Cancel closes the dialog without a request", async () => {
		const api = stubs();
		routeCreate.open({ mode: "port" });
		const user = userEvent.setup();
		await renderForm();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(screen.getByText("dialog-closed")).toBeInTheDocument();
		expect(posts(api)).toEqual([]);
	});
});
