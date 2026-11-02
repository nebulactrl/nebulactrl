import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PLAIN_DOMAIN_RULE, type ServiceDomain } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import type { DnsResolverView, DomainCert } from "#/features/dns01/queries";
import { type ApiRoutes, stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CertificateSection } from "./certificate-section";

const makeCert = (overrides: Partial<DomainCert> = {}): DomainCert => ({
	source: "acme",
	status: "active",
	message: "",
	resolver: "",
	wildcard: "",
	issuer: "",
	notAfter: null,
	daysToExpiry: null,
	warning: "",
	automatic: false,
	staged: false,
	fallback: "",
	issuedVia: "",
	...overrides,
});

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

const resolver = (
	overrides: Partial<DnsResolverView> = {},
): DnsResolverView => ({
	provider: "route53",
	providerName: "Amazon Route 53",
	credentialId: "cred-1",
	credentialName: "prod",
	delaySeconds: 0,
	staging: false,
	change: "none",
	automatic: false,
	rotated: false,
	...overrides,
});

const dns01 = (resolvers: DnsResolverView[]) => ({
	body: {
		resolvers,
		wildcards: [],
		acmeEmail: "",
		baseDomain: "",
		staged: false,
	},
});

const problem = (status: number, detail: string, code: string) => ({
	status,
	body: { status, title: "Error", detail, code },
});

async function show(
	domain: ServiceDomain,
	{
		resolvers = [] as DnsResolverView[],
		disabled = false,
		serverId = "server-1" as string | null,
		extra = {} as ApiRoutes,
	} = {},
) {
	const service = makeService({ id: "svc-1", generation: 7, serverId });
	const api = stubApi({
		"GET /servers/:serverId/dns01": dns01(resolvers),
		...extra,
	});
	await renderWithProviders(
		<CertificateSection
			service={service}
			domain={domain}
			disabled={disabled}
		/>,
	);
	return api;
}

/** Opening a Select popup is slow in happy-dom, more so on a busy machine. */
const SLOW = 20_000;

describe("CertificateSection", () => {
	test("a domain with no observed certificate shows only the HTTP-01 choice and a hint to add a resolver", async () => {
		await show(makeDomain());
		expect(screen.getByText("Certificate")).toBeInTheDocument();
		const select = screen.getByRole("combobox", { name: "Obtain it through" });
		expect(select).toHaveTextContent("HTTP-01 (automatic)");
		expect(screen.queryByText("Issued")).not.toBeInTheDocument();
		expect(
			await screen.findByText(
				"Add a resolver under Servers, Proxy, DNS-01 certificates.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText("Saves at once · applies on next deploy"),
		).toBeInTheDocument();
	});

	test("a pending certificate says it is waiting and where it will come from", async () => {
		await show(makeDomain({ cert: makeCert({ status: "pending" }) }));
		expect(screen.getByText("Waiting for certificate")).toBeInTheDocument();
		expect(
			screen.getByText(
				"Issued by Let's Encrypt (HTTP-01) on the first request",
			),
		).toBeInTheDocument();
	});

	test("an issued certificate shows issuer, expiry date and days left", async () => {
		await show(
			makeDomain({
				cert: makeCert({
					issuer: "Let's Encrypt",
					notAfter: "2026-12-01T10:00:00Z",
					daysToExpiry: 45,
				}),
			}),
		);
		expect(screen.getByText("Issued")).toBeInTheDocument();
		expect(
			screen.getByText("Let's Encrypt · expires 2026-12-01 (45 days left)"),
		).toBeInTheDocument();
	});

	test("an issued certificate without an issuer name omits it", async () => {
		await show(
			makeDomain({
				cert: makeCert({ notAfter: "2026-12-01T10:00:00Z", daysToExpiry: 1 }),
			}),
		);
		expect(
			screen.getByText("expires 2026-12-01 (1 day left)"),
		).toBeInTheDocument();
	});

	test("an expired certificate says how long ago", async () => {
		await show(
			makeDomain({
				cert: makeCert({
					notAfter: "2026-01-01T00:00:00Z",
					daysToExpiry: -3,
				}),
			}),
		);
		expect(
			screen.getByText("expires 2026-01-01 (expired 3 days ago)"),
		).toBeInTheDocument();
	});

	test("a failed certificate shows the problem badge and the ACME message", async () => {
		await show(
			makeDomain({
				cert: makeCert({
					status: "error",
					message: "ACME challenge failed: connection refused",
				}),
			}),
		);
		expect(screen.getByText("Problem")).toBeInTheDocument();
		expect(
			screen.getByText("ACME challenge failed: connection refused"),
		).toBeInTheDocument();
	});

	test("an unobserved certificate reads Not observed", async () => {
		await show(makeDomain({ cert: makeCert({ status: "unknown" }) }));
		expect(screen.getByText("Not observed")).toBeInTheDocument();
	});

	test("shows the HTTP-01 warning of a certificate that cannot be issued", async () => {
		await show(
			makeDomain({
				cert: makeCert({
					status: "pending",
					warning: "Port 80 is not published; HTTP-01 cannot validate.",
				}),
			}),
		);
		expect(
			screen.getByText("Port 80 is not published; HTTP-01 cannot validate."),
		).toBeInTheDocument();
	});

	test("explains why a name in a Cloudflare zone fell back to HTTP-01", async () => {
		await show(
			makeDomain({
				cert: makeCert({ fallback: "The zone token cannot edit DNS." }),
			}),
		);
		expect(
			screen.getByText("The zone token cannot edit DNS."),
		).toBeInTheDocument();
	});

	test("a covering wildcard certificate has no resolver to choose", async () => {
		await show(
			makeDomain({
				hostname: "api.example.com",
				cert: makeCert({ source: "wildcard", wildcard: "*.example.com" }),
			}),
		);
		expect(
			screen.getByText("Covered by a wildcard certificate (*.example.com)"),
		).toBeInTheDocument();
		expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
	});

	test("an automatic Cloudflare DNS-01 certificate shows its source and no resolver choice", async () => {
		await show(
			makeDomain({
				cert: makeCert({
					source: "dns01",
					automatic: true,
					issuedVia: "dns-01",
				}),
			}),
		);
		expect(
			screen.getByText("Certificate via Cloudflare DNS (DNS-01)"),
		).toBeInTheDocument();
		expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
	});

	test("an automatic certificate waiting for a proxy deploy reads Staged", async () => {
		await show(
			makeDomain({
				cert: makeCert({ source: "dns01", automatic: true, staged: true }),
			}),
		);
		expect(screen.getByText("Staged")).toBeInTheDocument();
		expect(
			screen.getByText(
				/after the next proxy deploy\. The current certificate keeps serving/,
			),
		).toBeInTheDocument();
	});

	test("an existing HTTP-01 certificate of an automatic name says it renews over HTTP-01", async () => {
		await show(
			makeDomain({
				cert: makeCert({
					source: "dns01",
					automatic: true,
					issuedVia: "http-01",
				}),
			}),
		);
		expect(
			screen.getByText("Certificate via HTTP-01, renews over HTTP-01"),
		).toBeInTheDocument();
		expect(screen.getByTestId("cloudflare-dns01-reason")).toHaveTextContent(
			"New names in this zone use DNS-01 through Cloudflare.",
		);
	});

	test("a wildcard hostname without a resolver warns that HTTP-01 cannot validate it", async () => {
		await show(
			makeDomain({
				hostname: "*.example.com",
				cert: makeCert({ status: "pending" }),
			}),
		);
		expect(
			screen.getByText(/A wildcard hostname needs a DNS-01 resolver/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("combobox", { name: "Obtain it through" }),
		).toHaveTextContent("Choose a resolver…");
	});

	test("a wildcard hostname with a resolver chosen shows no warning", async () => {
		await show(
			makeDomain({
				hostname: "*.example.com",
				dnsResolver: "route53",
				cert: makeCert({ status: "pending", source: "dns01" }),
			}),
			{ resolvers: [resolver()] },
		);
		expect(
			screen.queryByText(/A wildcard hostname needs a DNS-01 resolver/),
		).not.toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Obtain it through" }),
			).toHaveTextContent("DNS-01 · Amazon Route 53"),
		);
	});

	test("a local hostname without a certificate says it uses the proxy's default one", async () => {
		await show(makeDomain({ hostname: "app.localhost" }));
		expect(
			screen.getByText(
				"A local hostname gets the proxy's default certificate. No public certificate is requested.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
	});

	test("a local hostname with an issued certificate still reports it", async () => {
		await show(
			makeDomain({
				hostname: "app.localhost",
				cert: makeCert({ source: "custom" }),
			}),
		);
		expect(screen.getByText("Issued")).toBeInTheDocument();
		expect(screen.getByText("Uploaded certificate")).toBeInTheDocument();
	});

	test("with TLS off the status shows but there is no resolver to pick", async () => {
		await show(
			makeDomain({
				tlsMode: "none",
				cert: makeCert({ source: "none", status: "unknown" }),
			}),
		);
		expect(screen.getByText("No TLS: plain HTTP only")).toBeInTheDocument();
		expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
	});

	test(
		"lists the server's resolvers, marking a staged one and hiding a removed one",
		async () => {
			const user = userEvent.setup();
			await show(makeDomain(), {
				resolvers: [
					resolver(),
					resolver({
						provider: "ovh",
						providerName: "OVH",
						change: "add",
					}),
					resolver({
						provider: "gandiv5",
						providerName: "Gandi",
						change: "remove",
					}),
					resolver({
						provider: "cloudflare",
						providerName: "Cloudflare",
						automatic: true,
					}),
				],
			});
			await waitFor(() =>
				expect(
					screen.queryByText(/Add a resolver under Servers/),
				).not.toBeInTheDocument(),
			);
			await user.click(
				screen.getByRole("combobox", { name: "Obtain it through" }),
			);
			const names = screen.getAllByRole("option").map((o) => o.textContent);
			expect(names).toEqual([
				"HTTP-01 (automatic)",
				"DNS-01 · Amazon Route 53",
				"DNS-01 · OVH (staged)",
				"DNS-01 · Cloudflare (automatic, from the connected account)",
			]);
		},
		SLOW,
	);

	test(
		"choosing a resolver PATCHes the domain with it and If-Match, then toasts",
		async () => {
			const ifMatch: (string | null)[] = [];
			const user = userEvent.setup();
			const api = await show(makeDomain(), {
				resolvers: [resolver()],
				extra: {
					"PATCH /domains/:domainId": (request) => {
						ifMatch.push(request.headers.get("If-Match"));
						return Response.json({});
					},
				},
			});
			await screen.findByText("Saves at once · applies on next deploy");
			await waitFor(() =>
				expect(
					screen.queryByText(/Add a resolver under/),
				).not.toBeInTheDocument(),
			);
			await user.click(
				screen.getByRole("combobox", { name: "Obtain it through" }),
			);
			await user.click(
				screen.getByRole("option", { name: "DNS-01 · Amazon Route 53" }),
			);
			expect(
				await screen.findByText("Domain settings saved"),
			).toBeInTheDocument();
			const patch = api.calls.find((c) => c.method === "PATCH");
			expect(patch?.path).toBe("/domains/dom-1");
			expect(patch?.body).toEqual({ dnsResolver: "route53" });
			expect(ifMatch).toEqual(['"7"']);
		},
		SLOW,
	);

	test(
		"going back to HTTP-01 sends an empty resolver",
		async () => {
			const user = userEvent.setup();
			const api = await show(
				makeDomain({
					dnsResolver: "route53",
					cert: makeCert({ source: "dns01" }),
				}),
				{
					resolvers: [resolver()],
					extra: { "PATCH /domains/:domainId": { body: make("DomainView") } },
				},
			);
			await waitFor(() =>
				expect(
					screen.getByRole("combobox", { name: "Obtain it through" }),
				).toHaveTextContent("DNS-01 · Amazon Route 53"),
			);
			await user.click(
				screen.getByRole("combobox", { name: "Obtain it through" }),
			);
			await user.click(
				screen.getByRole("option", { name: "HTTP-01 (automatic)" }),
			);
			await waitFor(() =>
				expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
					dnsResolver: "",
				}),
			);
		},
		SLOW,
	);

	test(
		"shows the API error under the control when the save is refused",
		async () => {
			const user = userEvent.setup();
			await show(makeDomain(), {
				resolvers: [resolver()],
				extra: {
					"PATCH /domains/:domainId": problem(
						422,
						"resolver route53 is not configured on this server",
						"invalid_resolver",
					),
				},
			});
			await waitFor(() =>
				expect(
					screen.queryByText(/Add a resolver under/),
				).not.toBeInTheDocument(),
			);
			await user.click(
				screen.getByRole("combobox", { name: "Obtain it through" }),
			);
			await user.click(
				screen.getByRole("option", { name: "DNS-01 · Amazon Route 53" }),
			);
			expect(
				await screen.findByText(
					"resolver route53 is not configured on this server",
				),
			).toBeInTheDocument();
			expect(
				screen.queryByText("Domain settings saved"),
			).not.toBeInTheDocument();
		},
		SLOW,
	);

	test("the resolver choice is disabled when the editor is (agent too old)", async () => {
		await show(makeDomain(), { disabled: true });
		expect(
			screen.getByRole("combobox", { name: "Obtain it through" }),
		).toBeDisabled();
	});

	test("without a server there is nothing to choose from and no DNS-01 read", async () => {
		const api = await show(makeDomain(), { serverId: null });
		expect(
			screen.getByRole("combobox", { name: "Obtain it through" }),
		).toBeDisabled();
		const section = document.querySelector("[data-section=certificate]");
		expect(section).not.toBeNull();
		expect(
			within(section as HTMLElement).getByText(/Add a resolver under/),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});
});
