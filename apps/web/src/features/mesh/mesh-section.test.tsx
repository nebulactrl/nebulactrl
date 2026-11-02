import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { meQuery } from "#/api/hooks";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { MeshSection } from "./mesh-section";
import type { MeshView } from "./model";
import { link, meBody, meshView, node, rule } from "./test-data";

const ADMIN = { "GET /me": { body: meBody(["admin"]) } };

/** Renders, and waits for the principal: until then every write control is off. */
async function renderSection() {
	const result = await renderWithProviders(<MeshSection />);
	await waitFor(() =>
		expect(result.queryClient.getQueryData(meQuery().queryKey)).toBeDefined(),
	);
	return result;
}

/** Base UI marks a disabled switch with aria-disabled, not the disabled attribute. */
const isOff = (el: HTMLElement) =>
	expect(el).toHaveAttribute("aria-disabled", "true");

const section = (name: string) => screen.getByRole("region", { name });

async function loaded(overrides: Partial<MeshView> = {}, me = ADMIN) {
	const api = stubApi({
		...me,
		"GET /mesh": { body: meshView(overrides) },
	});
	await renderSection();
	await screen.findByRole("region", { name: "Status" });
	return api;
}

describe("MeshSection: before there is a mesh", () => {
	test("shows a skeleton while loading", async () => {
		stubApi({ "GET /mesh": () => new Promise<Response>(() => {}) });
		const { container } = await renderWithProviders(<MeshSection />);
		expect(container.querySelector('[data-slot="skeleton"]')).not.toBeNull();
		expect(
			screen.queryByRole("region", { name: "Status" }),
		).not.toBeInTheDocument();
	});

	test("an unconfigured team is asked to connect a tailnet, with both ways in", async () => {
		stubApi({
			"GET /mesh": problem(409, {
				code: "mesh_not_configured",
				detail: "no tailnet",
			}),
		});
		await renderWithProviders(<MeshSection />);
		expect(
			await screen.findByText("Connect a tailnet first"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Connect Tailscale" }),
		).toHaveAttribute("href", "/settings/integrations");
		expect(
			screen.getByRole("button", { name: "Set up Headscale" }),
		).toHaveAttribute("href", "/settings/headscale");
	});

	test("a mesh whose provider is none is treated as not connected", async () => {
		stubApi({
			"GET /mesh": { body: meshView({ provider: "none" }) },
		});
		await renderWithProviders(<MeshSection />);
		expect(
			await screen.findByText("Connect a tailnet first"),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("region", { name: "Status" }),
		).not.toBeInTheDocument();
	});

	test("any other failure says nothing was changed and Try again recovers", async () => {
		let attempts = 0;
		stubApi({
			...ADMIN,
			"GET /mesh": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json(meshView());
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<MeshSection />);
		expect(
			await screen.findByText("The mesh did not load"),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Connect a tailnet first"),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByRole("region", { name: "Status" }),
		).toBeInTheDocument();
	});
});

describe("MeshSection: a mesh with nothing in it", () => {
	test("each empty block explains what makes it fill up", async () => {
		await loaded();
		expect(
			screen.getByText(
				"No server has joined yet. Enable the mesh from a server's page.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/No server has a node\. Open a server and enable the mesh on it/,
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/No links yet\. One appears when a service reaches another/,
			),
		).toBeInTheDocument();
		expect(
			screen.getByText("No rules: nothing needs one yet."),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
	});

	test("shows the provider, the device budget with the pricing link and the relay disclosure", async () => {
		await loaded({
			devices: { used: 4 },
			loginServer: "https://hs.example.com",
		});
		const status = within(section("Status"));
		expect(status.getByText("Tailscale")).toBeInTheDocument();
		expect(status.getByText("https://hs.example.com")).toBeInTheDocument();
		expect(
			within(section("Devices")).getByText(/This team uses 4 tagged devices/),
		).toBeInTheDocument();
		expect(
			within(section("Devices")).getByRole("link", {
				name: "Tailscale pricing",
			}),
		).toHaveAttribute("href", "https://tailscale.com/pricing");
		expect(
			within(section("Relays")).getByText(
				/Your tailnet falls back to Tailscale's relay servers/,
			),
		).toBeInTheDocument();
	});

	test("Headscale is named and its relay disclosure differs", async () => {
		await loaded({ provider: "headscale" });
		expect(
			within(section("Status")).getByText("Headscale"),
		).toBeInTheDocument();
		expect(
			within(section("Relays")).getByText(
				/Managed Headscale falls back to Tailscale's public relay servers/,
			),
		).toBeInTheDocument();
	});
});

describe("MeshSection: coordinator and banners", () => {
	test("a reachable coordinator reads Reachable and has no banners", async () => {
		await loaded({ coordinator: { ok: true, checkedAt: null } });
		expect(
			within(section("Status")).getByText("Reachable"),
		).toBeInTheDocument();
		expect(screen.queryByText("The mesh is paused")).not.toBeInTheDocument();
		expect(
			screen.queryByText("The coordinator is unreachable"),
		).not.toBeInTheDocument();
	});

	test("an unreachable coordinator is stated in the row and as a banner", async () => {
		await loaded({
			coordinator: { ok: false, checkedAt: new Date().toISOString() },
		});
		expect(
			within(section("Status")).getByText("Unreachable"),
		).toBeInTheDocument();
		expect(
			screen.getByText("The coordinator is unreachable"),
		).toBeInTheDocument();
		expect(within(section("Status")).getByText(/Checked/)).toBeInTheDocument();
	});

	test("a coordinator never checked reads Not checked", async () => {
		await loaded({ coordinator: { ok: null, checkedAt: null } });
		expect(
			within(section("Status")).getByText("Not checked"),
		).toBeInTheDocument();
	});

	test("a paused mesh gives its reason, ending in a full stop", async () => {
		await loaded({ paused: true, pausedReason: "Headscale is being migrated" });
		expect(screen.getByText("The mesh is paused")).toBeInTheDocument();
		expect(
			screen.getByText(
				/Headscale is being migrated\. Connections that are up keep working/,
			),
		).toBeInTheDocument();
	});

	test("a paused mesh with no reason falls back to a generic one", async () => {
		await loaded({ paused: true, pausedReason: "" });
		expect(
			screen.getByText(
				/A change to the coordinator is in progress\. Connections that are up/,
			),
		).toBeInTheDocument();
	});

	test("warnings from the control plane are each shown", async () => {
		await loaded({ warnings: ["Tag owner missing", "Key expires soon"] });
		expect(screen.getByText("Tag owner missing")).toBeInTheDocument();
		expect(screen.getByText("Key expires soon")).toBeInTheDocument();
	});
});

describe("MeshSection: servers, links and rules", () => {
	const filled = () => ({
		nodes: [
			node({ serverId: "s-web", serverName: "web-02" }),
			node({ serverId: "s-db", serverName: "db-01", ips: ["100.64.0.2"] }),
		],
		links: [link()],
		rules: [rule()],
	});

	test("counts the servers and lists nodes, links and the rules derived from them", async () => {
		await loaded(filled());
		expect(
			within(section("Status")).getByText("2 servers in the mesh"),
		).toBeInTheDocument();
		const servers = within(section("Servers"));
		expect(servers.getByRole("link", { name: "web-02" })).toHaveAttribute(
			"href",
			"/servers/s-web/settings/mesh",
		);
		expect(servers.getByRole("link", { name: "db-01" })).toBeInTheDocument();
		expect(
			within(section("Links")).getByText("web-02 → db-01"),
		).toBeInTheDocument();
		const rules = within(section("Access rules"));
		expect(rules.getByText("production · web-02 → db-01")).toBeInTheDocument();
		expect(rules.getByText("100.64.0.7:15432")).toBeInTheDocument();
	});

	test("one server is counted in the singular", async () => {
		await loaded({ nodes: [node()] });
		expect(
			within(section("Status")).getByText("1 server in the mesh"),
		).toBeInTheDocument();
	});
});

describe("MeshSection: join automatically", () => {
	test("an admin turning it on sends autoJoin:true", async () => {
		const api = stubApi({
			...ADMIN,
			"GET /mesh": { body: meshView() },
			"PUT /mesh/settings": { body: meshView({ autoJoin: true }) },
		});
		const user = userEvent.setup();
		await renderSection();
		const toggle = await screen.findByRole("switch", {
			name: "Join servers automatically",
		});
		expect(toggle).not.toBeChecked();
		await user.click(toggle);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
				autoJoin: true,
			}),
		);
		expect(api.calls.find((c) => c.method === "PUT")?.path).toBe(
			"/mesh/settings",
		);
	});

	test("it reflects the saved setting", async () => {
		await loaded({ autoJoin: true });
		expect(
			screen.getByRole("switch", { name: "Join servers automatically" }),
		).toBeChecked();
		expect(screen.getByText("On")).toBeInTheDocument();
	});

	test("a principal that cannot write servers cannot toggle it", async () => {
		await loaded({}, { "GET /me": { body: meBody([]) } });
		isOff(screen.getByRole("switch", { name: "Join servers automatically" }));
	});

	test("a refused change toasts the reason", async () => {
		stubApi({
			...ADMIN,
			"GET /mesh": { body: meshView() },
			"PUT /mesh/settings": problem(409, {
				code: "mesh_paused",
				detail: "paused",
			}),
		});
		const user = userEvent.setup();
		await renderSection();
		await user.click(
			await screen.findByRole("switch", { name: "Join servers automatically" }),
		);
		expect(
			await screen.findByText("Could not change this setting"),
		).toBeInTheDocument();
		expect(
			screen.getByText("The mesh is paused. Changes wait until it resumes."),
		).toBeInTheDocument();
	});
});

describe("MeshSection: re-provisioning a link", () => {
	test("confirming posts to that link with an idempotency key and says it is under way", async () => {
		let key: string | null = null;
		const api = stubApi({
			...ADMIN,
			"GET /mesh": {
				body: meshView({ links: [link({ id: "link-9" })] }),
			},
			"POST /mesh/links/:id/reprovision": (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json({});
			},
		});
		const user = userEvent.setup();
		await renderSection();
		await user.click(
			await screen.findByRole("button", { name: "Re-provision" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Re-provision this link?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/production · web-02 → db-01 gets a new key/),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Re-provision" }),
		);
		expect(await screen.findByText("Re-provisioning")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/mesh/links/link-9/reprovision",
		);
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
	});

	test("a refusal toasts the reason", async () => {
		stubApi({
			...ADMIN,
			"GET /mesh": { body: meshView({ links: [link()] }) },
			"POST /mesh/links/:id/reprovision": problem(409, {
				code: "mesh_paused",
				detail: "paused",
			}),
		});
		const user = userEvent.setup();
		await renderSection();
		await user.click(
			await screen.findByRole("button", { name: "Re-provision" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Re-provision" }),
		);
		expect(
			await screen.findByText("Could not re-provision the link"),
		).toBeInTheDocument();
	});

	test("cancelling the confirmation posts nothing", async () => {
		const api = stubApi({
			...ADMIN,
			"GET /mesh": { body: meshView({ links: [link()] }) },
		});
		const user = userEvent.setup();
		await renderSection();
		await user.click(
			await screen.findByRole("button", { name: "Re-provision" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});

	test("a reader without servers:write sees the links but no Re-provision", async () => {
		await loaded({ links: [link()] }, { "GET /me": { body: meBody([]) } });
		expect(
			screen.queryByRole("button", { name: "Re-provision" }),
		).not.toBeInTheDocument();
	});
});

describe("MeshSection: access policy block", () => {
	test("an admin can change the mode", async () => {
		await loaded({}, ADMIN);
		expect(
			within(section("Access policy")).getByRole("button", { name: "Off" }),
		).toBeEnabled();
	});

	test("servers:write alone does not unlock the access policy", async () => {
		await loaded({}, { "GET /me": { body: meBody(["servers:write"]) } });
		expect(
			within(section("Access policy")).getByText(
				"Changing the access policy needs the admin scope.",
			),
		).toBeInTheDocument();
		expect(
			within(section("Access policy")).getByRole("button", { name: "Off" }),
		).toBeDisabled();
	});
});
