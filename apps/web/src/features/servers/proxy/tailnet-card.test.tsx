import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	ProxyTailnet,
	TailnetStatus,
	TailscaleIntegration,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { TailnetCard } from "./tailnet-card";

const tailnet = (over: Partial<ProxyTailnet> = {}): ProxyTailnet => ({
	appliedMode: "off",
	mode: "off",
	hostname: "nebula-edge-1",
	magicDns: false,
	integrationConfigured: true,
	hostTailscale: { detected: false },
	...over,
});

const integration = (
	over: Partial<TailscaleIntegration> = {},
): TailscaleIntegration => ({
	allowedTags: [],
	clientId: "k123",
	configured: true,
	defaultTags: ["tag:nebula-svc"],
	headscaleCredential: "apikey",
	headscaleUser: "",
	httpsEnabled: null,
	lastTest: { ok: false, at: null, error: "" },
	loginServer: "",
	provider: "tailscale_oauth",
	secretSet: true,
	tailnet: "-",
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

const SETTINGS = "PATCH /servers/:serverId/proxy/settings";

function renderCard(
	value: ProxyTailnet,
	{ agentTooOld = false }: { agentTooOld?: boolean } = {},
) {
	return renderWithProviders(
		<TailnetCard
			serverId="s1"
			serverName="edge-1"
			tailnet={value}
			agentTooOld={agentTooOld}
		/>,
	);
}

const mode = (name: string) => screen.getByRole("button", { name });

describe("TailnetCard modes", () => {
	test("picking Managed stages the mode and says it applies on the next deploy", async () => {
		const api = stubApi({
			"GET /me": MEMBER,
			[SETTINGS]: { body: make("ServerProxy") },
		});
		const user = userEvent.setup();
		await renderCard(tailnet());
		expect(mode("Off")).toHaveAttribute("aria-pressed", "true");
		expect(mode("Managed")).toBeEnabled();
		await user.click(mode("Managed"));
		expect(
			await screen.findByText("Tailnet change staged"),
		).toBeInTheDocument();
		expect(
			screen.getByText("It applies on the next Deploy proxy."),
		).toBeInTheDocument();
		const patch = api.calls.find((c) => c.method === "PATCH");
		expect(patch?.path).toBe("/servers/s1/proxy/settings");
		expect(patch?.body).toEqual({ tailnet: { mode: "managed" } });
		expect(api.unmatched).toEqual([]);
	});

	test("Host Tailscale is unavailable and explained while none is detected on the server", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(tailnet());
		expect(mode("Host Tailscale")).toBeDisabled();
		expect(
			screen.getByText("Host Tailscale is unavailable:"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Tailscale was not detected on this server."),
		).toBeInTheDocument();
	});

	test("Host Tailscale can be staged once it is detected", async () => {
		const api = stubApi({
			"GET /me": MEMBER,
			[SETTINGS]: { body: make("ServerProxy") },
		});
		const user = userEvent.setup();
		await renderCard(tailnet({ hostTailscale: { detected: true } }));
		expect(mode("Host Tailscale")).toBeEnabled();
		expect(screen.queryByText(/is unavailable/)).not.toBeInTheDocument();
		await user.click(mode("Host Tailscale"));
		await screen.findByText("Tailnet change staged");
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			tailnet: { mode: "host" },
		});
	});

	test("Managed is blocked, with a link to the integrations, when Tailscale is not set up", async () => {
		stubApi({
			"GET /me": ADMIN,
			"GET /integrations/tailscale": {
				body: integration({ configured: false }),
			},
		});
		await renderCard(tailnet({ integrationConfigured: false }));
		expect(mode("Managed")).toBeDisabled();
		expect(screen.getByText("Managed is unavailable:")).toBeInTheDocument();
		expect(
			screen.getByText(/Tailscale is not connected\./),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", {
				name: "Connect it in Team settings → Integrations",
			}),
		).toHaveAttribute("href", "/settings/integrations");
	});

	test("Managed is blocked for a Headscale integration, which cannot register the server's node", async () => {
		stubApi({
			"GET /me": ADMIN,
			"GET /integrations/tailscale": {
				body: integration({ provider: "headscale" }),
			},
		});
		await renderCard(tailnet());
		expect(
			await screen.findByText(/needs a Tailscale OAuth client/),
		).toBeInTheDocument();
		expect(mode("Managed")).toBeDisabled();
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
	});

	test("a Tailscale OAuth integration leaves Managed available", async () => {
		const api = stubApi({
			"GET /me": ADMIN,
			"GET /integrations/tailscale": { body: integration() },
		});
		await renderCard(tailnet());
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/integrations/tailscale")).toBe(
				true,
			),
		);
		expect(mode("Managed")).toBeEnabled();
		expect(
			screen.queryByText("Managed is unavailable:"),
		).not.toBeInTheDocument();
	});

	test("a staged mode shows the Staged badge and the mode's hint", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(tailnet({ mode: "managed", appliedMode: "off" }));
		expect(screen.getByText("Staged")).toBeInTheDocument();
		expect(
			screen.getByText(/runs a Tailscale node next to the proxy/),
		).toBeInTheDocument();
	});

	test("no Staged badge when the desired mode is the applied one", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(tailnet({ mode: "managed", appliedMode: "managed" }));
		expect(screen.queryByText("Staged")).not.toBeInTheDocument();
	});

	test("a failed staging shows the server's message", async () => {
		stubApi({
			"GET /me": MEMBER,
			[SETTINGS]: problem(422, "tailnet mode is not allowed", "invalid_mode"),
		});
		const user = userEvent.setup();
		await renderCard(tailnet());
		await user.click(mode("Managed"));
		expect(
			await screen.findByText("tailnet mode is not allowed"),
		).toBeInTheDocument();
		expect(screen.queryByText("Tailnet change staged")).not.toBeInTheDocument();
	});

	test("an agent_too_old refusal leaves the banner to explain, with no inline error", async () => {
		const api = stubApi({
			"GET /me": MEMBER,
			[SETTINGS]: problem(422, "agent predates proxy v2", "agent_too_old"),
		});
		const user = userEvent.setup();
		await renderCard(tailnet());
		await user.click(mode("Managed"));
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "PATCH")).toBe(true),
		);
		// The options lock while the request is pending and unlock once it failed.
		await waitFor(() => expect(mode("Managed")).toBeEnabled());
		expect(
			screen.queryByText("agent predates proxy v2"),
		).not.toBeInTheDocument();
		expect(screen.queryByText("Tailnet change staged")).not.toBeInTheDocument();
	});
});

describe("TailnetCard managed settings", () => {
	test("managed mode without the integration warns and links to it", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(
			tailnet({ mode: "managed", integrationConfigured: false }),
		);
		const alert = screen.getByRole("alert");
		expect(alert).toHaveTextContent(
			"Managed mode needs the Tailscale integration, which is not configured.",
		);
		expect(
			within(alert).getByRole("link", {
				name: "Open Team settings → Integrations",
			}),
		).toHaveAttribute("href", "/settings/integrations");
	});

	test("managed mode with a Headscale integration shows the egress blocker as an alert", async () => {
		stubApi({
			"GET /me": ADMIN,
			"GET /integrations/tailscale": {
				body: integration({ provider: "headscale" }),
			},
		});
		await renderCard(tailnet({ mode: "managed", appliedMode: "managed" }));
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("your integration is Headscale");
	});

	test("the machine name saves trimmed-as-typed, only when changed and valid", async () => {
		const api = stubApi({
			"GET /me": MEMBER,
			[SETTINGS]: { body: make("ServerProxy") },
		});
		const user = userEvent.setup();
		await renderCard(tailnet({ mode: "managed", appliedMode: "managed" }));
		const name = screen.getByLabelText("Machine name");
		const save = screen.getByRole("button", { name: "Save name" });
		expect(name).toHaveValue("nebula-edge-1");
		expect(save).toBeDisabled();

		await user.clear(name);
		await user.type(name, "Bad Name");
		expect(
			screen.getByText("Use lower-case letters, digits and dashes (max 63)."),
		).toBeInTheDocument();
		expect(name).toHaveAttribute("aria-invalid", "true");
		expect(save).toBeDisabled();

		await user.clear(name);
		await user.type(name, "nas-1");
		expect(
			screen.queryByText("Use lower-case letters, digits and dashes (max 63)."),
		).not.toBeInTheDocument();
		expect(save).toBeEnabled();
		await user.click(save);
		await screen.findByText("Tailnet change staged");
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			tailnet: { hostname: "nas-1" },
		});
	});

	test("an empty machine name is allowed to be saved (the server picks the default)", async () => {
		const api = stubApi({
			"GET /me": MEMBER,
			[SETTINGS]: { body: make("ServerProxy") },
		});
		const user = userEvent.setup();
		await renderCard(tailnet({ mode: "managed", appliedMode: "managed" }));
		await user.clear(screen.getByLabelText("Machine name"));
		await user.click(screen.getByRole("button", { name: "Save name" }));
		await screen.findByText("Tailnet change staged");
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			tailnet: { hostname: "" },
		});
	});

	test("the MagicDNS switch reflects the setting and stages the opposite", async () => {
		const api = stubApi({
			"GET /me": MEMBER,
			[SETTINGS]: { body: make("ServerProxy") },
		});
		const user = userEvent.setup();
		await renderCard(
			tailnet({ mode: "managed", appliedMode: "managed", magicDns: true }),
		);
		const toggle = screen.getByRole("switch");
		expect(toggle).toBeChecked();
		expect(screen.getByText(/100\.100\.100\.100/)).toBeInTheDocument();
		await user.click(toggle);
		await screen.findByText("Tailnet change staged");
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			tailnet: { magicDns: false },
		});
	});

	test("the machine name and MagicDNS controls only exist in managed mode", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(
			tailnet({ mode: "host", hostTailscale: { detected: true } }),
		);
		expect(screen.queryByLabelText("Machine name")).not.toBeInTheDocument();
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
	});
});

describe("TailnetCard host mode", () => {
	test("lists the detected addresses", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(
			tailnet({
				mode: "host",
				appliedMode: "host",
				hostTailscale: { detected: true, ips: ["100.64.0.2", "fd7a::2"] },
			}),
		);
		expect(
			screen.getByText(/Host Tailscale detected · 100\.64\.0\.2, fd7a::2\./),
		).toBeInTheDocument();
	});

	test("detected without addresses says so without a list", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(
			tailnet({
				mode: "host",
				appliedMode: "host",
				hostTailscale: { detected: true, ips: [] },
			}),
		);
		const line = screen.getByText(/Host Tailscale detected/);
		expect(line).not.toHaveTextContent("·");
		expect(line).toHaveTextContent(
			"Routes can target its 100.x addresses by IP.",
		);
	});

	test("warns when no Tailscale is detected", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(tailnet({ mode: "host", appliedMode: "host" }));
		expect(
			screen.getByText("No Tailscale was detected on this server."),
		).toBeInTheDocument();
	});
});

describe("TailnetCard status", () => {
	const status = (over: Partial<TailnetStatus> = {}): TailnetStatus => ({
		mode: "managed",
		state: "online",
		...over,
	});

	test("shows the node's name, addresses and last report with copy buttons", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(
			tailnet({
				mode: "managed",
				appliedMode: "managed",
				statusAt: "2026-01-01T00:00:00Z",
				status: status({
					fqdn: "edge-1.tail1234.ts.net.",
					ips: ["100.64.0.5", "fd7a::5"],
				}),
			}),
		);
		const region = screen.getByRole("region", { name: "Tailnet status" });
		expect(within(region).getByText("Online")).toBeInTheDocument();
		expect(within(region).getByText(/reported/)).toBeInTheDocument();
		// The DNS root dot is dropped from the name.
		expect(
			within(region).getByText("edge-1.tail1234.ts.net"),
		).toBeInTheDocument();
		expect(
			within(region).getByRole("button", { name: "Copy tailnet name" }),
		).toBeInTheDocument();
		expect(region).toHaveTextContent("100.64.0.5");
		expect(region).toHaveTextContent("fd7a::5");
		expect(
			within(region).getByRole("button", { name: "Copy tailnet addresses" }),
		).toBeInTheDocument();
	});

	test("shows a needs-attention node's message", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(
			tailnet({
				mode: "managed",
				appliedMode: "managed",
				status: status({
					state: "needs_attention",
					message: "auth key expired",
				}),
			}),
		);
		const region = screen.getByRole("region", { name: "Tailnet status" });
		expect(within(region).getByText("Needs attention")).toBeInTheDocument();
		expect(within(region).getByText("auth key expired")).toBeInTheDocument();
	});

	test("a node that never reported reads Not reported, without names or addresses", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(tailnet({ mode: "managed", appliedMode: "managed" }));
		const region = screen.getByRole("region", { name: "Tailnet status" });
		expect(within(region).getByText("Not reported")).toBeInTheDocument();
		expect(region).not.toHaveTextContent("reported ");
		expect(within(region).queryByRole("button")).not.toBeInTheDocument();
	});

	test("with the proxy off the status only appears when the agent still reports a state", async () => {
		stubApi({ "GET /me": MEMBER });
		const off = await renderCard(tailnet());
		expect(
			screen.queryByRole("region", { name: "Tailnet status" }),
		).not.toBeInTheDocument();
		off.unmount();

		stubApi({ "GET /me": MEMBER });
		await renderCard(tailnet({ status: status({ state: "offline" }) }));
		const region = screen.getByRole("region", { name: "Tailnet status" });
		// An off mode wins over the node's last state.
		expect(within(region).getByText("Off")).toBeInTheDocument();
	});
});

describe("TailnetCard re-provisioning", () => {
	test("is only offered once the managed node is applied", async () => {
		stubApi({ "GET /me": MEMBER });
		const staged = await renderCard(
			tailnet({ mode: "managed", appliedMode: "off" }),
		);
		expect(
			screen.queryByRole("button", { name: "Re-provision" }),
		).not.toBeInTheDocument();
		staged.unmount();

		stubApi({ "GET /me": MEMBER });
		await renderCard(tailnet({ mode: "managed", appliedMode: "managed" }));
		expect(screen.getByRole("button", { name: "Re-provision" })).toBeEnabled();
	});

	test("asks first, then posts with an idempotency key and confirms", async () => {
		let key: string | null = null;
		const api = stubApi({
			"GET /me": MEMBER,
			"POST /servers/:serverId/proxy/tailnet/reprovision": (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json({});
			},
		});
		const user = userEvent.setup();
		await renderCard(tailnet({ mode: "managed", appliedMode: "managed" }));
		await user.click(screen.getByRole("button", { name: "Re-provision" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Re-provision the tailnet node?"),
		).toBeInTheDocument();
		expect(dialog).toHaveTextContent("edge-1 registers again with a new key");
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Re-provision" }),
		);
		expect(await screen.findByText("Re-provisioning")).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/servers/s1/proxy/tailnet/reprovision");
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		expect(api.unmatched).toEqual([]);
	});

	test("cancelling sends nothing", async () => {
		const api = stubApi({ "GET /me": MEMBER });
		const user = userEvent.setup();
		await renderCard(tailnet({ mode: "managed", appliedMode: "managed" }));
		await user.click(screen.getByRole("button", { name: "Re-provision" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});

	test("a failed re-provision shows the error", async () => {
		stubApi({
			"GET /me": MEMBER,
			"POST /servers/:serverId/proxy/tailnet/reprovision": problem(
				502,
				"Tailscale API is down",
				"upstream",
			),
		});
		const user = userEvent.setup();
		await renderCard(tailnet({ mode: "managed", appliedMode: "managed" }));
		await user.click(screen.getByRole("button", { name: "Re-provision" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Re-provision" }),
		);
		expect(
			(await screen.findAllByText("Tailscale API is down")).length,
		).toBeGreaterThan(0);
		expect(screen.queryByText("Re-provisioning")).not.toBeInTheDocument();
	});
});

describe("TailnetCard with an agent that is too old", () => {
	test("shows the upgrade banner and disables every control", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(
			tailnet({
				mode: "managed",
				appliedMode: "managed",
				hostTailscale: { detected: true },
			}),
			{ agentTooOld: true },
		);
		expect(screen.getByTestId("agent-too-old")).toHaveTextContent(
			"The agent on edge-1 is too old for this",
		);
		expect(mode("Off")).toBeDisabled();
		expect(mode("Host Tailscale")).toBeDisabled();
		expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
		expect(screen.getByRole("button", { name: "Re-provision" })).toBeDisabled();
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Machine name"), "x");
		expect(screen.getByRole("button", { name: "Save name" })).toBeDisabled();
	});
});
