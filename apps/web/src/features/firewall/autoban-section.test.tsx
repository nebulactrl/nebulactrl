import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	AutoBan,
	AutoBanView,
	BanView,
	FirewallView,
} from "#/api/gen/types.gen";
import { makeService } from "#/features/canvas/lib/fixtures";
import { type ApiReply, stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { AutoBanSection } from "./autoban-section";

const OFF: AutoBan = {
	enabled: false,
	threshold: 10,
	windowSeconds: 600,
	durationSeconds: 3600,
	allow: [],
};

const view = (over: Partial<AutoBanView> = {}): AutoBanView => ({
	activeBans: 0,
	analytics: true,
	hosts: ["shop.example.com"],
	policy: OFF,
	server: "hetzner-01",
	supported: true,
	warnings: [],
	...over,
});

const firewall: FirewallView = {
	rules: [{ id: "r1", name: "No admin", action: "block", conditions: [] }],
	standard: [
		{
			rule: {
				id: "stdsecrets",
				name: "Secret files",
				action: "block",
				conditions: [],
			},
			description: "Requests for environment files.",
			examples: ["/.env"],
		},
	],
	enforcement: "routes",
	hosts: ["shop.example.com"],
	deployed: true,
	server: "hetzner-01",
	geoAvailable: false,
	supported: true,
};

const minutes = (n: number) => new Date(Date.now() + n * 60_000).toISOString();

const active: BanView = {
	id: "b1",
	prefix: "198.51.100.7/32",
	address: "198.51.100.7",
	startedAt: minutes(-20),
	expiresAt: minutes(40),
	endedAt: null,
	strikes: 12,
	rules: { stdsecrets: 9, r1: 3 },
	source: "auto",
};

const ended: BanView = {
	id: "b0",
	prefix: "203.0.113.9/32",
	address: null,
	startedAt: minutes(-300),
	expiresAt: minutes(-240),
	endedAt: minutes(-240),
	endReason: "expired",
	strikes: 10,
	rules: { stdsecrets: 10 },
	source: "auto",
};

interface Mount {
	view?: AutoBanView;
	put?: ApiReply;
	activeBans?: BanView[];
	endedBans?: BanView[];
	unban?: ApiReply;
}

async function mount(opts: Mount = {}) {
	const current = opts.view ?? view();
	const api = stubApi({
		"GET /services/s1/autoban": { body: current },
		"GET /services/s1/firewall": { body: firewall },
		"GET /services/s1/bans": (request: Request) => {
			const state = new URL(request.url).searchParams.get("state");
			const items = state === "ended" ? opts.endedBans : opts.activeBans;
			return Response.json({ items: items ?? [] });
		},
		"PUT /services/s1/autoban":
			opts.put ??
			((request: Request) =>
				request.json().then((policy: AutoBan) =>
					Response.json({
						...current,
						policy,
					}),
				)),
		"DELETE /services/s1/bans/b1": opts.unban ?? {
			status: 202,
			body: { status: "queued" },
		},
	});
	await renderWithProviders(
		<AutoBanSection service={makeService({ id: "s1", name: "shop" })} />,
		{ path: "/p/p1/prod", routePath: "/p/$projectId/$envSlug" },
	);
	await screen.findByRole("switch", { name: "Auto-ban" });
	return api;
}

describe("AutoBanSection", () => {
	test("off by default: defaults shown, what counts and the enforcement note, no save bar", async () => {
		await mount();
		expect(screen.getByRole("switch", { name: "Auto-ban" })).not.toBeChecked();
		expect(screen.getByLabelText("Blocked requests to ban")).toHaveValue("10");
		expect(screen.getByLabelText("Counting window")).toHaveValue("10");
		expect(
			screen.getByRole("combobox", { name: "Counting window unit" }),
		).toHaveTextContent("minutes");
		expect(screen.getByLabelText("Ban length")).toHaveValue("1");
		expect(
			screen.getByRole("combobox", { name: "Ban length unit" }),
		).toHaveTextContent("hours");
		expect(
			screen.getByText(/path, method, header, query or user-agent condition/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/only look at the address or the country never count/),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/Checked by the edge guard next to the proxy: adds a local hop to every request of this service/,
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Save auto-ban" }),
		).not.toBeInTheDocument();
	});

	test("enabling and saving sends the policy as one PUT", async () => {
		const user = userEvent.setup();
		const api = await mount();
		await user.click(screen.getByRole("switch", { name: "Auto-ban" }));
		await user.clear(screen.getByLabelText("Blocked requests to ban"));
		await user.type(screen.getByLabelText("Blocked requests to ban"), "5");
		await user.clear(screen.getByLabelText("Counting window"));
		await user.type(screen.getByLabelText("Counting window"), "2");
		await user.click(screen.getByRole("combobox", { name: "Ban length unit" }));
		await user.click(await screen.findByRole("option", { name: "days" }));
		await user.click(screen.getByRole("button", { name: "Add an address" }));
		await user.type(screen.getByLabelText("Address 1"), "203.0.113.0/24");
		expect(api.calls.filter((c) => c.method === "PUT")).toHaveLength(0);

		await user.click(screen.getByRole("button", { name: "Save auto-ban" }));
		expect(await screen.findByText("Auto-ban saved")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			enabled: true,
			threshold: 5,
			windowSeconds: 120,
			durationSeconds: 86400,
			allow: ["203.0.113.0/24"],
		});
		// Saved: the bar is gone and the card follows the server.
		expect(
			screen.queryByRole("button", { name: "Save auto-ban" }),
		).not.toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Auto-ban" })).toBeChecked();
	});

	test("Discard drops the edits", async () => {
		const user = userEvent.setup();
		const api = await mount();
		await user.click(screen.getByRole("switch", { name: "Auto-ban" }));
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(screen.getByRole("switch", { name: "Auto-ban" })).not.toBeChecked();
		expect(api.calls.filter((c) => c.method === "PUT")).toHaveLength(0);
	});

	test("out-of-range values and bad allow entries are flagged and block Save", async () => {
		const user = userEvent.setup();
		const api = await mount();
		await user.click(screen.getByRole("switch", { name: "Auto-ban" }));
		await user.clear(screen.getByLabelText("Blocked requests to ban"));
		await user.type(screen.getByLabelText("Blocked requests to ban"), "2");
		await user.clear(screen.getByLabelText("Ban length"));
		await user.type(screen.getByLabelText("Ban length"), "1");
		await user.click(screen.getByRole("combobox", { name: "Ban length unit" }));
		await user.click(await screen.findByRole("option", { name: "minutes" }));
		await user.click(screen.getByRole("button", { name: "Add an address" }));
		await user.type(screen.getByLabelText("Address 1"), "nope");

		expect(
			screen.getByText(
				/Blocked requests to ban is a whole number from 3 to 1000/,
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Ban length is 5 minutes to 7 days/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/“nope” is not a CIDR or IP address/),
		).toBeInTheDocument();
		expect(
			screen.getByText("Fix the highlighted settings."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Save auto-ban" }),
		).toBeDisabled();
		expect(api.calls.filter((c) => c.method === "PUT")).toHaveLength(0);
	});

	test("a 422 from the server shows its meaning on the switch", async () => {
		const user = userEvent.setup();
		await mount({
			put: {
				status: 422,
				body: {
					status: 422,
					title: "Unprocessable Entity",
					detail: "traffic analytics is off",
					code: "analytics_required",
				},
			},
		});
		await user.click(screen.getByRole("switch", { name: "Auto-ban" }));
		await user.click(screen.getByRole("button", { name: "Save auto-ban" }));
		expect(
			(await screen.findAllByText(/traffic analytics must be on/)).length,
		).toBeGreaterThan(0);
		// The edits stay for another try.
		expect(screen.getByRole("switch", { name: "Auto-ban" })).toBeChecked();
	});

	test("analytics off disables the switch and links to the server's monitoring settings", async () => {
		await mount({ view: view({ analytics: false }) });
		expect(screen.getByRole("switch", { name: "Auto-ban" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		expect(screen.getByText("Traffic analytics is off")).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: /Turn it on in the server's settings/ }),
		).toHaveAttribute("href", expect.stringContaining("/settings/monitoring"));
	});

	test("analytics off keeps an enabled policy switchable off", async () => {
		await mount({
			view: view({ analytics: false, policy: { ...OFF, enabled: true } }),
		});
		expect(
			screen.getByRole("switch", { name: "Auto-ban" }),
		).not.toHaveAttribute("aria-disabled", "true");
	});

	test("an agent that is too old disables the switch", async () => {
		await mount({ view: view({ supported: false }) });
		expect(screen.getByRole("switch", { name: "Auto-ban" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		expect(screen.getByText(/is too old/)).toBeInTheDocument();
	});

	test("a Cloudflare-proxied domain that is not locked is named", async () => {
		await mount({
			view: view({
				hosts: ["shop.example.com", "www.example.com"],
				warnings: ["cloudflare_proxied_not_locked"],
			}),
		});
		expect(
			screen.getByText("Some visitors cannot be banned"),
		).toBeInTheDocument();
		expect(
			screen.getByText("shop.example.com and www.example.com"),
		).toBeInTheDocument();
		expect(screen.getByText(/they are never banned/)).toBeInTheDocument();
	});

	test("without bans it says so, twice", async () => {
		await mount();
		expect(
			await screen.findByText("No address is banned right now."),
		).toBeInTheDocument();
		expect(
			await screen.findByText("No ban has ended yet."),
		).toBeInTheDocument();
	});

	test("active bans list the address, expiry, strikes and rule names; ended ones only the prefix", async () => {
		await mount({
			view: view({ activeBans: 1 }),
			activeBans: [active],
			endedBans: [ended],
		});
		const row = (await screen.findByText("198.51.100.7")).closest("tr");
		expect(row).not.toBeNull();
		const cells = within(row as HTMLElement);
		expect(cells.getByText(/^in (39|40)m$/)).toBeInTheDocument();
		expect(cells.getByText(/^(19|20)m ago$/)).toBeInTheDocument();
		expect(cells.getByText("12")).toBeInTheDocument();
		// The most counted rule first, ids resolved to names.
		expect(cells.getByText("Secret files, No admin")).toBeInTheDocument();

		const past = (await screen.findByText("203.0.113.9/32")).closest("tr");
		const pastCells = within(past as HTMLElement);
		expect(pastCells.getByText("Expired")).toBeInTheDocument();
		expect(screen.queryByText(/203\.0\.113\.9$/)).not.toBeInTheDocument();
	});

	test("Unban asks first, then sends the DELETE and shows the ban as unbanning", async () => {
		const user = userEvent.setup();
		const api = await mount({
			view: view({ activeBans: 1 }),
			activeBans: [active],
		});
		await user.click(
			await screen.findByRole("button", { name: "Unban 198.51.100.7" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Unban 198.51.100.7?")).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "DELETE")).toHaveLength(0);

		await user.click(within(dialog).getByRole("button", { name: "Unban" }));
		expect(await screen.findByText("Unban sent")).toBeInTheDocument();
		expect(
			api.calls.some(
				(c) => c.method === "DELETE" && c.path === "/services/s1/bans/b1",
			),
		).toBe(true);
		// The agent has not confirmed yet: the row stays, marked, until the list drops it.
		expect(
			await screen.findByRole("button", { name: "Unban 198.51.100.7" }),
		).toBeDisabled();
		expect(screen.getByText("Unbanning")).toBeInTheDocument();
	});

	test("a ban that already ended (409) keeps the dialog open with the reason", async () => {
		const user = userEvent.setup();
		await mount({
			view: view({ activeBans: 1 }),
			activeBans: [active],
			unban: {
				status: 409,
				body: {
					status: 409,
					title: "Conflict",
					detail: "the ban is not active",
					code: "ban_not_active",
				},
			},
		});
		await user.click(
			await screen.findByRole("button", { name: "Unban 198.51.100.7" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Unban" }));
		expect(
			await within(dialog).findByText(/the ban is not active/),
		).toBeInTheDocument();
	});
});
