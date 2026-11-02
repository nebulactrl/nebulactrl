import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	CloudflareConnectionView,
	DnsCredential,
	IpListView,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CloudflareSection } from "./cloudflare-section";

const unconfigured: CloudflareConnectionView = make(
	"CloudflareConnectionView",
	{
		accountId: "",
		accountName: "",
		accounts: null,
		configured: false,
		status: "",
		statusMessage: "",
		tokenExpiresAt: null,
		verifiedAt: null,
		warnings: null,
		zoneCount: 0,
		zonesSyncedAt: null,
	},
);

const connected: CloudflareConnectionView = make("CloudflareConnectionView", {
	...unconfigured,
	accountId: "acc-1",
	accountName: "Acme",
	accounts: [{ id: "acc-1", name: "Acme" }],
	configured: true,
	status: "ok",
	zoneCount: 3,
	zonesSyncedAt: "2026-01-01T00:00:00Z",
});

const ips: IpListView = make("IPListView", {
	builtinDate: "2025-12-01",
	changedAt: null,
	failingSince: null,
	fetchedAt: null,
	hash: "h",
	lastError: "",
	source: "api",
	stale: false,
	v4Count: 15,
	v6Count: 7,
});

const problem = (status: number, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code: "bad" },
});

const cloudflareCredential = make("DNSCredential", {
	id: "cred-cf",
	name: "Wildcard token",
	provider: "cloudflare",
	scope: "team",
});
const route53Credential = make("DNSCredential", {
	id: "cred-r53",
	name: "AWS",
	provider: "route53",
	scope: "team",
});

/** A 200 reply listing `items` as the DNS-01 credentials. */
const credentials = (...items: DnsCredential[]) => ({
	body: make("CredentialsOutputBody", { items }),
});

describe("CloudflareSection", () => {
	test("a first connection needs a token, sends it trimmed and confirms", async () => {
		let connection: CloudflareConnectionView = unconfigured;
		const api = stubApi({
			"GET /integrations/cloudflare": () => Response.json(connection),
			"GET /dns/credentials": credentials(),
			"PUT /integrations/cloudflare": () => {
				connection = connected;
				return Response.json(connected);
			},
			"GET /cloudflare/ips": { body: ips },
		});
		const user = userEvent.setup();
		await renderWithProviders(<CloudflareSection />);

		expect(await screen.findByText("Not connected")).toBeInTheDocument();
		const connect = screen.getByRole("button", { name: "Connect Cloudflare" });
		expect(connect).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Test connection" }),
		).toBeDisabled();
		expect(
			screen.queryByRole("button", { name: "Disconnect" }),
		).not.toBeInTheDocument();
		expect(screen.queryByText("Cloudflare IP ranges")).not.toBeInTheDocument();

		await user.type(screen.getByLabelText("API token"), "  cf-token-1  ");
		expect(connect).toBeEnabled();
		await user.click(connect);

		expect(await screen.findByText("Cloudflare connected")).toBeInTheDocument();
		expect(
			screen.getByText("Domains in your zones are being matched."),
		).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/integrations/cloudflare");
		expect(put?.body).toEqual({ token: "cf-token-1" });
		// The form now shows the stored connection and clears the typed secret.
		expect(await screen.findByText("Connected")).toBeInTheDocument();
		expect(screen.getByLabelText("API token")).toHaveValue("");
		expect(screen.getByLabelText("API token")).toHaveAttribute(
			"placeholder",
			"Stored, leave empty to keep",
		);
		expect(api.unmatched).toEqual([]);
	});

	test("an invalid-token response keeps the form and shows the server's reason", async () => {
		const api = stubApi({
			"GET /integrations/cloudflare": { body: unconfigured },
			"GET /dns/credentials": credentials(),
			"PUT /integrations/cloudflare": problem(
				422,
				"Cloudflare says the token is invalid",
			),
		});
		const user = userEvent.setup();
		await renderWithProviders(<CloudflareSection />);

		await user.type(await screen.findByLabelText("API token"), "bad");
		await user.click(
			screen.getByRole("button", { name: "Connect Cloudflare" }),
		);

		expect(
			await screen.findByText("Cloudflare says the token is invalid"),
		).toBeInTheDocument();
		expect(screen.queryByText("Cloudflare connected")).not.toBeInTheDocument();
		// The typed token is kept so the user can fix it.
		expect(screen.getByLabelText("API token")).toHaveValue("bad");
		expect(api.unmatched).toEqual([]);
	});

	test("a connected form shows zones, account and refresh time; save needs a change", async () => {
		stubApi({
			"GET /integrations/cloudflare": { body: connected },
			"GET /dns/credentials": credentials(),
			"GET /cloudflare/ips": { body: ips },
		});
		await renderWithProviders(<CloudflareSection />);

		expect(await screen.findByText("Connected")).toBeInTheDocument();
		expect(screen.getByText(/3 zones · Acme · refreshed/)).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Test connection" }),
		).toBeEnabled();
	});

	test("says zone in the singular for one zone", async () => {
		stubApi({
			"GET /integrations/cloudflare": {
				body: { ...connected, zoneCount: 1, accountName: "" },
			},
			"GET /dns/credentials": credentials(),
			"GET /cloudflare/ips": { body: ips },
		});
		await renderWithProviders(<CloudflareSection />);
		expect(await screen.findByText(/^1 zone/)).toBeInTheDocument();
		expect(screen.queryByText(/1 zones/)).not.toBeInTheDocument();
	});

	test("a rejected token shows the failure and the stored status", async () => {
		stubApi({
			"GET /integrations/cloudflare": {
				body: {
					...connected,
					status: "error",
					statusMessage: "Token expired on 2026-02-01",
					warnings: ["Token expires in 3 days"],
				},
			},
			"GET /dns/credentials": credentials(),
			"GET /cloudflare/ips": { body: ips },
		});
		await renderWithProviders(<CloudflareSection />);

		expect(await screen.findByText("Token rejected")).toBeInTheDocument();
		expect(
			screen.getByText("Cloudflare rejected the token"),
		).toBeInTheDocument();
		expect(screen.getByText(/Token expired on 2026-02-01/)).toBeInTheDocument();
		expect(screen.getByText("Token expires in 3 days")).toBeInTheDocument();
	});

	test("a rejected token without a message asks for a new one", async () => {
		stubApi({
			"GET /integrations/cloudflare": {
				body: { ...connected, status: "error", statusMessage: "" },
			},
			"GET /dns/credentials": credentials(),
			"GET /cloudflare/ips": { body: ips },
		});
		await renderWithProviders(<CloudflareSection />);
		expect(await screen.findByText(/Paste a new token\./)).toBeInTheDocument();
	});

	test("with several accounts the tunnel account can be changed without a token", async () => {
		const api = stubApi({
			"GET /integrations/cloudflare": {
				body: {
					...connected,
					accounts: [
						{ id: "acc-1", name: "Acme" },
						{ id: "acc-2", name: "" },
					],
				},
			},
			"GET /dns/credentials": credentials(),
			"PUT /integrations/cloudflare": { body: connected },
			"GET /cloudflare/ips": { body: ips },
		});
		const user = userEvent.setup();
		await renderWithProviders(<CloudflareSection />);

		const save = await screen.findByRole("button", { name: "Save changes" });
		expect(save).toBeDisabled();
		await user.click(screen.getByLabelText("Account for tunnels"));
		// An account without a name is labelled by its id.
		await user.click(await screen.findByRole("option", { name: "acc-2" }));
		expect(save).toBeEnabled();
		await user.click(save);

		expect(await screen.findByText("Cloudflare updated")).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.body).toEqual({ accountId: "acc-2" });
	});

	test("a single account offers no account picker", async () => {
		stubApi({
			"GET /integrations/cloudflare": { body: connected },
			"GET /dns/credentials": credentials(),
			"GET /cloudflare/ips": { body: ips },
		});
		await renderWithProviders(<CloudflareSection />);
		await screen.findByText("Connected");
		expect(
			screen.queryByLabelText("Account for tunnels"),
		).not.toBeInTheDocument();
	});

	test("reusing a Cloudflare DNS-01 credential sends its id instead of a token", async () => {
		const api = stubApi({
			"GET /integrations/cloudflare": { body: unconfigured },
			"GET /dns/credentials": credentials(
				cloudflareCredential,
				route53Credential,
			),
			"PUT /integrations/cloudflare": { body: connected },
		});
		const user = userEvent.setup();
		await renderWithProviders(<CloudflareSection />);

		await user.click(
			await screen.findByLabelText("Or reuse a DNS-01 credential"),
		);
		// Only Cloudflare credentials are offered.
		expect(
			screen.queryByRole("option", { name: "AWS" }),
		).not.toBeInTheDocument();
		await user.click(
			await screen.findByRole("option", { name: "Wildcard token" }),
		);
		await user.click(
			screen.getByRole("button", { name: "Connect Cloudflare" }),
		);

		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")).toBeDefined(),
		);
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			dns01CredentialId: "cred-cf",
		});
	});

	test("typing a token hides the credential picker and wins over it", async () => {
		stubApi({
			"GET /integrations/cloudflare": { body: unconfigured },
			"GET /dns/credentials": credentials(cloudflareCredential),
		});
		const user = userEvent.setup();
		await renderWithProviders(<CloudflareSection />);
		await screen.findByLabelText("Or reuse a DNS-01 credential");
		await user.type(screen.getByLabelText("API token"), "x");
		expect(
			screen.queryByLabelText("Or reuse a DNS-01 credential"),
		).not.toBeInTheDocument();
	});

	test("no Cloudflare credentials means no credential picker", async () => {
		stubApi({
			"GET /integrations/cloudflare": { body: unconfigured },
			"GET /dns/credentials": credentials(route53Credential),
		});
		await renderWithProviders(<CloudflareSection />);
		await screen.findByLabelText("API token");
		expect(
			screen.queryByLabelText("Or reuse a DNS-01 credential"),
		).not.toBeInTheDocument();
	});

	test("the tunnel checkbox changes the permissions the guide lists", async () => {
		stubApi({
			"GET /integrations/cloudflare": { body: unconfigured },
			"GET /dns/credentials": credentials(),
		});
		const user = userEvent.setup();
		await renderWithProviders(<CloudflareSection />);
		const guide = await screen.findByRole("region", {
			name: /credential setup/,
		});
		const withTunnel = guide.textContent;
		const box = screen.getByRole("checkbox", {
			name: "Also manage Cloudflare Tunnels",
		});
		expect(box).toBeChecked();
		await user.click(box);
		expect(box).not.toBeChecked();
		const without = screen.getByRole("region", {
			name: /credential setup/,
		}).textContent;
		expect(without).not.toBe(withTunnel);
	});

	test("Test connection reports the zones the token sees", async () => {
		const api = stubApi({
			"GET /integrations/cloudflare": { body: connected },
			"GET /dns/credentials": credentials(),
			"GET /cloudflare/ips": { body: ips },
			"POST /integrations/cloudflare/test": {
				body: { ...connected, zoneCount: 2 },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<CloudflareSection />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);
		expect(await screen.findByText("Cloudflare answered")).toBeInTheDocument();
		expect(
			screen.getByText("2 zones visible to the token."),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1);
	});

	test("Test connection says zone in the singular for one zone", async () => {
		stubApi({
			"GET /integrations/cloudflare": { body: connected },
			"GET /dns/credentials": credentials(),
			"GET /cloudflare/ips": { body: ips },
			"POST /integrations/cloudflare/test": {
				body: { ...connected, zoneCount: 1 },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<CloudflareSection />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);
		expect(
			await screen.findByText("1 zone visible to the token."),
		).toBeInTheDocument();
	});

	test("a failed connection test shows the reason in the form", async () => {
		stubApi({
			"GET /integrations/cloudflare": { body: connected },
			"GET /dns/credentials": credentials(),
			"GET /cloudflare/ips": { body: ips },
			"POST /integrations/cloudflare/test": problem(
				502,
				"Cloudflare is unreachable",
			),
		});
		const user = userEvent.setup();
		await renderWithProviders(<CloudflareSection />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);
		expect(
			await screen.findByText("Cloudflare is unreachable"),
		).toBeInTheDocument();
		expect(screen.queryByText("Cloudflare answered")).not.toBeInTheDocument();
	});

	test("shows a skeleton-free forbidden state for non-admins", async () => {
		stubApi({
			"GET /integrations/cloudflare": problem(403, "admins only"),
			"GET /dns/credentials": problem(403, "admins only"),
		});
		await renderWithProviders(<CloudflareSection />);
		expect(
			await screen.findByText("Only administrators can manage Cloudflare"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Ask an administrator to connect Cloudflare/),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /try again|retry/i }),
		).not.toBeInTheDocument();
	});

	test("a failed load offers a retry that recovers", async () => {
		let attempts = 0;
		stubApi({
			"GET /integrations/cloudflare": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json(unconfigured);
			},
			"GET /dns/credentials": credentials(),
		});
		const user = userEvent.setup();
		await renderWithProviders(<CloudflareSection />);
		expect(
			await screen.findByText("Cloudflare did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/The control plane did not answer/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByLabelText("API token")).toBeInTheDocument();
	});

	describe("IP ranges card", () => {
		test("summarises source, size and age for a fetched list", async () => {
			stubApi({
				"GET /integrations/cloudflare": { body: connected },
				"GET /dns/credentials": credentials(),
				"GET /cloudflare/ips": {
					body: {
						...ips,
						fetchedAt: new Date(Date.now() - 2 * 3_600_000).toISOString(),
						changedAt: new Date(Date.now() - 3 * 86_400_000).toISOString(),
					},
				},
			});
			await renderWithProviders(<CloudflareSection />);
			const card = await screen.findByRole("region", {
				name: "Cloudflare IP ranges",
			});
			expect(within(card).getByText("Cloudflare API")).toBeInTheDocument();
			expect(
				within(card).getByText("22 ranges (15 IPv4, 7 IPv6)"),
			).toBeInTheDocument();
			expect(within(card).getByText(/^Fetched /)).toBeInTheDocument();
			expect(
				within(card).getByText(/^Content last changed /),
			).toBeInTheDocument();
			expect(within(card).queryByRole("alert")).not.toBeInTheDocument();
		});

		test("the built-in list says it was never fetched", async () => {
			stubApi({
				"GET /integrations/cloudflare": { body: connected },
				"GET /dns/credentials": credentials(),
				"GET /cloudflare/ips": { body: { ...ips, source: "builtin" } },
			});
			await renderWithProviders(<CloudflareSection />);
			const card = await screen.findByRole("region", {
				name: "Cloudflare IP ranges",
			});
			expect(within(card).getByText("Built-in copy")).toBeInTheDocument();
			expect(
				within(card).getByText(
					"Shipped with this version (2025-12-01); not fetched yet",
				),
			).toBeInTheDocument();
			expect(within(card).queryByText("Changed")).not.toBeInTheDocument();
		});

		test("a failing fetch shows the last-refresh-failed warning", async () => {
			stubApi({
				"GET /integrations/cloudflare": { body: connected },
				"GET /dns/credentials": credentials(),
				"GET /cloudflare/ips": {
					body: { ...ips, lastError: "dial tcp: timeout" },
				},
			});
			await renderWithProviders(<CloudflareSection />);
			expect(
				await screen.findByText("The last refresh failed"),
			).toBeInTheDocument();
			expect(screen.getByText(/dial tcp: timeout/)).toBeInTheDocument();
			expect(
				screen.queryByText("The IP ranges could not be refreshed for a day"),
			).not.toBeInTheDocument();
		});

		test("a stale list shows the day-old warning", async () => {
			stubApi({
				"GET /integrations/cloudflare": { body: connected },
				"GET /dns/credentials": credentials(),
				"GET /cloudflare/ips": {
					body: { ...ips, lastError: "boom", stale: true },
				},
			});
			await renderWithProviders(<CloudflareSection />);
			expect(
				await screen.findByText(
					"The IP ranges could not be refreshed for a day",
				),
			).toBeInTheDocument();
		});

		test("Refresh now posts and reports the number of ranges", async () => {
			const api = stubApi({
				"GET /integrations/cloudflare": { body: connected },
				"GET /dns/credentials": credentials(),
				"GET /cloudflare/ips": { body: ips },
				"POST /cloudflare/ips/refresh": {
					body: { ...ips, v4Count: 20, v6Count: 10 },
				},
			});
			const user = userEvent.setup();
			await renderWithProviders(<CloudflareSection />);
			await user.click(
				await screen.findByRole("button", { name: "Refresh now" }),
			);
			expect(
				await screen.findByText("Cloudflare IP ranges refreshed"),
			).toBeInTheDocument();
			expect(screen.getByText("30 ranges in use.")).toBeInTheDocument();
			expect(
				api.calls.filter((c) => c.path === "/cloudflare/ips/refresh"),
			).toHaveLength(1);
		});

		test("a failed refresh raises an error toast", async () => {
			stubApi({
				"GET /integrations/cloudflare": { body: connected },
				"GET /dns/credentials": credentials(),
				"GET /cloudflare/ips": { body: ips },
				"POST /cloudflare/ips/refresh": problem(502, "feed unreachable"),
			});
			const user = userEvent.setup();
			await renderWithProviders(<CloudflareSection />);
			await user.click(
				await screen.findByRole("button", { name: "Refresh now" }),
			);
			expect(
				await screen.findByText("Could not refresh the IP ranges"),
			).toBeInTheDocument();
			expect(screen.getByText("feed unreachable")).toBeInTheDocument();
		});

		test("the card is absent when the list did not load", async () => {
			stubApi({
				"GET /integrations/cloudflare": { body: connected },
				"GET /dns/credentials": credentials(),
				"GET /cloudflare/ips": problem(500, "boom"),
			});
			await renderWithProviders(<CloudflareSection />);
			await screen.findByText("Connected");
			await waitFor(() =>
				expect(
					screen.queryByRole("region", { name: "Cloudflare IP ranges" }),
				).not.toBeInTheDocument(),
			);
			expect(
				screen.getByRole("button", { name: "Disconnect" }),
			).toBeInTheDocument();
		});
	});

	describe("disconnecting", () => {
		const baseRoutes = {
			"GET /dns/credentials": credentials(),
			"GET /cloudflare/ips": { body: ips },
		};

		test("asks first, then deletes while keeping the records", async () => {
			let connection: CloudflareConnectionView = connected;
			const api = stubApi({
				...baseRoutes,
				"GET /integrations/cloudflare": () => Response.json(connection),
				"DELETE /integrations/cloudflare": () => {
					connection = unconfigured;
					return new Response(null, { status: 204 });
				},
			});
			const user = userEvent.setup();
			await renderWithProviders(<CloudflareSection />);
			await user.click(
				await screen.findByRole("button", { name: "Disconnect" }),
			);

			const dialog = await screen.findByRole("alertdialog");
			expect(
				within(dialog).getByText("Disconnect Cloudflare?"),
			).toBeInTheDocument();
			expect(
				within(dialog).getByText(/Its records stay in Cloudflare/),
			).toBeInTheDocument();
			// Nothing is sent until the dialog is confirmed.
			expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
			await user.click(
				within(dialog).getByRole("button", { name: "Disconnect" }),
			);

			expect(
				await screen.findByText("Cloudflare disconnected"),
			).toBeInTheDocument();
			expect(
				screen.getByText("Existing records stay in Cloudflare, unmanaged."),
			).toBeInTheDocument();
			const del = api.calls.find((c) => c.method === "DELETE");
			expect(del?.path).toBe("/integrations/cloudflare");
			expect(del?.query.get("removeRecords")).toBe("false");
			expect(await screen.findByText("Not connected")).toBeInTheDocument();
		});

		test("cancelling sends nothing", async () => {
			const api = stubApi({
				...baseRoutes,
				"GET /integrations/cloudflare": { body: connected },
			});
			const user = userEvent.setup();
			await renderWithProviders(<CloudflareSection />);
			await user.click(
				await screen.findByRole("button", { name: "Disconnect" }),
			);
			const dialog = await screen.findByRole("alertdialog");
			await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
			await waitFor(() =>
				expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
			);
			expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		});

		test("with the record checkbox the records are removed too", async () => {
			const api = stubApi({
				...baseRoutes,
				"GET /integrations/cloudflare": { body: connected },
				"DELETE /integrations/cloudflare": { status: 204 },
			});
			const user = userEvent.setup();
			await renderWithProviders(<CloudflareSection />);
			await user.click(
				await screen.findByRole("checkbox", {
					name: "Also delete the DNS records NebulaCtrl created",
				}),
			);
			await user.click(screen.getByRole("button", { name: "Disconnect" }));
			const dialog = await screen.findByRole("alertdialog");
			expect(
				within(dialog).getByText(/deletes the DNS records it created/),
			).toBeInTheDocument();
			await user.click(
				within(dialog).getByRole("button", { name: "Disconnect" }),
			);

			expect(
				await screen.findByText("The records NebulaCtrl created were removed."),
			).toBeInTheDocument();
			expect(
				api.calls
					.find((c) => c.method === "DELETE")
					?.query.get("removeRecords"),
			).toBe("true");
		});

		test("a failed delete keeps the dialog open with the reason and a toast", async () => {
			stubApi({
				...baseRoutes,
				"GET /integrations/cloudflare": { body: connected },
				"DELETE /integrations/cloudflare": problem(500, "database is locked"),
			});
			const user = userEvent.setup();
			await renderWithProviders(<CloudflareSection />);
			await user.click(
				await screen.findByRole("button", { name: "Disconnect" }),
			);
			const dialog = await screen.findByRole("alertdialog");
			await user.click(
				within(dialog).getByRole("button", { name: "Disconnect" }),
			);

			expect(
				await screen.findByText("Could not disconnect Cloudflare"),
			).toBeInTheDocument();
			expect(
				within(dialog).getByText("database is locked"),
			).toBeInTheDocument();
			expect(
				screen.queryByText("Cloudflare disconnected"),
			).not.toBeInTheDocument();
		});
	});
});
