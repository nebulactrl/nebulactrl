import { afterEach, describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ProxyTailnet } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { routeCreate, useRouteCreateRequest } from "../create-store";
import { ExternalForm } from "./external-form";
import { DialogFrame, server, serviceView, staging } from "./test-fixtures";

afterEach(() => {
	routeCreate.close();
});

const edge = server({ id: "s-edge", name: "edge" });
const created = serviceView({
	id: "svc-ha",
	name: "ha",
	kind: "route",
	serverId: "s-edge",
});

function stubs(
	extra: Parameters<typeof stubApi>[0] = {},
	tailnet: Overrides<ProxyTailnet> = {
		integrationConfigured: false,
		mode: "off",
	},
) {
	return stubApi({
		"GET /servers/:id/proxy": { body: make("ServerProxy", { tailnet }) },
		"GET /environments/:id/services": { body: { items: [] } },
		"POST /environments/:id/routes": { status: 201, body: created },
		...extra,
	});
}

function posts(api: ReturnType<typeof stubApi>) {
	return api.calls.filter((c) => c.method === "POST");
}

function Open() {
	return useRouteCreateRequest() ? <p>dialog-open</p> : <p>dialog-closed</p>;
}

async function renderForm(servers = [edge]) {
	return renderWithProviders(
		<>
			<DialogFrame>
				<ExternalForm
					environment={staging}
					servers={servers}
					position={undefined}
				/>
			</DialogFrame>
			<Open />
		</>,
	);
}

describe("ExternalForm", () => {
	test("creates an http route from a name and an address, with nothing optional sent", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		await user.type(screen.getByLabelText("Name"), "  ha ");
		await user.type(
			screen.getByLabelText("Where it listens"),
			" 192.168.1.20:8123 ",
		);
		await user.click(screen.getByRole("button", { name: "Create route" }));

		expect(await screen.findByText("Created ha")).toBeInTheDocument();
		expect(posts(api)).toHaveLength(1);
		expect(posts(api)[0]).toMatchObject({
			path: "/environments/env-stg/routes",
			body: {
				name: "ha",
				serverId: "s-edge",
				protocol: "http",
				scheme: "http",
				upstreams: [{ address: "192.168.1.20:8123" }],
			},
		});
		expect(Object.keys(posts(api)[0]?.body as object).sort()).toEqual([
			"name",
			"protocol",
			"scheme",
			"serverId",
			"upstreams",
		]);
	});

	test("sends https options, a normalised hostname, a health path and every upstream", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		await user.type(screen.getByLabelText("Name"), "nas");
		await user.type(screen.getByLabelText("Where it listens"), "nas.lan:5001");
		await user.click(
			screen.getByRole("button", { name: "Add another address" }),
		);
		const second = document.getElementById(
			"route-upstream-1",
		) as HTMLInputElement;
		await user.type(second, "nas2.lan:5001");
		await user.click(screen.getByRole("button", { name: "HTTPS" }));
		await user.click(
			screen.getByRole("checkbox", { name: "Skip TLS verification" }),
		);
		expect(
			screen.getByText(
				/The proxy will accept any certificate from this service/,
			),
		).toBeInTheDocument();
		await user.type(
			screen.getByLabelText("Hostname (optional)"),
			"NAS.Example.com.",
		);
		await user.type(screen.getByLabelText("Health path (optional)"), "/health");
		await user.click(screen.getByRole("button", { name: "Create route" }));

		await screen.findByText("Created ha");
		expect(posts(api)[0]?.body).toEqual({
			name: "nas",
			serverId: "s-edge",
			protocol: "http",
			scheme: "https",
			upstreams: [{ address: "nas.lan:5001" }, { address: "nas2.lan:5001" }],
			insecureSkipVerify: true,
			domains: [{ hostname: "nas.example.com" }],
			healthPath: "/health",
		});
	});

	test("switching back to http drops the skip-verify option", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		await user.type(screen.getByLabelText("Name"), "x");
		await user.type(screen.getByLabelText("Where it listens"), "10.0.0.1:80");
		await user.click(screen.getByRole("button", { name: "HTTPS" }));
		await user.click(
			screen.getByRole("checkbox", { name: "Skip TLS verification" }),
		);
		await user.click(screen.getByRole("button", { name: "HTTP" }));
		expect(
			screen.queryByRole("checkbox", { name: "Skip TLS verification" }),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create route" }));
		await screen.findByText("Created ha");
		expect(posts(api)[0]?.body).not.toHaveProperty("insecureSkipVerify");
		expect(posts(api)[0]?.body).toMatchObject({ scheme: "http" });
	});

	test("submitting an empty form flags what is missing and sends nothing", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		expect(screen.queryByText("Enter host:port.")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create route" }));
		expect(await screen.findByText("Enter host:port.")).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(posts(api)).toEqual([]);
	});

	test("a malformed address is explained as it is typed and blocks the submit", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		await user.type(screen.getByLabelText("Name"), "ha");
		await user.type(screen.getByLabelText("Where it listens"), "192.168.1.20");
		expect(
			await screen.findByText("Use host:port, e.g. 192.168.1.20:8080."),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create route" }));
		expect(posts(api)).toEqual([]);
		expect(screen.getByText("dialog-closed")).toBeInTheDocument();
	});

	test("an invalid hostname and a health path without a slash are refused", async () => {
		const api = stubs();
		const user = userEvent.setup();
		await renderForm();
		await user.type(screen.getByLabelText("Name"), "ha");
		await user.type(screen.getByLabelText("Where it listens"), "10.0.0.1:80");
		await user.type(
			screen.getByLabelText("Hostname (optional)"),
			"https://bad host",
		);
		await user.type(screen.getByLabelText("Health path (optional)"), "health");
		expect(
			await screen.findByText(
				"Use a hostname such as app.example.com (no scheme, no path).",
			),
		).toBeInTheDocument();
		expect(screen.getByText("Start with /, e.g. /health.")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create route" }));
		expect(posts(api)).toEqual([]);
	});

	test("addresses can be added and removed; the last one has no remove key", async () => {
		stubs();
		const user = userEvent.setup();
		await renderForm();
		expect(
			screen.queryByRole("button", { name: /Remove address/ }),
		).not.toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: "Add another address" }),
		);
		await user.type(
			document.getElementById("route-upstream-0") as HTMLInputElement,
			"a.lan:1",
		);
		await user.type(
			document.getElementById("route-upstream-1") as HTMLInputElement,
			"b.lan:2",
		);
		await user.click(screen.getByRole("button", { name: "Remove address 1" }));
		expect(document.getElementById("route-upstream-0")).toHaveValue("b.lan:2");
		expect(document.getElementById("route-upstream-1")).toBeNull();
		expect(
			screen.queryByRole("button", { name: /Remove address/ }),
		).not.toBeInTheDocument();
	});

	test("the add-address key disappears at 20 addresses", async () => {
		stubs();
		const user = userEvent.setup();
		await renderForm();
		for (let i = 1; i < 20; i += 1) {
			await user.click(
				screen.getByRole("button", { name: "Add another address" }),
			);
		}
		expect(
			screen.getAllByRole("button", { name: /Remove address/ }),
		).toHaveLength(20);
		expect(
			screen.queryByRole("button", { name: "Add another address" }),
		).not.toBeInTheDocument();
	});

	test("picks the first online server whose agent speaks proxy v2", async () => {
		const api = stubs();
		const user = userEvent.setup();
		const old = server({
			id: "s-old",
			name: "old",
			proxy: { v2: false },
		});
		const down = server({ id: "s-down", name: "down", status: "offline" });
		await renderForm([old, down, edge]);
		expect(
			screen.getByRole("combobox", { name: "Published by" }),
		).toHaveTextContent("edge");
		await user.type(screen.getByLabelText("Name"), "ha");
		await user.type(screen.getByLabelText("Where it listens"), "10.0.0.1:80");
		await user.click(screen.getByRole("button", { name: "Create route" }));
		await screen.findByText("Created ha");
		expect(posts(api)[0]?.body).toMatchObject({ serverId: "s-edge" });
	});

	test("falls back to an offline v2 server when none is online", async () => {
		stubs();
		const old = server({
			id: "s-old",
			name: "old",
			proxy: { v2: false },
		});
		const down = server({ id: "s-down", name: "down", status: "offline" });
		await renderForm([old, down]);
		expect(
			screen.getByRole("combobox", { name: "Published by" }),
		).toHaveTextContent("down");
	});

	test("with only an old agent nothing is preselected and the server is required", async () => {
		const api = stubs();
		const user = userEvent.setup();
		const old = server({
			id: "s-old",
			name: "old",
			proxy: { v2: false },
		});
		await renderForm([old]);
		expect(
			screen.getByRole("combobox", { name: "Published by" }),
		).toHaveTextContent("Pick a server");
		await user.type(screen.getByLabelText("Name"), "ha");
		await user.type(screen.getByLabelText("Where it listens"), "10.0.0.1:80");
		await user.click(screen.getByRole("button", { name: "Create route" }));
		expect(
			screen.getByRole("combobox", { name: "Published by" }),
		).toHaveAttribute("aria-invalid", "true");
		expect(posts(api)).toEqual([]);
	});

	test("revoked and pending servers are not offered", async () => {
		stubs();
		const user = userEvent.setup();
		await renderForm([
			edge,
			server({ id: "s-rev", name: "revoked-box", status: "revoked" }),
			server({ id: "s-pen", name: "pending-box", status: "pending" }),
		]);
		await user.click(screen.getByRole("combobox", { name: "Published by" }));
		expect(
			await screen.findByRole("option", { name: /edge/ }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("option", { name: /revoked-box/ }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("option", { name: /pending-box/ }),
		).not.toBeInTheDocument();
	});

	test("a denied upstream is shown under the address, not as a general error", async () => {
		stubs({
			"POST /environments/:id/routes": problem(422, {
				code: "upstream_denied",
				detail: "Loopback addresses are refused for this server.",
			}),
		});
		const user = userEvent.setup();
		await renderForm();
		await user.type(screen.getByLabelText("Name"), "ha");
		await user.type(screen.getByLabelText("Where it listens"), "127.0.0.1:80");
		await user.click(screen.getByRole("button", { name: "Create route" }));
		const message = await screen.findByText(
			"Loopback addresses are refused for this server.",
		);
		expect(
			screen.getAllByText("Loopback addresses are refused for this server."),
		).toHaveLength(1);
		expect(message.closest("li")).toBeNull();
		expect(screen.getByText("dialog-closed")).toBeInTheDocument();
	});

	test("an unmapped failure shows its message once as the general error", async () => {
		stubs({
			"POST /environments/:id/routes": problem(500, {
				code: "internal",
				detail: "The database is unavailable.",
			}),
		});
		const user = userEvent.setup();
		await renderForm();
		await user.type(screen.getByLabelText("Name"), "ha");
		await user.type(screen.getByLabelText("Where it listens"), "10.0.0.1:80");
		await user.click(screen.getByRole("button", { name: "Create route" }));
		expect(
			await screen.findByText("The database is unavailable."),
		).toBeInTheDocument();
		// The button is usable again for a retry.
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Create route" }),
			).toBeEnabled(),
		);
	});

	test("an agent that is too old gets the upgrade banner, not an error line", async () => {
		stubs({
			"POST /environments/:id/routes": problem(422, {
				code: "agent_too_old",
				detail: "agent too old",
			}),
		});
		const user = userEvent.setup();
		await renderForm();
		await user.type(screen.getByLabelText("Name"), "ha");
		await user.type(screen.getByLabelText("Where it listens"), "10.0.0.1:80");
		await user.click(screen.getByRole("button", { name: "Create route" }));
		expect(await screen.findByTestId("agent-too-old")).toBeInTheDocument();
		expect(screen.queryByText("agent too old")).not.toBeInTheDocument();
	});

	test("Cancel closes the create dialog without a request", async () => {
		const api = stubs();
		routeCreate.open({ mode: "external" });
		const user = userEvent.setup();
		await renderForm();
		expect(screen.getByText("dialog-open")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(screen.getByText("dialog-closed")).toBeInTheDocument();
		expect(posts(api)).toEqual([]);
	});

	test("a picked tailnet device fills the host and keeps the typed port", async () => {
		stubs(
			{
				"GET /integrations/tailscale/devices": {
					body: {
						items: [
							{
								id: "d1",
								hostname: "nas",
								name: "nas.ts.net",
								addresses: ["100.64.0.2"],
								online: true,
								os: "linux",
								lastSeen: null,
								tags: null,
							},
						],
					},
				},
			},
			{ integrationConfigured: true, mode: "managed" },
		);
		const user = userEvent.setup();
		await renderForm();
		await user.type(screen.getByLabelText("Where it listens"), "old.lan:5000");
		await user.click(
			await screen.findByRole("combobox", { name: "Pick a tailnet device" }),
		);
		await user.click(await screen.findByRole("option", { name: /nas/ }));
		expect(screen.getByLabelText("Where it listens")).toHaveValue(
			"100.64.0.2:5000",
		);
	});

	test("the tailnet picker is not offered once there are several addresses", async () => {
		stubs(
			{
				"GET /integrations/tailscale/devices": {
					body: {
						items: [
							{
								id: "d1",
								hostname: "nas",
								name: "nas.ts.net",
								addresses: ["100.64.0.2"],
								online: true,
								os: "linux",
								lastSeen: null,
								tags: null,
							},
						],
					},
				},
			},
			{ integrationConfigured: true, mode: "managed" },
		);
		const user = userEvent.setup();
		await renderForm();
		await screen.findByRole("combobox", { name: "Pick a tailnet device" });
		await user.click(
			screen.getByRole("button", { name: "Add another address" }),
		);
		expect(
			screen.queryByRole("combobox", { name: "Pick a tailnet device" }),
		).not.toBeInTheDocument();
	});

	test("while the create is in flight the button is disabled and a second submit is ignored", async () => {
		let release: () => void = () => {};
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		const api = stubs({
			"POST /environments/:id/routes": async () => {
				await gate;
				return Response.json(created, { status: 201 });
			},
		});
		const user = userEvent.setup();
		await renderForm();
		await user.type(screen.getByLabelText("Name"), "ha");
		await user.type(screen.getByLabelText("Where it listens"), "10.0.0.1:80");
		await user.click(screen.getByRole("button", { name: "Create route" }));
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: /Create route/ }),
			).toBeDisabled(),
		);
		expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
		await user.type(screen.getByLabelText("Name"), "{Enter}");
		release();
		await screen.findByText("Created ha");
		expect(posts(api)).toHaveLength(1);
	});
});
