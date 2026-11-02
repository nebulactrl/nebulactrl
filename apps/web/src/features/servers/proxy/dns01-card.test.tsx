import { describe, expect, it, mock } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	DnsCredential,
	DnsProviderSpec,
	DnsResolverView,
	ServerDns01,
	WildcardCertView,
} from "#/features/dns01/queries";
import {
	type ApiReply,
	type ApiRoutes,
	type ApiStub,
	stubApi,
} from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { Dns01Card } from "./dns01-card";

const SERVER = "srv_1";

const specs: DnsProviderSpec[] = [
	{ code: "cloudflare", name: "Cloudflare", docs: "https://x/cf", fields: [] },
	{ code: "route53", name: "Route 53", docs: "https://x/r53", fields: [] },
	{ code: "hetzner", name: "Hetzner", docs: "https://x/hz", fields: [] },
];

const resolver = (over: Partial<DnsResolverView> = {}): DnsResolverView => ({
	provider: "cloudflare",
	providerName: "Cloudflare",
	credentialId: "cred_cf",
	credentialName: "cf-prod",
	delaySeconds: 0,
	staging: false,
	change: "none",
	automatic: false,
	rotated: false,
	...over,
});

const wildcard = (over: Partial<WildcardCertView> = {}): WildcardCertView => ({
	id: "wc_1",
	domain: "example.com",
	names: ["*.example.com", "example.com"],
	includeApex: true,
	provider: "cloudflare",
	coverGenerated: false,
	default: false,
	status: "active",
	statusMessage: "",
	issuer: "",
	notAfter: null,
	daysToExpiry: null,
	checkedAt: null,
	change: "none",
	...over,
});

const dns01 = (over: Partial<ServerDns01> = {}): ServerDns01 => ({
	resolvers: [],
	wildcards: [],
	acmeEmail: "ops@example.com",
	baseDomain: "",
	staged: false,
	...over,
});

const credential = (over: Partial<DnsCredential> = {}): DnsCredential => ({
	id: "cred_cf",
	scope: "team",
	serverId: null,
	name: "cf-prod",
	provider: "cloudflare",
	providerName: "Cloudflare",
	fields: [],
	rev: 1,
	usedBy: [],
	...over,
});

interface Options {
	state?: ServerDns01 | ApiReply;
	credentials?: DnsCredential[];
	providers?: DnsProviderSpec[];
	scopes?: string[];
	routes?: ApiRoutes;
	agentTooOld?: boolean;
	onOpenSettings?: () => void;
}

async function setup(options: Options = {}) {
	const {
		state = dns01(),
		credentials = [],
		providers = specs,
		scopes = ["admin"],
		routes = {},
		agentTooOld = false,
		onOpenSettings,
	} = options;
	const api: ApiStub = stubApi({
		"GET /me": {
			body: make("MeBody", {
				principal: { label: "me", type: "user", scopes },
			}),
		},
		"GET /servers/:serverId/dns01":
			"resolvers" in state ? { body: state } : (state as ApiReply),
		"GET /dns/credentials": {
			body: make("CredentialsOutputBody", {
				items: credentials.map((c) => make("DNSCredential", c)),
			}),
		},
		"GET /dns/providers": {
			body: make("DNSProviderList", {
				items: providers.map((p) =>
					make("DNSProviderSpec", { generic: false, ...p }),
				),
			}),
		},
		...routes,
	});
	const view = await renderWithProviders(
		<Dns01Card
			serverId={SERVER}
			agentTooOld={agentTooOld}
			{...(onOpenSettings ? { onOpenSettings } : {})}
		/>,
	);
	return { api, user: userEvent.setup(), ...view };
}

const writes = (api: ApiStub) => api.calls.filter((c) => c.method !== "GET");

const count = (api: ApiStub, path: string) =>
	api.calls.filter((c) => c.method === "GET" && c.path === path).length;

/** Resolves once the card has its data (the heading of the credentials section). */
const ready = () => screen.findByRole("region", { name: "Credentials" });

const resolversSection = () =>
	screen.getByRole("region", { name: "Resolvers" });
const wildcardsSection = () =>
	screen.getByRole("region", { name: "Wildcard certificates" });

async function pick(
	user: ReturnType<typeof userEvent.setup>,
	label: string,
	option: string | RegExp,
) {
	await user.click(screen.getByRole("combobox", { name: label }));
	await user.click(await screen.findByRole("option", { name: option }));
}

describe("Dns01Card: loading and prerequisites", () => {
	it("says the settings are loading until the server's DNS-01 state arrives", async () => {
		await setup({ state: () => new Promise<Response>(() => {}) });
		expect(screen.getByText("Loading DNS-01 settings…")).toBeInTheDocument();
		expect(screen.queryByRole("region", { name: "Credentials" })).toBeNull();
	});

	it("blocks resolvers and offers the settings when the ACME email is missing", async () => {
		const onOpenSettings = mock(() => {});
		const { user } = await setup({
			state: dns01({ acmeEmail: "", resolvers: [resolver()] }),
			credentials: [credential()],
			onOpenSettings,
		});
		await ready();
		expect(
			screen.getByText("An ACME email is needed first"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("combobox", { name: "Provider of the new resolver" }),
		).toBeDisabled();
		expect(screen.getByRole("button", { name: "Add resolver" })).toBeDisabled();

		await user.click(
			screen.getByRole("button", { name: "Open server settings" }),
		);
		expect(onOpenSettings).toHaveBeenCalledTimes(1);
	});

	it("has no settings button without a handler, and no alert once an email is set", async () => {
		await setup({ state: dns01({ acmeEmail: "" }) });
		await ready();
		expect(
			screen.getByText("An ACME email is needed first"),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Open server settings" }),
		).toBeNull();
	});

	it("shows no ACME alert when the server has an email", async () => {
		await setup();
		await ready();
		expect(screen.queryByText("An ACME email is needed first")).toBeNull();
	});
});

describe("Dns01Card: credentials", () => {
	it("says there are none yet", async () => {
		await setup();
		await ready();
		expect(screen.getByText("No credentials yet.")).toBeInTheDocument();
	});

	it("lists credentials with provider and scope", async () => {
		await setup({
			credentials: [
				credential(),
				credential({
					id: "cred_r53",
					name: "r53-here",
					provider: "route53",
					providerName: "Route 53",
					scope: "server",
					serverId: SERVER,
				}),
			],
		});
		const section = await ready();
		const items = within(section).getAllByRole("listitem");
		expect(items).toHaveLength(2);
		expect(within(items[0] as HTMLElement).getByText("cf-prod")).toBeVisible();
		expect(
			within(items[0] as HTMLElement).getByText("Cloudflare"),
		).toBeVisible();
		expect(within(items[0] as HTMLElement).getByText("team")).toBeVisible();
		expect(
			within(items[1] as HTMLElement).getByText("this server"),
		).toBeVisible();
	});

	it("asks for the credentials usable on this server", async () => {
		const { api } = await setup();
		await ready();
		const call = api.calls.find((c) => c.path === "/dns/credentials");
		expect(call?.query.get("serverId")).toBe(SERVER);
	});

	it("does not let a credential that resolvers still use be deleted, and names the servers", async () => {
		await setup({
			credentials: [
				credential({
					usedBy: [
						{ serverId: "s1", serverName: "edge-1" },
						{ serverId: "s2", serverName: "edge-2" },
					],
				}),
				credential({ id: "cred_free", name: "spare" }),
			],
		});
		await ready();
		const used = screen.getByRole("button", {
			name: "Delete credential cf-prod",
		});
		expect(used).toBeDisabled();
		expect(used).toHaveAttribute("title", "Used by edge-1, edge-2");
		expect(
			screen.getByRole("button", { name: "Delete credential spare" }),
		).toBeEnabled();
	});

	it("confirms before deleting a credential, then deletes it and refetches the list", async () => {
		let items = [credential()];
		const { api, user } = await setup({
			routes: {
				"GET /dns/credentials": () => Response.json({ items }),
				"DELETE /dns/credentials/:credentialId": () => {
					items = [];
					return new Response(null, { status: 204 });
				},
			},
		});
		await ready();
		await user.click(
			await screen.findByRole("button", { name: "Delete credential cf-prod" }),
		);
		expect(await screen.findByText("Delete cf-prod?")).toBeInTheDocument();
		expect(writes(api)).toEqual([]);

		await user.click(screen.getByRole("button", { name: "Delete credential" }));
		await waitFor(() =>
			expect(screen.getByText("No credentials yet.")).toBeInTheDocument(),
		);
		expect(writes(api).map((c) => `${c.method} ${c.path}`)).toEqual([
			"DELETE /dns/credentials/cred_cf",
		]);
		expect(api.unmatched).toEqual([]);
	});

	it("keeps the credential and shows the error when the delete is refused", async () => {
		const { api, user } = await setup({
			credentials: [credential()],
			routes: {
				"DELETE /dns/credentials/:credentialId": problem(409, {
					code: "in_use",
					detail: "Still used by a resolver",
				}),
			},
		});
		await ready();
		await user.click(
			await screen.findByRole("button", { name: "Delete credential cf-prod" }),
		);
		await user.click(
			await screen.findByRole("button", { name: "Delete credential" }),
		);
		expect(
			(await screen.findAllByText("Still used by a resolver")).length,
		).toBeGreaterThan(0);
		expect(screen.getByText("cf-prod")).toBeInTheDocument();
		expect(writes(api)).toHaveLength(1);
	});

	it("lets only an administrator add or delete credentials", async () => {
		await setup({ scopes: ["read"], credentials: [credential()] });
		await ready();
		const add = await screen.findByRole("button", { name: "Add credential" });
		expect(add).toBeDisabled();
		expect(add).toHaveAttribute(
			"title",
			"Only an administrator can add credentials",
		);
		expect(
			screen.getByRole("button", { name: "Delete credential cf-prod" }),
		).toBeDisabled();
	});

	it("disables adding a credential for an outdated agent or without providers", async () => {
		const { unmount } = await setup({ agentTooOld: true });
		await ready();
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Add credential" }),
			).toBeDisabled(),
		);
		unmount();

		await setup({ providers: [] });
		await ready();
		expect(
			screen.getByRole("button", { name: "Add credential" }),
		).toBeDisabled();
	});

	it("opens the credential form, saves, then closes it and refetches the credentials", async () => {
		const { api, user } = await setup({
			providers: [
				{
					code: "cloudflare",
					name: "Cloudflare",
					docs: "https://x/cf",
					fields: [
						{ env: "CF_TOKEN", label: "Token", secret: true, required: true },
					],
				},
			],
			routes: {
				"POST /dns/credentials": { status: 201, body: make("DNSCredential") },
			},
		});
		await ready();
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Add credential" }),
			).toBeEnabled(),
		);
		const before = count(api, "/dns/credentials");
		await user.click(screen.getByRole("button", { name: "Add credential" }));
		await user.type(screen.getByLabelText("Credential name"), "cf-new");
		await user.type(screen.getByLabelText(/^Token/), "tok");
		await user.click(screen.getByRole("button", { name: "Save credential" }));

		await waitFor(() =>
			expect(screen.queryByLabelText("Credential name")).toBeNull(),
		);
		expect(writes(api)[0]?.body).toEqual({
			name: "cf-new",
			provider: "cloudflare",
			values: { CF_TOKEN: "tok" },
		});
		expect(count(api, "/dns/credentials")).toBeGreaterThan(before);
	});

	it("closes the form with Cancel", async () => {
		const { user } = await setup();
		await ready();
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Add credential" }),
			).toBeEnabled(),
		);
		await user.click(screen.getByRole("button", { name: "Add credential" }));
		expect(screen.getByLabelText("Credential name")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(screen.queryByLabelText("Credential name")).toBeNull();
	});
});

describe("Dns01Card: resolvers", () => {
	it("describes each resolver: credential, staging CA, rotation and staged change", async () => {
		await setup({
			state: dns01({
				resolvers: [
					resolver({ staging: true, rotated: true, change: "update" }),
					resolver({
						provider: "route53",
						providerName: "Route 53",
						credentialName: "",
						change: "add",
					}),
					resolver({
						provider: "hetzner",
						providerName: "Hetzner",
						credentialName: "hz",
						change: "remove",
					}),
				],
			}),
			credentials: [credential()],
		});
		await ready();
		const [cf, r53, hz] = within(resolversSection()).getAllByRole("listitem");
		expect(within(cf as HTMLElement).getByText("uses cf-prod")).toBeVisible();
		expect(within(cf as HTMLElement).getByText("staging CA")).toBeVisible();
		expect(
			within(cf as HTMLElement).getByText("credential rotated"),
		).toBeVisible();
		expect(within(cf as HTMLElement).getByText("Change staged")).toBeVisible();
		expect(
			within(r53 as HTMLElement).getByText("uses (removed)"),
		).toBeVisible();
		expect(within(r53 as HTMLElement).getByText("Staged")).toBeVisible();
		expect(within(hz as HTMLElement).getByText("Removal staged")).toBeVisible();
		expect(
			within(hz as HTMLElement).getByText("Removal staged"),
		).toHaveAttribute("title", "Staged: removed on the next Deploy proxy");
	});

	it("offers no controls for the automatic Cloudflare resolver or one staged for removal", async () => {
		await setup({
			state: dns01({
				resolvers: [
					resolver({ automatic: true, credentialName: "" }),
					resolver({
						provider: "hetzner",
						providerName: "Hetzner",
						change: "remove",
					}),
				],
			}),
		});
		await ready();
		const items = within(resolversSection()).getAllByRole("listitem");
		expect(
			within(items[0] as HTMLElement).getByText(
				"Cloudflare (automatic, from the connected account)",
			),
		).toBeVisible();
		expect(within(items[0] as HTMLElement).queryByText(/^uses /)).toBeNull();
		for (const item of items) {
			expect(within(item).queryByRole("switch")).toBeNull();
			expect(within(item).queryByRole("button")).toBeNull();
		}
	});

	it("stages Let's Encrypt staging for a resolver with its credential and delay kept", async () => {
		const { api, user } = await setup({
			state: dns01({ resolvers: [resolver({ delaySeconds: 30 })] }),
			routes: {
				"PUT /servers/:serverId/dns01/resolvers/:provider": { body: dns01() },
			},
		});
		await ready();
		await user.click(
			screen.getByRole("switch", { name: "Let's Encrypt staging" }),
		);
		await waitFor(() => expect(writes(api)).toHaveLength(1));
		expect(writes(api)[0]).toMatchObject({
			method: "PUT",
			path: `/servers/${SERVER}/dns01/resolvers/cloudflare`,
			body: { credentialId: "cred_cf", delaySeconds: 30, staging: true },
		});
		expect(
			(await screen.findAllByText("Resolver staged")).length,
		).toBeGreaterThan(0);
	});

	it("turns staging off again for a resolver that has it on", async () => {
		const { api, user } = await setup({
			state: dns01({ resolvers: [resolver({ staging: true })] }),
			routes: {
				"PUT /servers/:serverId/dns01/resolvers/:provider": { body: dns01() },
			},
		});
		await ready();
		const toggle = screen.getByRole("switch", {
			name: "Let's Encrypt staging",
		});
		expect(toggle).toBeChecked();
		await user.click(toggle);
		await waitFor(() => expect(writes(api)).toHaveLength(1));
		expect(writes(api)[0]?.body).toMatchObject({ staging: false });
	});

	it("disables the staging switch for an outdated agent", async () => {
		const { api, user } = await setup({
			agentTooOld: true,
			state: dns01({ resolvers: [resolver()] }),
		});
		await ready();
		const toggle = screen.getByRole("switch", {
			name: "Let's Encrypt staging",
		});
		expect(toggle).toHaveAttribute("aria-disabled", "true");
		await user.click(toggle);
		expect(toggle).not.toBeChecked();
		expect(writes(api)).toEqual([]);
	});

	it("removes a resolver and refetches the state", async () => {
		let state = dns01({ resolvers: [resolver()] });
		const { api, user } = await setup({
			routes: {
				"GET /servers/:serverId/dns01": () => Response.json(state),
				"DELETE /servers/:serverId/dns01/resolvers/:provider": () => {
					state = dns01({ resolvers: [resolver({ change: "remove" })] });
					return new Response(null, { status: 204 });
				},
			},
		});
		await ready();
		await user.click(
			screen.getByRole("button", { name: "Remove the Cloudflare resolver" }),
		);
		expect(await screen.findByText("Removal staged")).toBeInTheDocument();
		expect(writes(api).map((c) => `${c.method} ${c.path}`)).toEqual([
			`DELETE /servers/${SERVER}/dns01/resolvers/cloudflare`,
		]);
		expect(
			screen.queryByRole("button", { name: "Remove the Cloudflare resolver" }),
		).toBeNull();
	});

	it("shows the error when a resolver change is refused", async () => {
		const { user } = await setup({
			state: dns01({ resolvers: [resolver()] }),
			routes: {
				"PUT /servers/:serverId/dns01/resolvers/:provider": problem(422, {
					code: "invalid",
					detail: "The agent is too old for DNS-01",
				}),
			},
		});
		await ready();
		await user.click(
			screen.getByRole("switch", { name: "Let's Encrypt staging" }),
		);
		expect(
			await screen.findByText("The agent is too old for DNS-01"),
		).toBeInTheDocument();
		expect(screen.queryByText("Resolver staged")).toBeNull();
	});

	it("adds a resolver: provider choice picks its first usable credential, then resets", async () => {
		let state = dns01();
		const { api, user } = await setup({
			credentials: [
				credential({
					id: "cred_other",
					name: "other-server",
					scope: "server",
					serverId: "srv_other",
				}),
				credential({
					id: "cred_r53_a",
					name: "r53-a",
					provider: "route53",
					providerName: "Route 53",
				}),
				credential({
					id: "cred_r53_b",
					name: "r53-b",
					provider: "route53",
					providerName: "Route 53",
					scope: "server",
					serverId: SERVER,
				}),
			],
			routes: {
				"GET /servers/:serverId/dns01": () => Response.json(state),
				"PUT /servers/:serverId/dns01/resolvers/:provider": () => {
					state = dns01({
						resolvers: [
							resolver({
								provider: "route53",
								providerName: "Route 53",
								credentialId: "cred_r53_a",
								credentialName: "r53-a",
								change: "add",
							}),
						],
					});
					return new Response(null, { status: 204 });
				},
			},
		});
		await ready();
		const add = screen.getByRole("button", { name: "Add resolver" });
		expect(add).toBeDisabled();

		await pick(user, "Provider of the new resolver", "Route 53");
		expect(
			screen.getByRole("combobox", { name: "Credential for the new resolver" }),
		).toHaveTextContent("r53-a");
		expect(add).toBeEnabled();
		await user.click(add);

		expect(await screen.findByText("uses r53-a")).toBeInTheDocument();
		expect(writes(api)[0]).toMatchObject({
			method: "PUT",
			path: `/servers/${SERVER}/dns01/resolvers/route53`,
			body: { credentialId: "cred_r53_a", delaySeconds: 0, staging: false },
		});
		expect(
			screen.queryByRole("combobox", {
				name: "Credential for the new resolver",
			}),
		).toBeNull();
		expect(
			screen.getByRole("combobox", { name: "Provider of the new resolver" }),
		).toHaveTextContent("Add resolver…");
	});

	it("lets another usable credential of the provider be chosen", async () => {
		const { api, user } = await setup({
			credentials: [
				credential({ id: "cred_a", name: "cf-a" }),
				credential({ id: "cred_b", name: "cf-b" }),
			],
			routes: {
				"PUT /servers/:serverId/dns01/resolvers/:provider": { body: dns01() },
			},
		});
		await ready();
		await pick(user, "Provider of the new resolver", "Cloudflare");
		await pick(user, "Credential for the new resolver", "cf-b");
		await user.click(screen.getByRole("button", { name: "Add resolver" }));
		await waitFor(() => expect(writes(api)).toHaveLength(1));
		expect(writes(api)[0]?.body).toMatchObject({ credentialId: "cred_b" });
	});

	it("marks providers without a usable credential and cannot pick them", async () => {
		const { user } = await setup({
			credentials: [
				credential({
					id: "cred_foreign",
					provider: "route53",
					providerName: "Route 53",
					scope: "server",
					serverId: "srv_other",
				}),
			],
		});
		await ready();
		await user.click(
			screen.getByRole("combobox", { name: "Provider of the new resolver" }),
		);
		const option = await screen.findByRole("option", {
			name: "Route 53 · no credential",
		});
		expect(option).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByRole("option", { name: "Cloudflare · no credential" }),
		).toBeInTheDocument();
	});

	it("leaves out providers that already have a resolver and adds lego providers that have a credential", async () => {
		const { user } = await setup({
			state: dns01({ resolvers: [resolver()] }),
			credentials: [
				credential(),
				credential({
					id: "cred_ovh",
					name: "ovh-1",
					provider: "ovh",
					providerName: "OVH",
				}),
			],
		});
		await ready();
		await user.click(
			screen.getByRole("combobox", { name: "Provider of the new resolver" }),
		);
		const options = (await screen.findAllByRole("option")).map(
			(o) => o.textContent,
		);
		expect(options).toEqual([
			"Route 53 · no credential",
			"Hetzner · no credential",
			"OVH",
		]);
	});

	it("keeps a provider whose resolver is staged for removal available again", async () => {
		const { user } = await setup({
			state: dns01({ resolvers: [resolver({ change: "remove" })] }),
			credentials: [credential()],
		});
		await ready();
		await user.click(
			screen.getByRole("combobox", { name: "Provider of the new resolver" }),
		);
		expect(
			await screen.findByRole("option", { name: "Cloudflare" }),
		).toBeInTheDocument();
	});

	it("hides the add-resolver row when every provider already has one", async () => {
		await setup({
			state: dns01({
				resolvers: specs.map((s) =>
					resolver({ provider: s.code, providerName: s.name }),
				),
			}),
		});
		await ready();
		expect(
			screen.queryByRole("combobox", { name: "Provider of the new resolver" }),
		).toBeNull();
		expect(screen.queryByRole("button", { name: "Add resolver" })).toBeNull();
	});

	it("disables adding a resolver for an outdated agent", async () => {
		await setup({ agentTooOld: true, credentials: [credential()] });
		await ready();
		expect(
			screen.getByRole("combobox", { name: "Provider of the new resolver" }),
		).toBeDisabled();
		expect(screen.getByRole("button", { name: "Add resolver" })).toBeDisabled();
	});
});

describe("Dns01Card: wildcard certificates", () => {
	it("explains that a wildcard covers every subdomain when there is none", async () => {
		await setup();
		await ready();
		expect(
			screen.getByText(
				"None. A wildcard covers every subdomain with one certificate.",
			),
		).toBeInTheDocument();
	});

	it("shows the server's generated-domain base when it has one", async () => {
		await setup({ state: dns01({ baseDomain: "apps.nebula.test" }) });
		await ready();
		expect(screen.getByText("apps.nebula.test")).toBeInTheDocument();
		expect(screen.getByText(/can cover them/)).toBeInTheDocument();
	});

	it("shows an issued wildcard: names, status, issuer, expiry and default badge", async () => {
		await setup({
			state: dns01({
				resolvers: [resolver()],
				wildcards: [
					wildcard({
						status: "active",
						issuer: "Let's Encrypt",
						notAfter: "2027-01-15T10:00:00Z",
						daysToExpiry: 12,
						default: true,
						change: "update",
					}),
				],
			}),
		});
		await ready();
		const row = within(wildcardsSection()).getByRole("listitem");
		expect(within(row).getByText("*.example.com, example.com")).toBeVisible();
		expect(within(row).getByText("Issued")).toBeVisible();
		expect(
			within(row).getByText(
				"Let's Encrypt · expires 2027-01-15 (12 days left)",
			),
		).toBeVisible();
		expect(within(row).getByText("default certificate")).toBeVisible();
		expect(within(row).getByText("Change staged")).toBeVisible();
		expect(
			within(row).getByRole("switch", {
				name: "Serve as the default certificate",
			}),
		).toBeChecked();
	});

	it("says an expired certificate expired, without an issuer prefix when unknown", async () => {
		await setup({
			state: dns01({
				resolvers: [resolver()],
				wildcards: [
					wildcard({ notAfter: "2026-10-01T00:00:00Z", daysToExpiry: -3 }),
				],
			}),
		});
		await ready();
		expect(
			screen.getByText("expires 2026-10-01 (expired 3 days ago)"),
		).toBeInTheDocument();
	});

	it("shows a pending wildcard as waiting, with no expiry line", async () => {
		await setup({
			state: dns01({
				resolvers: [resolver()],
				wildcards: [
					wildcard({
						status: "pending",
						statusMessage: "Requested after the next deploy",
					}),
				],
			}),
		});
		await ready();
		expect(screen.getByText("Waiting for certificate")).toBeInTheDocument();
		expect(
			screen.getByText("Requested after the next deploy"),
		).toBeInTheDocument();
		expect(screen.queryByText(/expires/)).toBeNull();
	});

	it("shows a failed wildcard as a problem with the reason", async () => {
		await setup({
			state: dns01({
				resolvers: [resolver()],
				wildcards: [
					wildcard({
						status: "error",
						statusMessage: "DNS challenge timed out",
					}),
				],
			}),
		});
		await ready();
		expect(screen.getByText("Problem")).toBeInTheDocument();
		expect(screen.getByText("DNS challenge timed out")).toBeInTheDocument();
	});

	it("patches 'also cover generated domains' and the default flag separately", async () => {
		const { api, user } = await setup({
			state: dns01({ resolvers: [resolver()], wildcards: [wildcard()] }),
			routes: { "PATCH /dns01/wildcards/:wildcardId": { body: dns01() } },
		});
		await ready();
		await user.click(
			screen.getByRole("switch", { name: "Also cover generated domains" }),
		);
		await waitFor(() => expect(writes(api)).toHaveLength(1));
		await user.click(
			screen.getByRole("switch", { name: "Serve as the default certificate" }),
		);
		await waitFor(() => expect(writes(api)).toHaveLength(2));
		expect(writes(api).map((c) => [c.method, c.path, c.body])).toEqual([
			["PATCH", "/dns01/wildcards/wc_1", { coverGenerated: true }],
			["PATCH", "/dns01/wildcards/wc_1", { default: true }],
		]);
	});

	it("removes a wildcard", async () => {
		const { api, user } = await setup({
			state: dns01({ resolvers: [resolver()], wildcards: [wildcard()] }),
			routes: { "DELETE /dns01/wildcards/:wildcardId": { body: dns01() } },
		});
		await ready();
		await user.click(
			screen.getByRole("button", { name: "Remove *.example.com" }),
		);
		await waitFor(() => expect(writes(api)).toHaveLength(1));
		expect(writes(api)[0]).toMatchObject({
			method: "DELETE",
			path: "/dns01/wildcards/wc_1",
		});
	});

	it("disables a wildcard's controls for an outdated agent", async () => {
		const { api, user } = await setup({
			agentTooOld: true,
			state: dns01({ resolvers: [resolver()], wildcards: [wildcard()] }),
		});
		await ready();
		expect(
			screen.getByRole("button", { name: "Remove *.example.com" }),
		).toBeDisabled();
		const generated = screen.getByRole("switch", {
			name: "Also cover generated domains",
		});
		const standard = screen.getByRole("switch", {
			name: "Serve as the default certificate",
		});
		expect(generated).toHaveAttribute("aria-disabled", "true");
		expect(standard).toHaveAttribute("aria-disabled", "true");
		await user.click(generated);
		await user.click(standard);
		expect(writes(api)).toEqual([]);
		expect(screen.getByRole("button", { name: "Add wildcard" })).toBeDisabled();
	});

	it("shows the error when a wildcard change is refused", async () => {
		const { user } = await setup({
			state: dns01({ resolvers: [resolver()], wildcards: [wildcard()] }),
			routes: {
				"DELETE /dns01/wildcards/:wildcardId": problem(404, {
					code: "not_found",
					detail: "No such wildcard",
				}),
			},
		});
		await ready();
		await user.click(
			screen.getByRole("button", { name: "Remove *.example.com" }),
		);
		expect(await screen.findByText("No such wildcard")).toBeInTheDocument();
	});

	it("needs a resolver before a wildcard can be requested", async () => {
		await setup();
		await ready();
		expect(
			screen.getByRole("combobox", {
				name: "Resolver for the wildcard certificate",
			}),
		).toBeDisabled();
		expect(screen.getByRole("button", { name: "Add wildcard" })).toBeDisabled();
	});

	it("keeps Add wildcard disabled until a domain and a resolver are given", async () => {
		const { api, user } = await setup({
			state: dns01({ resolvers: [resolver()] }),
		});
		await ready();
		const add = screen.getByRole("button", { name: "Add wildcard" });
		await user.type(
			screen.getByLabelText("Base domain of the wildcard certificate"),
			"   ",
		);
		await pick(user, "Resolver for the wildcard certificate", "Cloudflare");
		expect(add).toBeDisabled();
		await user.type(
			screen.getByLabelText("Base domain of the wildcard certificate"),
			"{Enter}",
		);
		expect(writes(api)).toEqual([]);
	});

	it("requests a wildcard: trimmed domain, chosen resolver, base domain included, then clears the field", async () => {
		let state = dns01({ resolvers: [resolver()] });
		const { api, user } = await setup({
			routes: {
				"GET /servers/:serverId/dns01": () => Response.json(state),
				"POST /servers/:serverId/dns01/wildcards": () => {
					state = dns01({
						resolvers: [resolver()],
						wildcards: [wildcard({ change: "add" })],
					});
					return Response.json({}, { status: 201 });
				},
			},
		});
		await ready();
		const domain = screen.getByLabelText(
			"Base domain of the wildcard certificate",
		);
		await user.type(domain, "  example.com ");
		await pick(user, "Resolver for the wildcard certificate", "Cloudflare");
		const add = screen.getByRole("button", { name: "Add wildcard" });
		expect(add).toBeEnabled();
		await user.click(add);

		expect(
			await screen.findByText("*.example.com, example.com"),
		).toBeInTheDocument();
		expect(writes(api)[0]).toMatchObject({
			method: "POST",
			path: `/servers/${SERVER}/dns01/wildcards`,
			body: {
				domain: "example.com",
				provider: "cloudflare",
				includeApex: true,
			},
		});
		expect(domain).toHaveValue("");
		expect(
			(await screen.findAllByText("Wildcard certificate staged")).length,
		).toBeGreaterThan(0);
	});

	it("leaves the base domain out of the request when its switch is turned off", async () => {
		const { api, user } = await setup({
			state: dns01({ resolvers: [resolver()] }),
			routes: {
				"POST /servers/:serverId/dns01/wildcards": {
					status: 201,
					body: dns01(),
				},
			},
		});
		await ready();
		await user.type(
			screen.getByLabelText("Base domain of the wildcard certificate"),
			"example.com{Enter}",
		);
		expect(writes(api)).toEqual([]);
		await pick(user, "Resolver for the wildcard certificate", "Cloudflare");
		await user.click(
			screen.getByRole("switch", { name: "Include the base domain" }),
		);
		await user.click(screen.getByRole("button", { name: "Add wildcard" }));
		await waitFor(() => expect(writes(api)).toHaveLength(1));
		expect(writes(api)[0]?.body).toMatchObject({ includeApex: false });
	});

	it("submits with Enter and keeps the domain when the request fails", async () => {
		const { api, user } = await setup({
			state: dns01({ resolvers: [resolver()] }),
			routes: {
				"POST /servers/:serverId/dns01/wildcards": problem(409, {
					code: "conflict",
					detail: "A wildcard for example.com exists",
				}),
			},
		});
		await ready();
		await pick(user, "Resolver for the wildcard certificate", "Cloudflare");
		const domain = screen.getByLabelText(
			"Base domain of the wildcard certificate",
		);
		await user.type(domain, "example.com{Enter}");
		expect(
			await screen.findByText("A wildcard for example.com exists"),
		).toBeInTheDocument();
		expect(writes(api)).toHaveLength(1);
		expect(domain).toHaveValue("example.com");
	});

	it("offers only resolvers that are not staged for removal, under their display names", async () => {
		const { user } = await setup({
			state: dns01({
				resolvers: [
					resolver(),
					resolver({
						provider: "hetzner",
						providerName: "Hetzner",
						change: "remove",
					}),
				],
			}),
		});
		await ready();
		await user.click(
			screen.getByRole("combobox", {
				name: "Resolver for the wildcard certificate",
			}),
		);
		const options = await screen.findAllByRole("option");
		expect(options.map((o) => o.textContent)).toEqual(["Cloudflare"]);
	});
});
