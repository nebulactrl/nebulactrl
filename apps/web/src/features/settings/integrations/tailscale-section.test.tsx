import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	TailscaleDevice,
	TailscaleIntegration,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { TailscaleSection } from "./tailscale-section";

type User = ReturnType<typeof userEvent.setup>;

/** Types `text` after what the field holds; pasting keeps long values fast. */
async function typeInto(user: User, field: HTMLElement, text: string) {
	await user.click(field);
	await user.keyboard("{End}");
	await user.paste(text);
}

const notConnected: TailscaleIntegration = {
	allowedTags: null,
	clientId: "",
	configured: false,
	defaultTags: null,
	headscaleCredential: "apikey",
	headscaleUser: "",
	httpsEnabled: null,
	lastTest: { at: null, ok: null },
	loginServer: "",
	provider: "tailscale_oauth",
	secretSet: false,
	tailnet: "-",
};

const oauth: TailscaleIntegration = {
	...notConnected,
	clientId: "kABC123",
	configured: true,
	defaultTags: ["tag:nebula-svc"],
	httpsEnabled: true,
	secretSet: true,
};

const headscale: TailscaleIntegration = {
	...notConnected,
	configured: true,
	provider: "headscale",
	loginServer: "https://hs.example.com",
	headscaleCredential: "apikey",
	headscaleUser: "nebula",
	defaultTags: ["tag:nebula-svc"],
	secretSet: true,
};

const device = (over: Partial<TailscaleDevice>): TailscaleDevice => ({
	addresses: ["100.64.0.2", "fd7a::2"],
	hostname: "proxy-1",
	id: "d1",
	lastSeen: null,
	name: "proxy-1.tail1234.ts.net",
	online: true,
	os: "linux",
	tags: ["tag:nebula-svc"],
	...over,
});

const problem = (status: number, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code: "bad" },
});

// The Headscale "Set up for me" button reads these; a failed load hides it.
const sideRoutes = {
	"GET /platform-services": problem(500, "none"),
	"GET /servers": problem(500, "none"),
};

describe("TailscaleSection loading states", () => {
	test("a non-admin sees the forbidden explanation without a retry", async () => {
		stubApi({ "GET /integrations/tailscale": problem(403, "admins only") });
		await renderWithProviders(<TailscaleSection />);
		expect(
			await screen.findByText("Only administrators can manage Tailscale"),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /try again|retry/i }),
		).not.toBeInTheDocument();
	});

	test("a failed load offers a retry that recovers", async () => {
		let attempts = 0;
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json(notConnected);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		expect(
			await screen.findByText("Tailscale did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByLabelText("Client ID")).toBeInTheDocument();
	});
});

describe("TailscaleSection OAuth connection", () => {
	test("a fresh install shows the unconnected form and the default policy", async () => {
		const api = stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: notConnected },
		});
		await renderWithProviders(<TailscaleSection />);
		expect(await screen.findByText("Not connected")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Connect Tailscale" }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Test connection" }),
		).toBeDisabled();
		// Default tags are pre-filled; the tailnet field shows its placeholder.
		expect(screen.getByLabelText("Default tags")).toHaveValue("tag:nebula-svc");
		expect(screen.getByLabelText("Tailnet")).toHaveValue("");
		const policy = screen.getByRole("region", { name: "Tailnet policy" });
		expect(policy).toHaveTextContent('"tag:nebula-svc": ["tag:nebula"]');
		expect(
			screen.queryByRole("button", { name: "Remove" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("region", { name: "Devices" }),
		).not.toBeInTheDocument();
		expect(api.calls.filter((c) => c.method !== "GET")).toEqual([]);
	});

	test("connecting sends the trimmed client id, secret, tags and the default tailnet", async () => {
		let current: TailscaleIntegration = notConnected;
		const api = stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": () => Response.json(current),
			"GET /integrations/tailscale/devices": { body: { items: [] } },
			"PUT /integrations/tailscale": () => {
				current = oauth;
				return Response.json(oauth);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);

		await typeInto(
			user,
			await screen.findByLabelText("Client ID"),
			"  kABC123 ",
		);
		const connect = screen.getByRole("button", { name: "Connect Tailscale" });
		// A secret is required for a first connection.
		expect(connect).toBeDisabled();
		await typeInto(
			user,
			screen.getByLabelText("Client secret"),
			"tskey-client-xyz",
		);
		expect(connect).toBeEnabled();
		await user.click(connect);

		expect(await screen.findByText("Tailscale saved")).toBeInTheDocument();
		expect(
			screen.getByText("Run the connection test to check it."),
		).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/integrations/tailscale");
		expect(put?.body).toEqual({
			provider: "tailscale_oauth",
			clientId: "kABC123",
			tailnet: "-",
			clientSecret: "tskey-client-xyz",
			defaultTags: ["tag:nebula-svc"],
			allowedTags: null,
		});
		expect(await screen.findByText("Connected")).toBeInTheDocument();
		expect(screen.getByLabelText("Client secret")).toHaveValue("");
		expect(api.unmatched).toEqual([]);
	});

	test("custom tags, allowed tags and a named tailnet are sent as typed", async () => {
		const api = stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: notConnected },
			"PUT /integrations/tailscale": { body: oauth },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await typeInto(user, await screen.findByLabelText("Client ID"), "cid");
		await typeInto(user, screen.getByLabelText("Client secret"), "sec");
		await user.clear(screen.getByLabelText("Default tags"));
		await typeInto(
			user,
			screen.getByLabelText("Default tags"),
			"tag:a, tag:b tag:a",
		);
		await typeInto(
			user,
			screen.getByLabelText("Allowed tags"),
			"tag:a,tag:b,tag:c",
		);
		await typeInto(user, screen.getByLabelText("Tailnet"), " example.org ");
		await user.click(screen.getByRole("button", { name: "Connect Tailscale" }));

		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")).toBeDefined(),
		);
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			provider: "tailscale_oauth",
			clientId: "cid",
			tailnet: "example.org",
			clientSecret: "sec",
			defaultTags: ["tag:a", "tag:b"],
			allowedTags: ["tag:a", "tag:b", "tag:c"],
		});
	});

	test("a malformed tag blocks saving and says why", async () => {
		const api = stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: notConnected },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await typeInto(user, await screen.findByLabelText("Client ID"), "cid");
		await typeInto(user, screen.getByLabelText("Client secret"), "sec");
		await user.clear(screen.getByLabelText("Default tags"));
		await typeInto(user, screen.getByLabelText("Default tags"), "nebula");

		expect(
			screen.getByText(
				"“nebula” is not a tag. Use tag:name, starting with a letter.",
			),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Default tags")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(
			screen.getByRole("button", { name: "Connect Tailscale" }),
		).toBeDisabled();
		expect(api.calls.some((c) => c.method === "PUT")).toBe(false);
	});

	test("empty default tags and allowed tags missing a default are refused", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: notConnected },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await typeInto(user, await screen.findByLabelText("Client ID"), "cid");
		await typeInto(user, screen.getByLabelText("Client secret"), "sec");
		await user.clear(screen.getByLabelText("Default tags"));
		expect(
			screen.getByText("Give at least one default tag."),
		).toBeInTheDocument();

		await typeInto(user, screen.getByLabelText("Default tags"), "tag:a");
		await typeInto(user, screen.getByLabelText("Allowed tags"), "tag:b");
		expect(
			screen.getByText("Allowed tags must include every default tag (tag:a)."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Connect Tailscale" }),
		).toBeDisabled();
	});

	test("a rejected save shows the server's message and keeps the input", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: notConnected },
			"PUT /integrations/tailscale": problem(422, "client id not recognised"),
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await typeInto(user, await screen.findByLabelText("Client ID"), "cid");
		await typeInto(user, screen.getByLabelText("Client secret"), "sec");
		await user.click(screen.getByRole("button", { name: "Connect Tailscale" }));
		expect(
			await screen.findByText("client id not recognised"),
		).toBeInTheDocument();
		expect(screen.queryByText("Tailscale saved")).not.toBeInTheDocument();
		expect(screen.getByLabelText("Client secret")).toHaveValue("sec");
	});

	test("a connected form keeps the stored secret: saving needs no new one", async () => {
		const api = stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": { body: { items: [] } },
			"PUT /integrations/tailscale": { body: oauth },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		const secret = await screen.findByLabelText("Client secret");
		expect(secret).toHaveAttribute(
			"placeholder",
			"Stored, leave empty to keep",
		);
		expect(screen.getByLabelText("Client ID")).toHaveValue("kABC123");
		expect(screen.getByText("Connected")).toBeInTheDocument();

		await typeInto(
			user,
			screen.getByLabelText("Allowed tags"),
			"tag:nebula-svc, tag:x",
		);
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")).toBeDefined(),
		);
		// No clientSecret field when the stored one is kept.
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			provider: "tailscale_oauth",
			clientId: "kABC123",
			tailnet: "-",
			defaultTags: ["tag:nebula-svc"],
			allowedTags: ["tag:nebula-svc", "tag:x"],
		});
	});

	test("changing the OAuth client id demands a new secret", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		const clientId = await screen.findByLabelText("Client ID");
		await user.clear(clientId);
		await typeInto(user, clientId, "kOTHER");
		expect(screen.getByLabelText("Client secret")).toHaveAttribute(
			"placeholder",
			"tskey-client-…",
		);
		expect(
			screen.getByText(/stored secret belongs to the previous connection/),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
		await typeInto(user, screen.getByLabelText("Client secret"), "new");
		expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();
	});

	test("shows an HTTPS-off warning only for a configured OAuth tailnet", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": {
				body: { ...oauth, httpsEnabled: false },
			},
			"GET /integrations/tailscale/devices": { body: { items: [] } },
		});
		await renderWithProviders(<TailscaleSection />);
		expect(
			await screen.findByText("HTTPS certificates are off for this tailnet"),
		).toBeInTheDocument();
	});

	test("no HTTPS warning when HTTPS is on or unknown", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: { ...oauth, httpsEnabled: null } },
			"GET /integrations/tailscale/devices": { body: { items: [] } },
		});
		await renderWithProviders(<TailscaleSection />);
		await screen.findByText("Connected");
		expect(
			screen.queryByText("HTTPS certificates are off for this tailnet"),
		).not.toBeInTheDocument();
	});
});

describe("TailscaleSection testing the connection", () => {
	test("posts the test and lists each check with its outcome", async () => {
		const api = stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": { body: { items: [] } },
			"POST /integrations/tailscale/test": {
				body: {
					ok: false,
					checks: [
						{ id: "oauth", ok: true, message: "token minted" },
						{ id: "devices", ok: false, message: "scope devices:core missing" },
						{
							id: "acl",
							ok: false,
							skipped: true,
							message: "needs policy scope",
						},
					],
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);

		const list = await screen.findByRole("list", { name: "Connection checks" });
		const items = within(list).getAllByRole("listitem");
		expect(items).toHaveLength(4);
		expect(items[0]).toHaveTextContent("oauth passed");
		expect(items[0]).toHaveTextContent("token minted");
		expect(items[1]).toHaveTextContent("devices failed");
		expect(items[1]).toHaveTextContent("scope devices:core missing");
		expect(items[2]).toHaveTextContent("acl skipped");
		expect(items[3]).toHaveTextContent(
			"Something failed: fix it and test again.",
		);
		expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1);
	});

	test("an all-green test says everything works", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": { body: { items: [] } },
			"POST /integrations/tailscale/test": {
				body: { ok: true, checks: null },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);
		expect(
			await screen.findByText("Everything NebulaCtrl needs works."),
		).toBeInTheDocument();
	});

	test("a request failure shows the error under the form", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": { body: { items: [] } },
			"POST /integrations/tailscale/test": problem(
				502,
				"tailscale unreachable",
			),
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);
		expect(
			await screen.findByText("tailscale unreachable"),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("list", { name: "Connection checks" }),
		).not.toBeInTheDocument();
	});

	test("the stored last-test failure is shown until a new test replaces it", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": {
				body: {
					...oauth,
					lastTest: {
						at: "2026-01-01T00:00:00Z",
						ok: false,
						error: "invalid_client",
					},
				},
			},
			"GET /integrations/tailscale/devices": { body: { items: [] } },
		});
		await renderWithProviders(<TailscaleSection />);
		expect(await screen.findByText("Last test failed")).toBeInTheDocument();
		expect(screen.getByText("The last test failed")).toBeInTheDocument();
		expect(screen.getByText("invalid_client")).toBeInTheDocument();
		expect(screen.getByText(/^tested/)).toBeInTheDocument();
	});

	test("editing the form disables the test until the changes are saved", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		const test = await screen.findByRole("button", { name: "Test connection" });
		expect(test).toBeEnabled();
		await typeInto(
			user,
			screen.getByLabelText("Allowed tags"),
			"tag:nebula-svc",
		);
		expect(test).toBeDisabled();
		expect(test).toHaveAttribute(
			"title",
			"Save your changes first: the test uses the saved connection",
		);
	});

	test("a successful save clears an earlier test result", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": { body: { items: [] } },
			"POST /integrations/tailscale/test": { body: { ok: true, checks: [] } },
			"PUT /integrations/tailscale": { body: oauth },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);
		await screen.findByRole("list", { name: "Connection checks" });
		await typeInto(
			user,
			screen.getByLabelText("Allowed tags"),
			"tag:nebula-svc",
		);
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		await screen.findByText("Tailscale saved");
		expect(
			screen.queryByRole("list", { name: "Connection checks" }),
		).not.toBeInTheDocument();
	});
});

describe("TailscaleSection Headscale", () => {
	test("switching provider shows the Headscale fields and policy", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: notConnected },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await user.click(await screen.findByRole("button", { name: "Headscale" }));

		expect(screen.getByLabelText("Login server URL")).toBeInTheDocument();
		expect(screen.queryByLabelText("Client ID")).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Tailnet")).not.toBeInTheDocument();
		expect(screen.getByLabelText("Headscale user")).toBeInTheDocument();
		expect(screen.getByLabelText("API key")).toBeInTheDocument();
		expect(
			screen.getByText(/Headscale has no Serve HTTPS or Funnel/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Connect Headscale" }),
		).toBeDisabled();
		// The policy panel documents the saved connection, not the draft.
		expect(
			screen.getByRole("region", { name: "Tailnet policy" }),
		).toBeInTheDocument();
	});

	test("an invalid login server is flagged and blocks connecting", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: notConnected },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await user.click(await screen.findByRole("button", { name: "Headscale" }));
		await typeInto(
			user,
			screen.getByLabelText("Login server URL"),
			"headscale",
		);
		expect(
			screen.getByText("Use a full URL such as https://headscale.example.com."),
		).toBeInTheDocument();
		await user.clear(screen.getByLabelText("Login server URL"));
		await typeInto(
			user,
			screen.getByLabelText("Login server URL"),
			"ftp://hs.example.com",
		);
		expect(
			screen.getByText("Use an http:// or https:// URL."),
		).toBeInTheDocument();
		await user.clear(screen.getByLabelText("Login server URL"));
		await typeInto(
			user,
			screen.getByLabelText("Login server URL"),
			"https://u:p@hs.example.com",
		);
		expect(
			screen.getByText("Leave out credentials, a query and a fragment."),
		).toBeInTheDocument();
		await typeInto(user, screen.getByLabelText("API key"), "key");
		expect(
			screen.getByRole("button", { name: "Connect Headscale" }),
		).toBeDisabled();
	});

	test("connecting with an API key sends the server, credential kind, user and key", async () => {
		const api = stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: notConnected },
			"PUT /integrations/tailscale": { body: headscale },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await user.click(await screen.findByRole("button", { name: "Headscale" }));
		await typeInto(
			user,
			screen.getByLabelText("Login server URL"),
			" https://hs.example.com ",
		);
		await typeInto(user, screen.getByLabelText("Headscale user"), " nebula ");
		await typeInto(user, screen.getByLabelText("API key"), "hskey");
		await user.click(screen.getByRole("button", { name: "Connect Headscale" }));

		expect(await screen.findByText("Headscale saved")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			provider: "headscale",
			loginServer: "https://hs.example.com",
			headscaleCredential: "apikey",
			headscaleUser: "nebula",
			clientSecret: "hskey",
			defaultTags: ["tag:nebula-svc"],
			allowedTags: null,
		});
	});

	test("a pre-auth key omits the Headscale user", async () => {
		const api = stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: notConnected },
			"PUT /integrations/tailscale": {
				body: { ...headscale, headscaleCredential: "preauth" },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await user.click(await screen.findByRole("button", { name: "Headscale" }));
		await user.click(screen.getByRole("button", { name: "Pre-auth key" }));
		expect(screen.queryByLabelText("Headscale user")).not.toBeInTheDocument();
		await typeInto(
			user,
			screen.getByLabelText("Login server URL"),
			"http://hs.local:8080",
		);
		expect(screen.getByLabelText("Pre-auth key")).toHaveAttribute(
			"placeholder",
			"Reusable tagged pre-auth key",
		);
		await typeInto(user, screen.getByLabelText("Pre-auth key"), "preauth-1");
		await user.click(screen.getByRole("button", { name: "Connect Headscale" }));

		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")).toBeDefined(),
		);
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			provider: "headscale",
			loginServer: "http://hs.local:8080",
			headscaleCredential: "preauth",
			clientSecret: "preauth-1",
			defaultTags: ["tag:nebula-svc"],
			allowedTags: null,
		});
	});

	test("a connected API-key Headscale keeps its secret and lists its devices", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: headscale },
			"GET /integrations/tailscale/devices": {
				body: { items: [device({})] },
			},
		});
		await renderWithProviders(<TailscaleSection />);
		expect(await screen.findByLabelText("Login server URL")).toHaveValue(
			"https://hs.example.com",
		);
		expect(screen.getByLabelText("Headscale user")).toHaveValue("nebula");
		expect(screen.getByLabelText("API key")).toHaveAttribute(
			"placeholder",
			"Stored, leave empty to keep",
		);
		expect(
			screen.getByRole("region", { name: "Headscale policy" }),
		).toHaveTextContent('"tag:nebula-svc": ["nebula@"]');
		expect(await screen.findByText("proxy-1")).toBeInTheDocument();
		// A trailing slash is the same server: the stored secret still applies.
		const user = userEvent.setup();
		await typeInto(user, screen.getByLabelText("Login server URL"), "/");
		expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();
	});

	test("a pre-auth Headscale has no device list", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": {
				body: { ...headscale, headscaleCredential: "preauth" },
			},
		});
		await renderWithProviders(<TailscaleSection />);
		await screen.findByLabelText("Login server URL");
		expect(
			screen.queryByRole("region", { name: "Devices" }),
		).not.toBeInTheDocument();
		// And no HTTPS warning, which concerns Tailscale only.
		expect(
			screen.queryByText("HTTPS certificates are off for this tailnet"),
		).not.toBeInTheDocument();
	});

	test("switching to another Headscale server demands a new secret", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: headscale },
			"GET /integrations/tailscale/devices": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		const server = await screen.findByLabelText("Login server URL");
		await user.clear(server);
		await typeInto(user, server, "https://other.example.com");
		expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
		await typeInto(user, screen.getByLabelText("API key"), "new");
		expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();
	});
});

describe("TailscaleSection devices", () => {
	test("lists devices with address, tags and state", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": {
				body: {
					items: [
						device({}),
						device({
							id: "d2",
							hostname: "db-box",
							name: "db-box.tail1234.ts.net",
							addresses: ["fd7a::9"],
							tags: null,
							online: false,
							lastSeen: new Date(Date.now() - 3_600_000).toISOString(),
						}),
					],
				},
			},
		});
		await renderWithProviders(<TailscaleSection />);
		const section = await screen.findByRole("region", { name: "Devices" });
		const online = (await within(section).findByText("proxy-1")).closest("tr");
		expect(online).not.toBeNull();
		expect(online as HTMLElement).toHaveTextContent("100.64.0.2");
		expect(online as HTMLElement).toHaveTextContent("tag:nebula-svc");
		expect(online as HTMLElement).toHaveTextContent("Online");
		const offline = within(section).getByText("db-box").closest("tr");
		expect(offline as HTMLElement).toHaveTextContent("fd7a::9");
		expect(offline as HTMLElement).toHaveTextContent("none");
		expect(offline as HTMLElement).toHaveTextContent("Offline");
		expect(offline as HTMLElement).toHaveTextContent("seen");
		expect(online as HTMLElement).not.toHaveTextContent("seen");
	});

	test("an empty tailnet explains where nodes will appear", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": { body: { items: null } },
		});
		await renderWithProviders(<TailscaleSection />);
		expect(await screen.findByText(/No devices yet\./)).toBeInTheDocument();
	});

	test("a failed device load shows the reason without hiding the form", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": problem(
				502,
				"tailnet unreachable",
			),
		});
		await renderWithProviders(<TailscaleSection />);
		expect(
			await screen.findByText("Devices did not load: tailnet unreachable"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Client ID")).toBeInTheDocument();
	});
});

describe("TailscaleSection policy panel", () => {
	test("the policy follows the saved default tags", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": {
				body: { ...oauth, defaultTags: ["tag:web", "tag:db"] },
			},
			"GET /integrations/tailscale/devices": { body: { items: [] } },
		});
		await renderWithProviders(<TailscaleSection />);
		const policy = await screen.findByRole("region", {
			name: "Tailnet policy",
		});
		expect(policy).toHaveTextContent('"tag:web": ["tag:nebula"]');
		expect(policy).toHaveTextContent('"tag:db": ["tag:nebula"]');
		expect(policy).toHaveTextContent("auth_keys and devices:core");
	});

	test("Copy policy puts the policy snippet on the clipboard", async () => {
		const writes: string[] = [];
		const user = userEvent.setup();
		// userEvent.setup() installs its own clipboard, so ours goes on after it.
		const descriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: {
				writeText: async (text: string) => {
					writes.push(text);
				},
			},
		});
		try {
			stubApi({
				...sideRoutes,
				"GET /integrations/tailscale": { body: notConnected },
			});
			await renderWithProviders(<TailscaleSection />);
			await user.click(
				await screen.findByRole("button", { name: /Copy policy/ }),
			);
			await waitFor(() => expect(writes).toHaveLength(1));
			expect(writes[0]).toContain('"tag:nebula-svc": ["tag:nebula"]');
			expect(writes[0]).toContain('"tagOwners"');
		} finally {
			if (descriptor) Object.defineProperty(navigator, "clipboard", descriptor);
			else Reflect.deleteProperty(navigator, "clipboard");
		}
	});
});

describe("TailscaleSection removal", () => {
	test("asks first, then deletes and reports", async () => {
		let current: TailscaleIntegration = oauth;
		const api = stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": () => Response.json(current),
			"GET /integrations/tailscale/devices": { body: { items: [] } },
			"DELETE /integrations/tailscale": () => {
				current = notConnected;
				return new Response(null, { status: 204 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await user.click(await screen.findByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Remove the Tailscale integration?"),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		await user.click(
			within(dialog).getByRole("button", { name: "Remove integration" }),
		);

		expect(await screen.findByText("Tailscale removed")).toBeInTheDocument();
		expect(
			screen.getByText("Servers keep the tailnet nodes they already have."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/integrations/tailscale",
		);
		expect(await screen.findByText("Not connected")).toBeInTheDocument();
	});

	test("cancelling sends nothing", async () => {
		const api = stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await user.click(await screen.findByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
	});

	test("a failed delete keeps the dialog open with the reason and a toast", async () => {
		stubApi({
			...sideRoutes,
			"GET /integrations/tailscale": { body: oauth },
			"GET /integrations/tailscale/devices": { body: { items: [] } },
			"DELETE /integrations/tailscale": problem(500, "still in use"),
		});
		const user = userEvent.setup();
		await renderWithProviders(<TailscaleSection />);
		await user.click(await screen.findByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Remove integration" }),
		);
		expect(
			await screen.findByText("Could not remove Tailscale"),
		).toBeInTheDocument();
		expect(within(dialog).getByText("still in use")).toBeInTheDocument();
		expect(screen.queryByText("Tailscale removed")).not.toBeInTheDocument();
	});
});
