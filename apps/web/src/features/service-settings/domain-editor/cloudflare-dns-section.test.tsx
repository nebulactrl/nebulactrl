import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DnsStatus, DomainDns, OriginLockView } from "#/api/gen/types.gen";
import { PLAIN_DOMAIN_RULE, type ServiceDomain } from "#/api/models";
import { type ApiRoutes, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CloudflareDnsSection } from "./cloudflare-dns-section";

const makeDomain = (overrides: Partial<ServiceDomain> = {}): ServiceDomain => ({
	...PLAIN_DOMAIN_RULE,
	id: "dom-1",
	hostname: "app.example.com",
	targetPort: 80,
	component: "",
	process: "",
	generated: false,
	status: "active",
	statusMessage: null,
	dnsOk: true,
	dnsCheckedAt: null,
	createdAt: null,
	dnsResolver: "",
	cert: null,
	dns: null,
	wwwPartner: "",
	wwwRedirect: false,
	wwwRedirectFor: "",
	...overrides,
});

const cachedDns = (overrides: Partial<DomainDns> = {}): DomainDns => ({
	checkedAt: null,
	error: "",
	manage: true,
	message: "",
	provider: "cloudflare",
	proxied: false,
	status: "managed",
	zoneId: "zone-1",
	zoneName: "example.com",
	...overrides,
});

const lock = (overrides: Partial<OriginLockView> = {}): OriginLockView => ({
	aop: "on",
	aopReason: "",
	aopReasonCode: "",
	enabled: true,
	ipListSource: "api",
	ipLock: true,
	optIn: true,
	permissionHint: "",
	realClientIp: true,
	reason: "",
	...overrides,
});

const liveDns = (overrides: Partial<DnsStatus> = {}): DnsStatus => ({
	checkedAt: null,
	configured: true,
	desired: { type: "A", content: "203.0.113.9", owned: true, proxied: false },
	desiredRecords: null,
	effectiveProxied: false,
	error: "",
	manage: true,
	message: "",
	needsAdopt: false,
	originLock: lock(),
	proxied: false,
	records: [],
	sslMode: "",
	status: "managed",
	tunnel: false,
	warnings: null,
	zoneId: "zone-1",
	zoneName: "example.com",
	...overrides,
});

const connection = (configured: boolean) => ({
	body: make("CloudflareConnectionView", {
		configured,
		accountId: "",
		accountName: "",
		status: configured ? "ok" : "",
		verifiedAt: null,
		zonesSyncedAt: null,
	}),
});

const me = (scopes: string[]) => ({
	body: make("MeBody", {
		principal: { label: "robin", type: "user", scopes },
	}),
});

async function show(
	routes: ApiRoutes,
	{ domain = makeDomain(), admin = true } = {},
) {
	const api = stubApi({
		"GET /integrations/cloudflare": connection(true),
		"GET /me": me(admin ? ["admin"] : ["read"]),
		...routes,
	});
	await renderWithProviders(<CloudflareDnsSection domain={domain} />);
	return api;
}

const proxyGroup = () =>
	screen.getByRole("group", { name: "Cloudflare proxy" });

/** Selects and hold keys are slow in happy-dom, more so on a busy machine. */
const SLOW = 20_000;

describe("CloudflareDnsSection", () => {
	test("renders nothing, and never reads the zone, without a Cloudflare connection", async () => {
		const api = stubApi({
			"GET /integrations/cloudflare": connection(false),
			"GET /me": me(["admin"]),
			"GET /domains/:domainId/dns": { body: liveDns() },
		});
		await renderWithProviders(<CloudflareDnsSection domain={makeDomain()} />);
		await waitFor(() =>
			expect(api.calls.map((c) => c.path)).toContain(
				"/integrations/cloudflare",
			),
		);
		expect(screen.queryByText("Cloudflare DNS")).not.toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/domains/dom-1/dns")).toBe(false);
	});

	test("a managed record shows its status, zone, proxy choice and needs no action", async () => {
		await show({
			"GET /domains/:domainId/dns": {
				body: liveDns({
					records: [
						{ type: "A", content: "203.0.113.9", owned: true, proxied: false },
					],
				}),
			},
		});
		expect(await screen.findByText("Managed")).toBeInTheDocument();
		expect(screen.getByText("zone example.com")).toBeInTheDocument();
		expect(
			screen.getByText(
				"NebulaCtrl manages this record in your Cloudflare zone example.com.",
			),
		).toBeInTheDocument();
		expect(screen.getByText("in zone")).toBeInTheDocument();
		expect(screen.getByText("203.0.113.9")).toBeInTheDocument();
		expect(screen.getByText("managed by NebulaCtrl")).toBeInTheDocument();
		expect(
			within(proxyGroup()).getByRole("button", { name: "DNS only" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(
			screen.queryByRole("button", {
				name: /Create record|Fix DNS|Retry sync/,
			}),
		).not.toBeInTheDocument();
	});

	test("before the live read answers it shows the cached status of the domain", async () => {
		const { promise: gate, resolve: release } = Promise.withResolvers<void>();
		await show(
			{
				"GET /domains/:domainId/dns": async () => {
					await gate;
					return Response.json(liveDns());
				},
			},
			{
				domain: makeDomain({
					dns: cachedDns({
						status: "wrong_target",
						zoneName: "cached.example",
					}),
				}),
			},
		);
		expect(await screen.findByText("Wrong target")).toBeInTheDocument();
		expect(screen.getByText("zone cached.example")).toBeInTheDocument();
		expect(
			screen.getByText("A record exists but points somewhere else."),
		).toBeInTheDocument();
		release();
		expect(await screen.findByText("Managed")).toBeInTheDocument();
	});

	test("a missing record offers Create record, which POSTs the fix without adopting", async () => {
		const api = await show({
			"GET /domains/:domainId/dns": {
				body: liveDns({ status: "missing" }),
			},
			"POST /domains/:domainId/dns/fix": { body: liveDns() },
		});
		const user = userEvent.setup();
		expect(await screen.findByText("No record")).toBeInTheDocument();
		// What NebulaCtrl wants to create is listed.
		expect(screen.getByText("wanted")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create record" }));
		expect(await screen.findByText("DNS fixed")).toBeInTheDocument();
		expect(
			screen.getByText("app.example.com now points at the server."),
		).toBeInTheDocument();
		const fix = api.calls.find((c) => c.method === "POST");
		expect(fix?.path).toBe("/domains/dom-1/dns/fix");
		expect(fix?.body).toEqual({ adopt: false });
	});

	test("a wrong target offers Fix DNS", async () => {
		await show({
			"GET /domains/:domainId/dns": {
				body: liveDns({
					status: "wrong_target",
					records: [
						{ type: "A", content: "198.51.100.1", owned: true, proxied: false },
					],
				}),
			},
		});
		expect(
			await screen.findByRole("button", { name: "Fix DNS" }),
		).toBeInTheDocument();
		expect(screen.getByText("198.51.100.1")).toBeInTheDocument();
	});

	test("a failed last sync is shown and can be retried", async () => {
		const api = await show({
			"GET /domains/:domainId/dns": {
				body: liveDns({ error: "Cloudflare answered 429" }),
			},
			"POST /domains/:domainId/dns/fix": { body: liveDns() },
		});
		const user = userEvent.setup();
		expect(
			await screen.findByText(
				"The last sync failed: Cloudflare answered 429. It is retried in the background.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Retry sync" }));
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
				adopt: false,
			}),
		);
	});

	test("a fix the API refuses shows its message under the section", async () => {
		await show({
			"GET /domains/:domainId/dns": { body: liveDns({ status: "missing" }) },
			"POST /domains/:domainId/dns/fix": problem(502, {
				detail: "Cloudflare rejected the record: zone is paused",
				code: "cloudflare_error",
			}),
		});
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: "Create record" }),
		);
		expect(
			await screen.findByText("Cloudflare rejected the record: zone is paused"),
		).toBeInTheDocument();
		expect(screen.queryByText("DNS fixed")).not.toBeInTheDocument();
	});

	test("an existing record the team created can be adopted by an admin after confirming", async () => {
		const api = await show({
			"GET /domains/:domainId/dns": {
				body: liveDns({
					status: "present",
					records: [
						{ type: "A", content: "203.0.113.9", owned: false, proxied: false },
					],
				}),
			},
			"POST /domains/:domainId/dns/fix": { body: liveDns() },
		});
		const user = userEvent.setup();
		expect(await screen.findByText("Record present")).toBeInTheDocument();
		expect(screen.getByText("not NebulaCtrl")).toBeInTheDocument();
		expect(
			screen.getByText(
				"The existing record is not NebulaCtrl's: it is left alone unless it is adopted.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Adopt record" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Adopt the record for app.example.com?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				/The existing record already points at 203\.0\.113\.9/,
			),
		).toBeInTheDocument();
		// Nothing is sent until the dialog is confirmed.
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
		await user.click(
			within(dialog).getByRole("button", { name: "Adopt record" }),
		);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
				adopt: true,
			}),
		);
		expect(await screen.findByText("DNS fixed")).toBeInTheDocument();
	});

	test("cancelling the adopt dialog sends nothing", async () => {
		const api = await show({
			"GET /domains/:domainId/dns": { body: liveDns({ status: "present" }) },
		});
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: "Adopt record" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
	});

	test("without the admin scope the adopt button is withheld and the reason given", async () => {
		await show(
			{
				"GET /domains/:domainId/dns": { body: liveDns({ status: "present" }) },
			},
			{ admin: false },
		);
		expect(
			await screen.findByText(/The existing record is not NebulaCtrl's/),
		).toHaveTextContent("An admin can do this.");
		expect(
			screen.queryByRole("button", { name: "Adopt record" }),
		).not.toBeInTheDocument();
	});

	test(
		"a conflict shows the API message as a warning and asks before replacing the records",
		async () => {
			const api = await show({
				"GET /domains/:domainId/dns": {
					body: liveDns({
						status: "conflict",
						message: "app.example.com already has an A record to 198.51.100.7.",
						records: [
							{
								type: "A",
								content: "198.51.100.7",
								owned: false,
								proxied: true,
							},
						],
					}),
				},
				"POST /domains/:domainId/dns/fix": { body: liveDns() },
			});
			const user = userEvent.setup();
			expect(await screen.findByText("Conflicting record")).toBeInTheDocument();
			expect(
				screen.getByText(
					"app.example.com already has an A record to 198.51.100.7.",
				),
			).toBeInTheDocument();
			expect(
				screen.getByText(
					"NebulaCtrl leaves these records as they are until they are replaced.",
				),
			).toBeInTheDocument();
			// A conflicting record is not "managed by NebulaCtrl".
			expect(
				screen.queryByText(
					/NebulaCtrl manages this record in your Cloudflare zone/,
				),
			).not.toBeInTheDocument();
			await user.click(
				screen.getByRole("button", {
					name: "Replace with NebulaCtrl's record",
				}),
			);
			const dialog = await screen.findByRole("alertdialog");
			expect(
				within(dialog).getByText("Replace the record for app.example.com?"),
			).toBeInTheDocument();
			expect(
				within(dialog).getByText(
					/NebulaCtrl replaces or deletes: A 198\.51\.100\.7 \(Proxied\)/,
				),
			).toBeInTheDocument();
			// The destructive replace is confirmed by holding the key, not by a click.
			const hold = within(dialog).getByRole("button", {
				name: /Hold to replace with nebulactrl's record/i,
			});
			await user.click(hold);
			expect(api.calls.some((c) => c.method === "POST")).toBe(false);
			fireEvent.keyDown(hold, { key: "Enter" });
			await waitFor(
				() =>
					expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
						adopt: true,
					}),
				{ timeout: 10_000 },
			);
		},
		SLOW,
	);

	test("a hostname outside every zone has no proxy controls and no action", async () => {
		await show({
			"GET /domains/:domainId/dns": {
				body: liveDns({ status: "not_in_zone", zoneName: "", zoneId: "" }),
			},
		});
		expect(
			await screen.findByText("Not in a connected zone"),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("group", { name: "Cloudflare proxy" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByText(/NebulaCtrl manages this record/),
		).not.toBeInTheDocument();
		expect(
			screen.getByText(
				/No zone of the Cloudflare token contains this hostname/,
			),
		).toBeInTheDocument();
	});

	test("switching the proxy sends PUT with proxied, and a DNS sync error comes back as a toast", async () => {
		const api = await show({
			"GET /domains/:domainId/dns": { body: liveDns() },
			"PUT /domains/:domainId/dns": {
				body: liveDns({ proxied: true, error: "zone settings rejected" }),
			},
		});
		const user = userEvent.setup();
		await screen.findByText("Managed");
		await user.click(
			within(proxyGroup()).getByRole("button", { name: "Proxied" }),
		);
		expect(
			await screen.findByText("Saved, but the DNS sync failed"),
		).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/domains/dom-1/dns");
		expect(put?.body).toEqual({ proxied: true });
	});

	test("a proxied record switches back to DNS only", async () => {
		const api = await show({
			"GET /domains/:domainId/dns": {
				body: liveDns({ proxied: true, effectiveProxied: true }),
			},
			"PUT /domains/:domainId/dns": { body: liveDns() },
		});
		const user = userEvent.setup();
		await screen.findByText("Managed");
		expect(
			within(proxyGroup()).getByRole("button", { name: "Proxied" }),
		).toHaveAttribute("aria-pressed", "true");
		await user.click(
			within(proxyGroup()).getByRole("button", { name: "DNS only" }),
		);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
				proxied: false,
			}),
		);
	});

	test("a tunnel record is always proxied and cannot be switched to DNS only", async () => {
		const api = await show({
			"GET /domains/:domainId/dns": {
				body: liveDns({
					tunnel: true,
					proxied: false,
					originLock: lock({ enabled: false, reason: "tunnel" }),
				}),
			},
		});
		await screen.findByText("Managed");
		expect(
			screen.getByText("Tunnel records are always proxied."),
		).toBeInTheDocument();
		expect(
			within(proxyGroup()).getByRole("button", { name: "Proxied" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(
			within(proxyGroup()).getByRole("button", { name: "DNS only" }),
		).toBeDisabled();
		expect(api.calls.some((c) => c.method === "PUT")).toBe(false);
	});

	test("a refused proxy change shows the API message", async () => {
		await show({
			"GET /domains/:domainId/dns": { body: liveDns() },
			"PUT /domains/:domainId/dns": problem(403, {
				detail: "The token cannot edit this zone",
				code: "forbidden",
			}),
		});
		const user = userEvent.setup();
		await screen.findByText("Managed");
		await user.click(
			within(proxyGroup()).getByRole("button", { name: "Proxied" }),
		);
		expect(
			await screen.findByText("The token cannot edit this zone"),
		).toBeInTheDocument();
	});

	test("the origin lock is shown inside the section and re-locking sends originLock true", async () => {
		const api = await show({
			"GET /domains/:domainId/dns": {
				body: liveDns({
					proxied: true,
					effectiveProxied: true,
					originLock: lock({
						enabled: false,
						optIn: false,
						reason: "opted_out",
					}),
				}),
			},
			"PUT /domains/:domainId/dns": { body: liveDns() },
		});
		const user = userEvent.setup();
		expect(
			await screen.findByText("Origin locked to Cloudflare"),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("switch", { name: "Lock the origin to Cloudflare" }),
		);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
				originLock: true,
			}),
		);
	});

	test("lists the live warnings of the zone", async () => {
		await show({
			"GET /domains/:domainId/dns": {
				body: liveDns({
					warnings: ["SSL mode is flexible: use full (strict)."],
				}),
			},
		});
		expect(
			await screen.findByText("SSL mode is flexible: use full (strict)."),
		).toBeInTheDocument();
	});

	test("when the zone cannot be read the API error shows and the cached state stays", async () => {
		await show(
			{
				"GET /domains/:domainId/dns": problem(502, {
					detail: "Cloudflare is unreachable",
					code: "cloudflare_error",
				}),
			},
			{ domain: makeDomain({ dns: cachedDns({ zoneName: "example.com" }) }) },
		);
		expect(
			await screen.findByText("Cloudflare is unreachable"),
		).toBeInTheDocument();
		expect(screen.getByText("Managed")).toBeInTheDocument();
	});

	test("Check DNS again reads the zone once more", async () => {
		let reads = 0;
		await show({
			"GET /domains/:domainId/dns": () => {
				reads += 1;
				return Response.json(liveDns());
			},
		});
		const user = userEvent.setup();
		await screen.findByText("Managed");
		expect(reads).toBe(1);
		await user.click(screen.getByRole("button", { name: "Check DNS again" }));
		await waitFor(() => expect(reads).toBe(2));
	});
});
