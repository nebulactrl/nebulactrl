import { describe, expect, it } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DnsStatus } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DnsPanel } from "./cloudflare-dns";
import { cloudflareStatus, dnsCheck } from "./sp-pages-fixtures";

const CF = "GET /status-pages/:id/cloudflare";
const DNS = "GET /status-pages/:id/dns";
const PUT = "PUT /status-pages/:id/cloudflare";
const FIX = "POST /status-pages/:id/cloudflare/fix";
const ME = (scopes: string[]) => ({
	"GET /me": {
		body: make("MeBody", {
			principal: { label: "robin", type: "user", scopes },
		}),
	},
});

function routes(status: DnsStatus, extra: Record<string, unknown> = {}) {
	return {
		[CF]: { body: status },
		[DNS]: { body: dnsCheck({ status: "missing" }) },
		...ME(["admin"]),
		...extra,
	} as Parameters<typeof stubApi>[0];
}

async function renderPanel() {
	return renderWithProviders(
		<DnsPanel pageId="page-1" hostname="status.acme.test" />,
	);
}

/** A hold-to-confirm button fills for 1.3 s; holding Space completes it. */
async function holdToConfirm(name: RegExp) {
	const button = await screen.findByRole("button", { name });
	fireEvent.keyDown(button, { key: " " });
	return button;
}

describe("DnsPanel mode", () => {
	it("shows a skeleton while Cloudflare is read", async () => {
		stubApi(routes(cloudflareStatus()));
		await renderPanel();
		expect(
			screen.getByRole("status", { name: "Reading Cloudflare" }),
		).toBeVisible();
		await screen.findByText("Cloudflare DNS");
	});

	it("shows the records to create by hand when no Cloudflare connection exists", async () => {
		const api = stubApi(
			routes(cloudflareStatus({ configured: false, status: "" })),
		);
		await renderPanel();
		expect(await screen.findByText("Create these records")).toBeInTheDocument();
		expect(screen.queryByText("Cloudflare DNS")).toBeNull();
		expect(api.calls.some((c) => c.path === "/status-pages/page-1/dns")).toBe(
			true,
		);
	});

	it("shows the manual panel for a hostname outside every connected zone", async () => {
		stubApi(routes(cloudflareStatus({ status: "not_in_zone" })));
		await renderPanel();
		expect(await screen.findByText("Create these records")).toBeInTheDocument();
		expect(screen.queryByText("Cloudflare DNS")).toBeNull();
	});

	it("falls back to the manual panel when Cloudflare refuses the read", async () => {
		stubApi({
			...routes(cloudflareStatus()),
			[CF]: problem(502, { code: "cloudflare_error", detail: "bad gateway" }),
		});
		await renderPanel();
		expect(await screen.findByText("Create these records")).toBeInTheDocument();
		expect(screen.queryByText("Cloudflare DNS")).toBeNull();
	});
});

describe("CloudflareDns status", () => {
	it("names the zone and what the state means", async () => {
		stubApi(routes(cloudflareStatus({ status: "missing" })));
		await renderPanel();
		expect(await screen.findByText("No record")).toBeInTheDocument();
		expect(screen.getByText("zone acme.test")).toBeInTheDocument();
		expect(
			screen.getByText("The hostname has no DNS record yet."),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/NebulaCtrl manages this record in your Cloudflare zone/,
			),
		).toBeInTheDocument();
	});

	it("prefers the server's message to the state's hint", async () => {
		stubApi(
			routes(
				cloudflareStatus({ status: "missing", message: "Zone is locked." }),
			),
		);
		await renderPanel();
		expect(await screen.findByText("Zone is locked.")).toBeInTheDocument();
		expect(
			screen.queryByText("The hostname has no DNS record yet."),
		).toBeNull();
	});

	it("shows a conflict as a warning without the managing sentence", async () => {
		stubApi(
			routes(
				cloudflareStatus({
					status: "conflict",
					message: "A record we did not create is in the way.",
					records: [
						{
							type: "A",
							content: "198.51.100.9",
							proxied: false,
							owned: false,
						},
					],
				} as Partial<DnsStatus>),
			),
		);
		await renderPanel();
		expect(
			await screen.findByText("A record we did not create is in the way."),
		).toBeInTheDocument();
		expect(screen.getByText("Conflicting record")).toBeInTheDocument();
		expect(screen.queryByText(/NebulaCtrl manages this record/)).toBeNull();
	});

	it("lists the zone's records and the wanted ones with who owns them", async () => {
		stubApi(
			routes(
				cloudflareStatus({
					status: "wrong_target",
					records: [
						{ type: "A", content: "198.51.100.9", proxied: true, owned: false },
						{
							type: "AAAA",
							content: "2001:db8::2",
							proxied: false,
							owned: true,
						},
					],
				} as Partial<DnsStatus>),
			),
		);
		await renderPanel();
		await screen.findByText("Wrong target");
		const foreign = screen.getByText("198.51.100.9").closest("li");
		expect(foreign).not.toBeNull();
		expect(
			within(foreign as HTMLElement).getByText("not NebulaCtrl"),
		).toBeVisible();
		expect(within(foreign as HTMLElement).getByText("Proxied")).toBeVisible();
		expect(
			within(foreign as HTMLElement).queryByText("managed by NebulaCtrl"),
		).toBeNull();
		const owned = screen.getByText("2001:db8::2").closest("li");
		expect(within(owned as HTMLElement).getByText("in zone")).toBeVisible();
		expect(
			within(owned as HTMLElement).getByText("managed by NebulaCtrl"),
		).toBeVisible();
		const wanted = screen.getByText("203.0.113.10", { selector: "li > span" });
		expect(
			within(wanted.closest("li") as HTMLElement).getByText("wanted"),
		).toBeVisible();
	});

	it("does not repeat the wanted records once the state is managed", async () => {
		stubApi(
			routes(
				cloudflareStatus({
					status: "managed",
					records: [
						{ type: "A", content: "203.0.113.10", proxied: false, owned: true },
					],
				} as Partial<DnsStatus>),
			),
		);
		await renderPanel();
		await screen.findByText("Managed");
		expect(
			screen.getAllByText("203.0.113.10", { selector: "li > span" }),
		).toHaveLength(1);
		expect(screen.queryByText("wanted")).toBeNull();
	});

	it("shows warnings and the last failed sync", async () => {
		stubApi(
			routes(
				cloudflareStatus({
					status: "managed",
					warnings: ["SSL mode is flexible."],
					error: "rate limited",
				}),
			),
		);
		await renderPanel();
		expect(
			await screen.findByText("SSL mode is flexible."),
		).toBeInTheDocument();
		expect(
			screen.getByText(/The last sync failed: rate limited\./),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Retry sync" }),
		).toBeInTheDocument();
	});

	it("offers no fix for a managed, healthy record", async () => {
		stubApi(routes(cloudflareStatus({ status: "managed" })));
		await renderPanel();
		await screen.findByText("Managed");
		expect(screen.queryByRole("button", { name: "Retry sync" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Create records" })).toBeNull();
	});

	it("reads the zone again from the refresh button", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes(cloudflareStatus()));
		await renderPanel();
		await screen.findByText("Cloudflare DNS");
		const before = api.calls.filter((c) =>
			c.path.endsWith("/cloudflare"),
		).length;
		await user.click(
			screen.getByRole("button", { name: "Read the zone again" }),
		);
		await waitFor(() =>
			expect(
				api.calls.filter((c) => c.path.endsWith("/cloudflare")).length,
			).toBe(before + 1),
		);
	});

	it("checks the hostname's DNS inside the panel without a manual record list", async () => {
		stubApi(routes(cloudflareStatus()));
		await renderPanel();
		expect(
			await screen.findByText(/The Cloudflare section above creates it/),
		).toBeInTheDocument();
		expect(screen.queryByText("Create these records")).toBeNull();
	});
});

describe("CloudflareDns proxy and lock", () => {
	it("switches the record to proxied and sends only the proxy setting", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(cloudflareStatus(), {
				[PUT]: { body: cloudflareStatus({ proxied: true }) },
			}),
		);
		await renderPanel();
		const proxied = await screen.findByRole("button", { name: "Proxied" });
		expect(screen.getByRole("button", { name: "DNS only" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		await user.click(proxied);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
				proxied: true,
			}),
		);
		expect(api.calls.find((c) => c.method === "PUT")?.path).toBe(
			"/status-pages/page-1/cloudflare",
		);
		expect(api.unmatched).toEqual([]);
	});

	it("toasts when the change was saved but the sync failed", async () => {
		const user = userEvent.setup();
		stubApi(
			routes(cloudflareStatus(), {
				[PUT]: { body: cloudflareStatus({ error: "zone is read-only" }) },
			}),
		);
		await renderPanel();
		await user.click(await screen.findByRole("button", { name: "Proxied" }));
		expect(
			await screen.findByText("Saved, but the DNS sync failed"),
		).toBeInTheDocument();
		expect(screen.getByText("zone is read-only")).toBeInTheDocument();
	});

	it("shows the server's reason when the change is refused", async () => {
		const user = userEvent.setup();
		stubApi(
			routes(cloudflareStatus(), {
				[PUT]: problem(403, {
					code: "forbidden",
					detail: "Admin scope required.",
				}),
			}),
		);
		await renderPanel();
		await user.click(await screen.findByRole("button", { name: "Proxied" }));
		expect(
			await screen.findByText("Admin scope required."),
		).toBeInTheDocument();
	});

	it("turns the origin lock on at once", async () => {
		const user = userEvent.setup();
		const locked = cloudflareStatus();
		locked.originLock.optIn = false;
		locked.originLock.reason = "opted_out";
		const api = stubApi(
			routes(locked, { [PUT]: { body: cloudflareStatus() } }),
		);
		await renderPanel();
		const toggle = await screen.findByRole("switch", {
			name: "Lock the origin to Cloudflare",
		});
		expect(toggle).not.toBeChecked();
		expect(
			screen.getByText("You turned the lock off for this page."),
		).toBeVisible();
		await user.click(toggle);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
				originLock: true,
			}),
		);
	});

	it("turns the lock off without asking when the record is not proxied", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(cloudflareStatus(), { [PUT]: { body: cloudflareStatus() } }),
		);
		await renderPanel();
		await user.click(
			await screen.findByRole("switch", {
				name: "Lock the origin to Cloudflare",
			}),
		);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
				originLock: false,
			}),
		);
		expect(screen.queryByRole("alertdialog")).toBeNull();
	});

	it("asks before opening a proxied page to the internet, and keeps it locked on refusal", async () => {
		const user = userEvent.setup();
		const proxied = cloudflareStatus({ proxied: true, status: "managed" });
		proxied.originLock.enabled = true;
		proxied.originLock.aop = "on";
		proxied.originLock.reason = "";
		const api = stubApi(routes(proxied, { [PUT]: { body: proxied } }));
		await renderPanel();
		expect(await screen.findByText("Locked")).toBeInTheDocument();
		expect(
			screen.getByText("Authenticated Origin Pulls: on"),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("switch", { name: "Lock the origin to Cloudflare" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Open status.acme.test to the internet?"),
		).toBeVisible();
		await user.click(
			within(dialog).getByRole("button", { name: "Keep it locked" }),
		);
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	it("turns the lock off after the confirmation is held", async () => {
		const user = userEvent.setup();
		const proxied = cloudflareStatus({ proxied: true, status: "managed" });
		proxied.originLock.enabled = true;
		proxied.originLock.aop = "on";
		const api = stubApi(routes(proxied, { [PUT]: { body: proxied } }));
		await renderPanel();
		await user.click(
			await screen.findByRole("switch", {
				name: "Lock the origin to Cloudflare",
			}),
		);
		await holdToConfirm(/turn the lock off/i);
		await waitFor(
			() =>
				expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
					originLock: false,
				}),
			{ timeout: 4000 },
		);
	});

	it("warns that an unlocked proxied origin can be bypassed", async () => {
		const open = cloudflareStatus({ proxied: true, status: "managed" });
		open.originLock.optIn = false;
		open.originLock.reason = "opted_out";
		stubApi(routes(open));
		await renderPanel();
		await screen.findByText("Open");
		expect(
			screen.getAllByText(
				"Anyone who finds this server's IP can bypass Cloudflare.",
			),
		).not.toHaveLength(0);
	});

	it("points to the token permission when Authenticated Origin Pulls is unavailable", async () => {
		const status = cloudflareStatus({ proxied: true, status: "managed" });
		status.originLock.enabled = true;
		status.originLock.aop = "unavailable";
		status.originLock.aopReasonCode = "permission";
		stubApi(routes(status));
		await renderPanel();
		await screen.findByText("Locked");
		expect(
			screen.getByText("Authenticated Origin Pulls: unavailable"),
		).toBeInTheDocument();
		expect(screen.getByText("Edit the Cloudflare token")).toBeInTheDocument();
	});

	it("explains why a page is not locked yet", async () => {
		const status = cloudflareStatus({ status: "managed" });
		status.originLock.reason = "not_published";
		stubApi(routes(status));
		await renderPanel();
		expect(await screen.findByText("Pending")).toBeInTheDocument();
		expect(
			screen.getByText(
				"The page is not published yet. The lock applies once it is.",
			),
		).toBeInTheDocument();
	});
});

describe("CloudflareDns fix", () => {
	it("creates the records when they are missing and says so", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(cloudflareStatus({ status: "missing" }), {
				[FIX]: { body: cloudflareStatus({ status: "managed" }) },
			}),
		);
		await renderPanel();
		await user.click(
			await screen.findByRole("button", { name: "Create records" }),
		);
		expect(await screen.findByText("DNS fixed")).toBeInTheDocument();
		expect(
			screen.getByText("status.acme.test now points at the server."),
		).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/status-pages/page-1/cloudflare/fix");
		expect(post?.body).toEqual({ adopt: false });
	});

	it("fixes a wrong target with Fix DNS", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(cloudflareStatus({ status: "wrong_target" }), {
				[FIX]: { body: cloudflareStatus({ status: "managed" }) },
			}),
		);
		await renderPanel();
		await user.click(await screen.findByRole("button", { name: "Fix DNS" }));
		await screen.findByText("DNS fixed");
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			adopt: false,
		});
	});

	it("retries a failed sync of a managed record without adopting", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(cloudflareStatus({ status: "managed", error: "timeout" }), {
				[FIX]: { body: cloudflareStatus({ status: "managed" }) },
			}),
		);
		await renderPanel();
		await user.click(await screen.findByRole("button", { name: "Retry sync" }));
		await screen.findByText("DNS fixed");
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			adopt: false,
		});
	});

	it("explains a refused fix in words and keeps the button", async () => {
		const user = userEvent.setup();
		stubApi(
			routes(cloudflareStatus({ status: "missing" }), {
				[FIX]: problem(409, {
					code: "adoption_required",
					detail: "raw detail",
				}),
			}),
		);
		await renderPanel();
		await user.click(
			await screen.findByRole("button", { name: "Create records" }),
		);
		expect(
			await screen.findByText(
				/A record NebulaCtrl did not create is in the way/,
			),
		).toBeInTheDocument();
		expect(screen.queryByText("raw detail")).toBeNull();
		expect(
			screen.getByRole("button", { name: "Create records" }),
		).toBeEnabled();
	});

	it("shows the server's own message for a fix failing for another reason", async () => {
		const user = userEvent.setup();
		stubApi(
			routes(cloudflareStatus({ status: "missing" }), {
				[FIX]: problem(502, {
					code: "cloudflare_down",
					detail: "Cloudflare is down.",
				}),
			}),
		);
		await renderPanel();
		await user.click(
			await screen.findByRole("button", { name: "Create records" }),
		);
		expect(await screen.findByText("Cloudflare is down.")).toBeInTheDocument();
	});

	it("lets an admin adopt a record the team created after confirming", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(cloudflareStatus({ status: "present" }), {
				[FIX]: { body: cloudflareStatus({ status: "managed" }) },
			}),
		);
		await renderPanel();
		await user.click(
			await screen.findByRole("button", { name: "Adopt record" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Adopt the record for status.acme.test?"),
		).toBeVisible();
		expect(
			within(dialog).getByText(/already points at 203\.0\.113\.10/),
		).toBeVisible();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Adopt record" }),
		);
		expect(await screen.findByText("DNS fixed")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			adopt: true,
		});
	});

	it("hides adopt from someone without the admin scope and says an admin can do it", async () => {
		stubApi({
			...routes(cloudflareStatus({ status: "present" })),
			...ME(["read"]),
		});
		await renderPanel();
		await screen.findByText("Record present");
		await waitFor(() =>
			expect(screen.getByText(/An admin can do this\./)).toBeInTheDocument(),
		);
		expect(screen.queryByRole("button", { name: "Adopt record" })).toBeNull();
	});

	it("still offers the plain fix to a non-admin", async () => {
		stubApi({
			...routes(cloudflareStatus({ status: "missing" })),
			...ME(["read"]),
		});
		await renderPanel();
		expect(
			await screen.findByRole("button", { name: "Create records" }),
		).toBeVisible();
	});

	it("replaces a conflicting record only after the confirmation is held", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(
				cloudflareStatus({
					status: "conflict",
					message: "A record is in the way.",
					records: [
						{
							type: "A",
							content: "198.51.100.9",
							proxied: false,
							owned: false,
						},
					],
				}),
				{ [FIX]: { body: cloudflareStatus({ status: "managed" }) } },
			),
		);
		await renderPanel();
		await user.click(
			await screen.findByRole("button", {
				name: "Replace with NebulaCtrl's record",
			}),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Replace the record for status.acme.test?"),
		).toBeVisible();
		expect(
			within(dialog).getByText(/replaces or deletes: A 198\.51\.100\.9/),
		).toBeVisible();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		await holdToConfirm(/replace with nebulactrl/i);
		await waitFor(
			() =>
				expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
					adopt: true,
				}),
			{ timeout: 4000 },
		);
		expect(
			screen.getByText(/NebulaCtrl leaves these records as they are/),
		).toBeVisible();
	});
});
