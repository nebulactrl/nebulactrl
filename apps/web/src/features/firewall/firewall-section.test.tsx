import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { FirewallBlocks, FirewallView } from "#/api/gen/types.gen";
import { makeService } from "#/features/canvas/lib/fixtures";
import { type ApiReply, stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { FirewallSection } from "./firewall-section";

const STANDARD: FirewallView["standard"] = [
	{
		rule: {
			id: "stdsecrets",
			name: "Secret files",
			action: "block",
			conditions: [
				{ field: "path", op: "regex", values: ["(?i)/\\.(env|git)(/|\\.|$)"] },
			],
		},
		description: "Requests for environment files and version control folders.",
		examples: ["/.env", "/.git/config"],
	},
];

const view = (over: Partial<FirewallView> = {}): FirewallView => ({
	rules: [],
	standard: STANDARD,
	enforcement: "routes",
	hosts: ["shop.example.com"],
	deployed: true,
	server: "hetzner-01",
	geoAvailable: false,
	supported: true,
	...over,
});

const PATH = "/services/s1/firewall";

const BLOCKS_PATH = `${PATH}/blocks`;

const blocksOf = (over: Partial<FirewallBlocks> = {}): FirewallBlocks => ({
	range: "24h",
	from: "2026-11-22T12:00:00Z",
	to: "2026-11-23T12:00:00Z",
	trafficAnalytics: true,
	total: 0,
	byRule: [],
	logsQuery: "stream:requests @blocked_by:*",
	...over,
});

async function mount(
	initial: FirewallView,
	put?: (body: unknown) => FirewallView,
	blocks: ApiReply = { body: blocksOf() },
) {
	const api = stubApi({
		[`GET ${PATH}`]: { body: initial },
		[`GET ${BLOCKS_PATH}`]: blocks,
		"GET /services/s1/autoban": {
			body: {
				activeBans: 0,
				analytics: true,
				hosts: ["shop.example.com"],
				policy: {
					enabled: false,
					threshold: 10,
					windowSeconds: 600,
					durationSeconds: 3600,
					allow: [],
				},
				server: "hetzner-01",
				supported: true,
				warnings: [],
			},
		},
		"GET /services/s1/bans": { body: { items: [] } },
		[`PUT ${PATH}`]: (request: Request) =>
			request.json().then((body) => Response.json(put ? put(body) : initial)),
	});
	await renderWithProviders(
		<FirewallSection service={makeService({ id: "s1", name: "shop" })} />,
		{ path: "/p/p1/prod", routePath: "/p/$projectId/$envSlug" },
	);
	await screen.findByText(/Covers/);
	await screen.findByRole("switch", { name: "Auto-ban" });
	await screen.findByText("No ban has ended yet.");
	return api;
}

const plainRule = (i: number): NonNullable<FirewallView["rules"]>[number] => ({
	id: `r${i}`,
	name: `Rule ${i}`,
	action: "block",
	conditions: [{ field: "path", op: "prefix", values: [`/p${i}`] }],
});

describe("FirewallSection", () => {
	test("without rules it offers recipes; a recipe opens as a rule that applies with one PUT", async () => {
		const user = userEvent.setup();
		const api = await mount(view(), (body) =>
			view({ rules: (body as FirewallView).rules, enforcement: "routes" }),
		);
		// Without rules of its own the service still has the standard protection.
		expect(
			screen.getByText(/only the standard protection below applies/),
		).toBeInTheDocument();
		expect(screen.getByText("Secret files")).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: /Block WordPress probes/ }),
		);

		// The rule is open, reads as a sentence, and nothing is sent before Apply.
		expect(screen.getByLabelText("Name")).toHaveValue("Block WordPress probes");
		expect(
			screen.getByText(/Block requests when the path contains wp-login\.php/),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "PUT")).toHaveLength(0);

		await user.click(screen.getByRole("button", { name: "Apply firewall" }));
		expect(await screen.findByText("Firewall applied")).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.body).toMatchObject({
			rules: [
				{
					name: "Block WordPress probes",
					action: "block",
					conditions: [{ field: "path", op: "contains" }],
				},
			],
		});
	});

	test("the empty state groups recipes under headings and says who should not use them", async () => {
		await mount(view());
		for (const name of ["Probes and exploits", "Bots and crawlers", "Access"]) {
			expect(screen.getByRole("heading", { name })).toBeInTheDocument();
		}
		expect(
			screen.getByText("Not for apps written in PHP."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /Block empty user agents/ }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /Block TRACE and CONNECT/ }),
		).toBeInTheDocument();
	});

	test("the recipe menu is grouped and a recipe of several rules adds them all", async () => {
		const user = userEvent.setup();
		const api = await mount(view({ rules: [plainRule(1)] }), (body) =>
			view({ rules: (body as FirewallView).rules }),
		);
		await user.click(screen.getByRole("button", { name: "Add from a recipe" }));
		for (const heading of [
			"Probes and exploits",
			"Bots and crawlers",
			"Access",
		]) {
			expect(await screen.findByText(heading)).toBeInTheDocument();
		}
		expect(
			screen.getByText("Not for apps written in PHP."),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("menuitem", { name: /Block attack signatures/ }),
		);
		expect(screen.getByText("8 rules + standard")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Apply firewall" }));
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "PUT")).toBe(true),
		);
		const sent = api.calls.find((c) => c.method === "PUT")
			?.body as FirewallView;
		expect(sent.rules).toHaveLength(8);
		expect(sent.rules?.[1]?.name).toBe("Block Log4Shell in user agents");
	});

	test("a recipe whose rules would pass the limit is disabled in the menu", async () => {
		const user = userEvent.setup();
		await mount(
			view({ rules: Array.from({ length: 45 }, (_, i) => plainRule(i + 1)) }),
		);
		await user.click(screen.getByRole("button", { name: "Add from a recipe" }));
		expect(
			await screen.findByRole("menuitem", { name: /Block attack signatures/ }),
		).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByRole("menuitem", { name: /Block countries/ }),
		).not.toHaveAttribute("aria-disabled", "true");
	});

	test("Try a request reaches the standard protection after the service's own rules", async () => {
		await mount(
			view({
				rules: [
					{
						id: "dotenv",
						name: "Our public .env.example",
						action: "allow",
						conditions: [
							{ field: "path", op: "exact", values: ["/.env.example"] },
						],
					},
				],
			}),
		);
		const path = screen.getByLabelText("Path");
		const verdict = () => screen.getByRole("status");
		fireEvent.change(path, { target: { value: "/.ENV.production" } });
		expect(verdict()).toHaveTextContent(
			"Blocked by the standard protection, Secret files.",
		);
		// An allow rule of the service runs first and exempts the path.
		fireEvent.change(path, { target: { value: "/.env.example" } });
		expect(verdict()).toHaveTextContent(
			"Allowed by rule 1, Our public .env.example.",
		);
		fireEvent.change(path, { target: { value: "/.github/workflows" } });
		expect(verdict()).toHaveTextContent("Reaches the service.");
	});

	test("a rule missing its values cannot be applied, and says what to fix", async () => {
		const user = userEvent.setup();
		await mount(view({ geoAvailable: false }));
		await user.click(screen.getByRole("button", { name: /Block countries/ }));
		expect(screen.getByText("Add at least one value.")).toBeInTheDocument();
		expect(screen.getByText("Needs fix")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Apply firewall" }),
		).toBeDisabled();
		// Country rules on a server without geolocation are called out before Apply.
		expect(
			screen.getByText("Country rules need geolocation"),
		).toBeInTheDocument();

		const values = screen.getByLabelText("Condition 1: values");
		fireEvent.change(values, { target: { value: "Russia" } });
		fireEvent.keyDown(values, { key: "Enter" });
		expect(screen.getByText("RU")).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.queryByText("Add at least one value.")).toBeNull(),
		);
		expect(
			screen.getByRole("button", { name: "Apply firewall" }),
		).toBeEnabled();
	});

	test("Try a request answers with the rule that decides, before anything is applied", async () => {
		await mount(
			view({
				enforcement: "routes",
				rules: [
					{
						id: "office",
						name: "Office",
						action: "allow",
						conditions: [{ field: "ip", op: "in", values: ["203.0.113.0/24"] }],
					},
					{
						id: "admin",
						name: "Admin only",
						action: "block",
						conditions: [{ field: "path", op: "prefix", values: ["/admin"] }],
					},
				],
			}),
		);
		const verdict = () => screen.getByRole("status");
		expect(verdict()).toHaveTextContent(
			"Reaches the service. No rule matches it.",
		);
		fireEvent.change(screen.getByLabelText("Path"), {
			target: { value: "/admin/users" },
		});
		expect(verdict()).toHaveTextContent("Blocked by rule 2, Admin only.");
		fireEvent.change(screen.getByLabelText("IP address"), {
			target: { value: "203.0.113.20" },
		});
		expect(verdict()).toHaveTextContent("Allowed by rule 1, Office.");

		// Turning the allow rule off is reflected at once, unsaved.
		fireEvent.click(screen.getByRole("switch", { name: "Rule 1, Office: on" }));
		expect(verdict()).toHaveTextContent("Blocked by rule 2, Admin only.");
		expect(
			screen.getByText(/Unsaved changes, applied without a deploy/),
		).toBeInTheDocument();
	});

	test("rules move up and down, and the new order is what Apply sends", async () => {
		const user = userEvent.setup();
		const api = await mount(
			view({
				rules: [
					{
						id: "a",
						name: "First",
						action: "block",
						conditions: [{ field: "path", op: "prefix", values: ["/a"] }],
					},
					{
						id: "b",
						name: "Second",
						action: "block",
						conditions: [{ field: "path", op: "prefix", values: ["/b"] }],
					},
				],
			}),
		);
		await user.click(screen.getByRole("button", { name: "Move rule 2 up" }));
		await user.click(screen.getByRole("button", { name: "Apply firewall" }));
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "PUT")).toBe(true),
		);
		const put = api.calls.find((c) => c.method === "PUT");
		const sent = (put?.body ?? { rules: [] }) as FirewallView;
		expect(sent.rules?.map((r) => r.id)).toEqual(["b", "a"]);
	});

	describe("blocked requests", () => {
		const ownRules = [plainRule(1), plainRule(2)];
		const busy = blocksOf({
			total: 17,
			byRule: [
				{
					id: "stdsecrets",
					name: "Secret files",
					kind: "standard",
					requests: 12,
				},
				{
					id: "r1",
					name: "Rule 1",
					kind: "rule",
					action: "block",
					requests: 4,
				},
				{ id: "gone", name: "gone", kind: "removed", requests: 1 },
			],
		});
		const summary = () =>
			within(screen.getByRole("group", { name: "Blocked requests" }));
		const hrefOf = (link: HTMLElement) =>
			new URL(link.getAttribute("href") ?? "", "http://nebula.test");

		test("the summary and each rule row show what was blocked, and a removed rule is listed by its id", async () => {
			await mount(view({ rules: ownRules }), undefined, { body: busy });
			expect(await summary().findByText("17")).toBeInTheDocument();
			expect(
				summary().getByText(/Blocked in the last 24 hours:/),
			).toBeInTheDocument();

			const own = within(screen.getByRole("list", { name: "Firewall rules" }));
			expect(
				await own.findByRole("link", {
					name: "Rule 1: 4 blocked, see them in Logs",
				}),
			).toHaveTextContent("4");
			// Rule 2 blocked nothing: a dash, no link.
			expect(own.queryByRole("link", { name: /^Rule 2/ })).toBeNull();
			expect(own.getAllByText("—")).toHaveLength(1);

			const standard = within(
				screen.getByRole("list", { name: "Standard protection" }),
			);
			expect(
				standard.getByRole("link", {
					name: "Secret files: 12 blocked, see them in Logs",
				}),
			).toHaveTextContent("12");

			const removed = within(
				summary().getByRole("list", {
					name: "Removed rules that blocked requests",
				}),
			);
			expect(removed.getByText("Removed rule gone")).toBeInTheDocument();
			expect(
				removed.getByRole("link", {
					name: "Removed rule gone: 1 blocked, see them in Logs",
				}),
			).toBeInTheDocument();
		});

		test("nothing blocked reads as zero, with dashes in the rows and no Logs link", async () => {
			await mount(view({ rules: ownRules }));
			expect(
				await summary().findByText(/Nothing was blocked in the last 24 hours/),
			).toBeInTheDocument();
			expect(summary().getByText("0")).toBeInTheDocument();
			expect(
				screen.queryByRole("link", { name: "See blocked requests in Logs" }),
			).toBeNull();
			const own = within(screen.getByRole("list", { name: "Firewall rules" }));
			expect(own.getAllByText("—")).toHaveLength(2);
			const standard = within(
				screen.getByRole("list", { name: "Standard protection" }),
			);
			expect(standard.getAllByText("—")).toHaveLength(1);
		});

		test("the Logs links carry the blocked requests query, for every rule or for one", async () => {
			await mount(view({ rules: ownRules }), undefined, { body: busy });
			const all = hrefOf(
				await screen.findByRole("link", {
					name: "See blocked requests in Logs",
				}),
			);
			expect(all.pathname).toBe("/p/proj-1/prod/logs");
			expect(all.searchParams.get("q")).toBe(
				"stream:requests service:shop @blocked_by:*",
			);
			expect(all.searchParams.get("sources")).toBe("requests");

			const one = hrefOf(
				screen.getByRole("link", {
					name: "Rule 1: 4 blocked, see them in Logs",
				}),
			);
			expect(one.searchParams.get("q")).toBe(
				"stream:requests service:shop @blocked_by:r1",
			);
			const standard = hrefOf(
				screen.getByRole("link", {
					name: "Secret files: 12 blocked, see them in Logs",
				}),
			);
			expect(standard.searchParams.get("q")).toBe(
				"stream:requests service:shop @blocked_by:stdsecrets",
			);
		});

		test("the range switch asks for the other window and relabels the summary", async () => {
			const user = userEvent.setup();
			const api = await mount(view(), undefined, (request) => {
				const range = new URL(request.url).searchParams.get("range");
				return Response.json(
					range === "7d"
						? blocksOf({
								range: "7d",
								total: 40,
								byRule: [
									{
										id: "stdsecrets",
										name: "Secret files",
										kind: "standard",
										requests: 40,
									},
								],
							})
						: blocksOf(),
				);
			});
			await summary().findByText(/Blocked in the last 24 hours:/);
			await user.click(summary().getByRole("button", { name: "7d" }));
			expect(
				await summary().findByText(/Blocked in the last 7 days:/),
			).toBeInTheDocument();
			expect(await summary().findByText("40")).toBeInTheDocument();
			const asked = api.calls
				.filter((c) => c.path === BLOCKS_PATH)
				.map((c) => c.query.get("range"));
			expect(asked).toEqual(["24h", "7d"]);
		});

		test("with traffic analytics off it says nothing is counted and where to turn it on", async () => {
			await mount(view({ rules: ownRules }), undefined, {
				body: blocksOf({ trafficAnalytics: false }),
			});
			expect(
				await summary().findByText(/Blocked requests are not counted/),
			).toBeInTheDocument();
			const link = summary().getByRole("link", {
				name: "Turn it on in the server's settings",
			});
			expect(hrefOf(link).pathname).toBe(
				"/servers/server-1/settings/monitoring",
			);
			expect(
				screen.queryByRole("link", { name: "See blocked requests in Logs" }),
			).toBeNull();
		});

		test("a failed read is reported in place and does not hide the rules", async () => {
			await mount(view({ rules: ownRules }), undefined, () =>
				Response.json(
					{ title: "Internal Server Error", status: 500, detail: "disk full" },
					{ status: 500 },
				),
			);
			expect(
				await summary().findByText(/Blocked requests could not be read/),
			).toBeInTheDocument();
			expect(
				summary().getByRole("button", { name: "Retry" }),
			).toBeInTheDocument();
			expect(screen.getByText("Rule 1")).toBeInTheDocument();
		});

		test("a service without a firewall (tcp or udp) has no summary", async () => {
			await mount(view({ enforcement: "none", standard: [] }));
			expect(
				screen.queryByRole("group", { name: "Blocked requests" }),
			).toBeNull();
		});
	});
});
