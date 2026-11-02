import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ServerCertificate } from "#/api/gen/types.gen";
import { PLAIN_DOMAIN_RULE, type ServiceDomain } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import type { DomainStaged } from "#/features/proxy/lib/staged";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DomainOptions } from "./domain-options";

function makeDomain(overrides: Partial<ServiceDomain> = {}): ServiceDomain {
	return {
		...PLAIN_DOMAIN_RULE,
		id: "dom-1",
		hostname: "app.example.com",
		targetPort: 80,
		component: "",
		process: "",
		generated: false,
		status: "active",
		statusMessage: null,
		dnsOk: null,
		dnsCheckedAt: null,
		...overrides,
	};
}

const clean: DomainStaged = { added: false, buckets: new Set() };

function cert(overrides: Partial<ServerCertificate> = {}): ServerCertificate {
	return {
		appliedAt: null,
		change: "none",
		createdAt: "2026-01-01T00:00:00Z",
		daysToExpiry: 90,
		domains: ["*.example.com"],
		fingerprint: "ab",
		id: "cert-1",
		issuer: "Test CA",
		name: "wildcard",
		notAfter: "2027-01-01T00:00:00Z",
		notBefore: "2026-01-01T00:00:00Z",
		serverId: "server-1",
		status: "active",
		statusMessage: "",
		...overrides,
	};
}

interface Call {
	ifMatch: string | null;
	body: unknown;
}

function problem(status: number, code: string, detail: string) {
	return { status, body: { status, code, title: code, detail } };
}

/** The routes every render needs; `patches` records each PATCH's headers and body. */
function routes(
	patches: Call[] = [],
	extra: Parameters<typeof stubApi>[0] = {},
	certificates: ServerCertificate[] = [],
) {
	return stubApi({
		"GET /servers/:serverId/certificates": { body: { items: certificates } },
		"PATCH /domains/:domainId": async (request) => {
			patches.push({
				ifMatch: request.headers.get("If-Match"),
				body: await request.clone().json(),
			});
			return Response.json({});
		},
		...extra,
	});
}

async function renderOptions(
	domain: ServiceDomain,
	{
		staged = clean,
		disabled = false,
		serverId = "server-1" as string | null,
		generation = 4,
	}: {
		staged?: DomainStaged;
		disabled?: boolean;
		serverId?: string | null;
		generation?: number;
	} = {},
) {
	const service = makeService({
		id: "svc-1",
		serverId,
		generation,
		appliedGeneration: 2,
		domains: [domain],
	});
	return renderWithProviders(
		<DomainOptions
			service={service}
			domain={domain}
			staged={staged}
			disabled={disabled}
		/>,
	);
}

const user = () => userEvent.setup({ delay: null });
const panel = (name: string) => screen.getByRole("region", { name });
const header = (name: string) =>
	within(panel(name)).getByRole("button", { name: new RegExp(`^${name}`) });

async function open(name: string) {
	await user().click(header(name));
	expect(header(name)).toHaveAttribute("aria-expanded", "true");
	return panel(name);
}

async function replace(input: HTMLElement, text: string) {
	const u = user();
	await u.clear(input);
	if (text !== "") await u.type(input, text);
}

describe("DomainOptions: headers of the five groups", () => {
	it("lists Access, Headers & CORS, Redirects, Limits and TLS, all collapsed", async () => {
		routes();
		await renderOptions(makeDomain());
		for (const name of [
			"Access",
			"Headers & CORS",
			"Redirects",
			"Limits",
			"TLS",
		]) {
			expect(header(name)).toHaveAttribute("aria-expanded", "false");
		}
	});

	it("summarises what each group has set", async () => {
		routes();
		await renderOptions(
			makeDomain({
				authUsers: ["alice"],
				tlsMode: "custom",
				options: {
					basicAuth: true,
					access: { allowCidrs: ["10.0.0.0/8"] },
					requestHeaders: [{ name: "X-A", value: "1" }],
					noindex: true,
					redirects: [{ regex: "^/a", replacement: "/b" }],
					rateLimit: { average: 20, burst: 30, periodSeconds: 2 },
					compress: true,
					forceHttps: false,
				},
			}),
		);
		expect(header("Access")).toHaveTextContent(
			"basic auth · 1 user(s) · 1 allowed network(s)",
		);
		expect(header("Headers & CORS")).toHaveTextContent("1 header(s) · noindex");
		expect(header("Redirects")).toHaveTextContent("1 rule(s)");
		expect(header("Limits")).toHaveTextContent("20 req / 2s · compression");
		expect(header("TLS")).toHaveTextContent("custom · HTTP allowed");
	});

	it("badges only the groups with staged changes", async () => {
		routes();
		await renderOptions(makeDomain(), {
			staged: { added: false, buckets: new Set(["limits", "rule"]) },
		});
		expect(within(panel("Limits")).getByText("Staged")).toBeInTheDocument();
		expect(within(panel("Access")).queryByText("Staged")).toBeNull();
		expect(within(panel("TLS")).queryByText("Staged")).toBeNull();
	});

	it("badges every group of a domain that is itself staged as new", async () => {
		routes();
		await renderOptions(makeDomain(), {
			staged: { added: true, buckets: new Set() },
		});
		expect(screen.getAllByText("Staged")).toHaveLength(5);
		expect(within(panel("TLS")).getByText("Staged")).toHaveAttribute(
			"title",
			"Staged: generation 4, deployed 2",
		);
	});
});

describe("DomainOptions: saving one group", () => {
	it("saves only the group's fields on top of the saved options, with If-Match", async () => {
		const patches: Call[] = [];
		const api = routes(patches);
		await renderOptions(
			makeDomain({
				options: { compress: true, noindex: true, securityHeaders: "basic" },
			}),
		);
		const limits = await open("Limits");
		expect(within(limits).queryByRole("button", { name: /^Save/ })).toBeNull();
		await user().click(
			within(limits).getByRole("switch", { name: "Rate limit" }),
		);
		expect(within(limits).getByText("Unsaved")).toBeInTheDocument();
		await user().click(
			within(limits).getByRole("button", { name: "Save limits" }),
		);
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0]).toEqual({
			ifMatch: '"4"',
			body: {
				options: {
					compress: true,
					noindex: true,
					securityHeaders: "basic",
					rateLimit: { average: 50, burst: 100, periodSeconds: 1 },
				},
			},
		});
		expect(
			await screen.findByText("Domain settings saved"),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(within(panel("Limits")).queryByText("Unsaved")).toBeNull(),
		);
		expect(api.unmatched).toEqual([]);
	});

	it("keeps another group's unsaved edit out of the save and still unsaved after", async () => {
		const patches: Call[] = [];
		routes(patches);
		await renderOptions(makeDomain());
		const headers = await open("Headers & CORS");
		await user().click(
			within(headers).getByRole("switch", {
				name: "Ask search engines not to index",
			}),
		);
		const limits = await open("Limits");
		await user().click(
			within(limits).getByRole("switch", { name: "Compress responses" }),
		);
		await user().click(
			within(limits).getByRole("button", { name: "Save limits" }),
		);
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0]?.body).toEqual({ options: { compress: true } });
		await waitFor(() =>
			expect(within(panel("Limits")).queryByText("Unsaved")).toBeNull(),
		);
		expect(
			within(panel("Headers & CORS")).getByText("Unsaved"),
		).toBeInTheDocument();
		expect(
			within(panel("Headers & CORS")).getByRole("switch", {
				name: "Ask search engines not to index",
			}),
		).toBeChecked();
	});

	it("discards a group's edits without calling the API", async () => {
		const api = routes();
		await renderOptions(makeDomain());
		const limits = await open("Limits");
		const compress = within(limits).getByRole("switch", {
			name: "Compress responses",
		});
		await user().click(compress);
		expect(compress).toBeChecked();
		await user().click(within(limits).getByRole("button", { name: "Discard" }));
		expect(compress).not.toBeChecked();
		expect(within(limits).queryByText("Unsaved")).toBeNull();
		expect(api.calls.filter((c) => c.method !== "GET")).toEqual([]);
	});

	it("blocks Save while a field of the group is invalid", async () => {
		const patches: Call[] = [];
		routes(patches);
		await renderOptions(makeDomain());
		const limits = await open("Limits");
		await user().click(
			within(limits).getByRole("switch", { name: "Rate limit" }),
		);
		await replace(within(limits).getByLabelText("Requests"), "0");
		expect(within(limits).getByText("At least 1.")).toBeInTheDocument();
		const save = within(limits).getByRole("button", { name: "Save limits" });
		expect(save).toBeDisabled();
		await user().click(save);
		expect(patches).toEqual([]);
	});

	it("one group's invalid field does not block another group's save", async () => {
		const patches: Call[] = [];
		routes(patches);
		await renderOptions(makeDomain());
		const limits = await open("Limits");
		await user().click(
			within(limits).getByRole("switch", { name: "Rate limit" }),
		);
		await replace(within(limits).getByLabelText("Requests"), "0");
		const headers = await open("Headers & CORS");
		await user().click(
			within(headers).getByRole("switch", {
				name: "Ask search engines not to index",
			}),
		);
		await user().click(
			within(headers).getByRole("button", { name: "Save headers & cors" }),
		);
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0]?.body).toEqual({ options: { noindex: true } });
	});

	it("saves redirect rules in order", async () => {
		const patches: Call[] = [];
		routes(patches);
		await renderOptions(makeDomain());
		const redirects = await open("Redirects");
		const add = within(redirects).getByRole("button", { name: "Add rule" });
		await user().click(add);
		await user().click(add);
		const regexes = within(redirects).getAllByLabelText("Regex");
		const replacements = within(redirects).getAllByLabelText("Replacement");
		await user().type(regexes[0] as HTMLElement, "^/one");
		await user().type(replacements[0] as HTMLElement, "/1");
		await user().type(regexes[1] as HTMLElement, "^/two");
		await user().type(replacements[1] as HTMLElement, "/2");
		await user().click(
			within(redirects).getAllByRole("switch", {
				name: "Permanent",
			})[1] as HTMLElement,
		);
		await user().click(
			within(redirects).getByRole("button", { name: "Save redirects" }),
		);
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0]?.body).toEqual({
			options: {
				redirects: [
					{ regex: "^/one", replacement: "/1" },
					{ regex: "^/two", replacement: "/2", permanent: true },
				],
			},
		});
	});
});

describe("DomainOptions: access users", () => {
	it("stores users before turning basic auth on, then PATCHes with the regenerated generation", async () => {
		const patches: Call[] = [];
		const api = routes(patches, {
			"PUT /domains/:domainId/auth-users": {
				body: make("AuthUsersOutputBody"),
			},
			"GET /services/:serviceId": {
				body: make("ServiceView", { generation: 9 }),
			},
		});
		await renderOptions(makeDomain());
		const access = await open("Access");
		await user().click(
			within(access).getByRole("switch", {
				name: "Ask for a username and password",
			}),
		);
		expect(
			within(access).getByText(
				"Add at least one user before turning basic auth on.",
			),
		).toBeInTheDocument();
		expect(
			within(access).getByRole("button", { name: "Save access" }),
		).toBeDisabled();
		await user().click(
			within(access).getByRole("button", { name: "Add user" }),
		);
		await user().type(within(access).getByLabelText("User name 1"), "carol");
		await user().type(
			within(access).getByLabelText("Password for user 1"),
			"pw",
		);
		await user().click(
			within(access).getByRole("button", { name: "Save access" }),
		);
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(
			api.calls
				.filter((c) => !c.path.endsWith("/certificates"))
				.map((c) => `${c.method} ${c.path}`),
		).toEqual([
			"PUT /domains/dom-1/auth-users",
			"GET /services/svc-1",
			"PATCH /domains/dom-1",
		]);
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual([
			{ username: "carol", password: "pw" },
		]);
		expect(patches[0]).toEqual({
			ifMatch: '"9"',
			body: { options: { basicAuth: true } },
		});
	});

	it("turns basic auth off first and removes the users afterwards", async () => {
		const patches: Call[] = [];
		const api = routes(patches, {
			"PUT /domains/:domainId/auth-users": {
				body: make("AuthUsersOutputBody"),
			},
		});
		await renderOptions(
			makeDomain({ authUsers: ["alice", "bob"], options: { basicAuth: true } }),
		);
		const access = await open("Access");
		await user().click(
			within(access).getByRole("switch", {
				name: "Ask for a username and password",
			}),
		);
		await user().click(
			within(access).getByRole("button", { name: "Remove user bob" }),
		);
		await user().click(
			within(access).getByRole("button", { name: "Save access" }),
		);
		await waitFor(() =>
			expect(api.calls.filter((c) => c.method !== "GET")).toHaveLength(2),
		);
		const writes = api.calls.filter((c) => c.method !== "GET");
		expect(writes.map((c) => `${c.method} ${c.path}`)).toEqual([
			"PATCH /domains/dom-1",
			"PUT /domains/dom-1/auth-users",
		]);
		expect(patches[0]?.ifMatch).toBe('"4"');
		expect(writes[0]?.body).toEqual({ options: {} });
		expect(writes[1]?.body).toEqual([{ username: "alice" }]);
	});

	it("only a password change sends the PUT without a password-less user", async () => {
		const api = routes([], {
			"PUT /domains/:domainId/auth-users": {
				body: make("AuthUsersOutputBody"),
			},
		});
		await renderOptions(makeDomain({ authUsers: ["alice"] }));
		const access = await open("Access");
		await user().type(
			within(access).getByLabelText("Password for user 1"),
			"fresh",
		);
		await user().click(
			within(access).getByRole("button", { name: "Save access" }),
		);
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "PUT")).toBe(true),
		);
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual([
			{ username: "alice", password: "fresh" },
		]);
	});

	it("shows an IP allow-list save and keeps basic-auth users untouched", async () => {
		const patches: Call[] = [];
		// Basic auth stays on, so the save reads the generation again first.
		const api = routes(patches, {
			"GET /services/:serviceId": {
				body: make("ServiceView", { generation: 6 }),
			},
		});
		await renderOptions(
			makeDomain({ authUsers: ["alice"], options: { basicAuth: true } }),
		);
		const access = await open("Access");
		await user().click(
			within(access).getByRole("switch", { name: "Restrict by IP address" }),
		);
		await user().click(
			within(access).getByRole("button", { name: "Add network" }),
		);
		await user().type(
			within(access).getByLabelText("Allowed network 1"),
			"10.0.0.0/8",
		);
		await user().click(
			within(access).getByRole("button", { name: "Save access" }),
		);
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0]).toEqual({
			ifMatch: '"6"',
			body: {
				options: { basicAuth: true, access: { allowCidrs: ["10.0.0.0/8"] } },
			},
		});
		expect(api.calls.some((c) => c.method === "PUT")).toBe(false);
	});
});

describe("DomainOptions: TLS", () => {
	it("needs a pinned certificate in custom mode and sends tlsMode with it", async () => {
		const patches: Call[] = [];
		routes(patches, {}, [cert()]);
		await renderOptions(makeDomain());
		const tls = await open("TLS");
		await user().click(
			within(tls).getByRole("button", { name: "Custom certificate" }),
		);
		expect(
			within(tls).getByText("Pick the certificate to serve."),
		).toBeInTheDocument();
		expect(
			within(tls).getByRole("button", { name: "Save tls" }),
		).toBeDisabled();
		await user().click(within(tls).getByRole("combobox"));
		await user().click(await screen.findByRole("option", { name: /wildcard/ }));
		expect(
			within(tls).queryByText("Pick the certificate to serve."),
		).toBeNull();
		await user().click(within(tls).getByRole("button", { name: "Save tls" }));
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0]?.body).toEqual({
			tlsMode: "custom",
			certificateId: "cert-1",
			options: {},
		});
	});

	it("clears the certificate when moving a pinned domain back to automatic", async () => {
		const patches: Call[] = [];
		routes(patches, {}, [cert()]);
		await renderOptions(
			makeDomain({ tlsMode: "custom", certificateId: "cert-1" }),
		);
		const tls = await open("TLS");
		await user().click(within(tls).getByRole("button", { name: "Automatic" }));
		await user().click(within(tls).getByRole("button", { name: "Save tls" }));
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0]?.body).toEqual({
			tlsMode: "auto",
			certificateId: "",
			options: {},
		});
	});

	it("turns the HTTP redirect off with the TLS group's own option", async () => {
		const patches: Call[] = [];
		routes(patches);
		await renderOptions(makeDomain({ options: { compress: true } }));
		const tls = await open("TLS");
		await user().click(
			within(tls).getByRole("switch", { name: "Redirect HTTP to HTTPS" }),
		);
		await user().click(within(tls).getByRole("button", { name: "Save tls" }));
		await waitFor(() => expect(patches).toHaveLength(1));
		expect(patches[0]?.body).toEqual({
			tlsMode: "auto",
			certificateId: "",
			options: { compress: true, forceHttps: false },
		});
	});

	it("discarding restores the saved mode", async () => {
		routes();
		await renderOptions(makeDomain());
		const tls = await open("TLS");
		await user().click(
			within(tls).getByRole("button", { name: "Off (HTTP only)" }),
		);
		expect(within(tls).getByText("Unsaved")).toBeInTheDocument();
		await user().click(within(tls).getByRole("button", { name: "Discard" }));
		expect(
			within(tls).getByRole("button", { name: "Automatic" }),
		).toHaveAttribute("aria-pressed", "true");
	});

	it("explains there are no certificates for a service without a server", async () => {
		const api = routes();
		await renderOptions(makeDomain(), { serverId: null });
		const tls = await open("TLS");
		await user().click(
			within(tls).getByRole("button", { name: "Custom certificate" }),
		);
		expect(
			within(tls).getByText(/No certificate is uploaded on this server yet/),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});
});

describe("DomainOptions: failures", () => {
	it("puts an options error next to the field it names, in that group only", async () => {
		routes([], {
			"PATCH /domains/:domainId": problem(
				422,
				"invalid_options",
				"rateLimit average is too high",
			),
		});
		await renderOptions(makeDomain());
		const limits = await open("Limits");
		await user().click(
			within(limits).getByRole("switch", { name: "Rate limit" }),
		);
		await user().click(
			within(limits).getByRole("button", { name: "Save limits" }),
		);
		expect(
			await within(limits).findByText("rateLimit average is too high"),
		).toBeInTheDocument();
		expect(within(limits).getByLabelText("Requests")).toBeInvalid();
		expect(screen.getAllByText("rateLimit average is too high")).toHaveLength(
			1,
		);
		// The edit survives so it can be fixed.
		expect(
			within(limits).getByRole("switch", { name: "Rate limit" }),
		).toBeChecked();
	});

	it("shows an unmapped failure in the group's footer", async () => {
		routes([], {
			"PATCH /domains/:domainId": problem(
				500,
				"boom",
				"The control plane exploded.",
			),
		});
		await renderOptions(makeDomain());
		const limits = await open("Limits");
		await user().click(
			within(limits).getByRole("switch", { name: "Compress responses" }),
		);
		await user().click(
			within(limits).getByRole("button", { name: "Save limits" }),
		);
		expect(
			await within(limits).findByText("The control plane exploded."),
		).toBeInTheDocument();
		expect(
			within(limits).getByRole("button", { name: "Save limits" }),
		).toBeEnabled();
	});

	it("routes a basic-auth rejection to the Access group's switch", async () => {
		routes([], {
			"PATCH /domains/:domainId": problem(
				422,
				"basic_auth_needs_users",
				"Basic auth needs at least one user.",
			),
			"PUT /domains/:domainId/auth-users": {
				body: make("AuthUsersOutputBody"),
			},
			"GET /services/:serviceId": {
				body: make("ServiceView", { generation: 5 }),
			},
		});
		await renderOptions(makeDomain());
		const access = await open("Access");
		await user().click(
			within(access).getByRole("switch", {
				name: "Ask for a username and password",
			}),
		);
		await user().click(
			within(access).getByRole("button", { name: "Add user" }),
		);
		await user().type(within(access).getByLabelText("User name 1"), "carol");
		await user().type(
			within(access).getByLabelText("Password for user 1"),
			"pw",
		);
		await user().click(
			within(access).getByRole("button", { name: "Save access" }),
		);
		expect(
			await within(access).findByText("Basic auth needs at least one user."),
		).toBeInTheDocument();
	});

	it("shows a certificate rejection under the TLS picker", async () => {
		routes(
			[],
			{
				"PATCH /domains/:domainId": problem(
					422,
					"certificate_mismatch",
					"The certificate does not cover app.example.com.",
				),
			},
			[cert()],
		);
		await renderOptions(
			makeDomain({ tlsMode: "custom", certificateId: "cert-1" }),
		);
		const tls = await open("TLS");
		await user().click(
			within(tls).getByRole("switch", { name: "Redirect HTTP to HTTPS" }),
		);
		await user().click(within(tls).getByRole("button", { name: "Save tls" }));
		expect(
			await within(tls).findByText(
				"The certificate does not cover app.example.com.",
			),
		).toBeInTheDocument();
	});

	it("drops the group's edits when someone else changed the service (412)", async () => {
		routes([], {
			"PATCH /domains/:domainId": problem(
				412,
				"generation_conflict",
				"The service changed.",
			),
		});
		await renderOptions(makeDomain());
		const limits = await open("Limits");
		const compress = within(limits).getByRole("switch", {
			name: "Compress responses",
		});
		await user().click(compress);
		await user().click(
			within(limits).getByRole("button", { name: "Save limits" }),
		);
		await waitFor(() => expect(compress).not.toBeChecked());
		expect(within(limits).queryByText("Unsaved")).toBeNull();
		expect(screen.queryByText("The service changed.")).toBeNull();
	});
});

describe("DomainOptions: agent too old", () => {
	it("cannot be edited in any group", async () => {
		routes();
		await renderOptions(makeDomain(), { disabled: true });
		const limits = await open("Limits");
		const compress = within(limits).getByRole("switch", {
			name: "Compress responses",
		});
		await user().click(compress);
		expect(compress).not.toBeChecked();
		expect(within(limits).queryByText("Unsaved")).toBeNull();
		const headers = await open("Headers & CORS");
		expect(
			within(headers).getAllByRole("button", { name: "Add header" })[0],
		).toBeDisabled();
		const redirects = await open("Redirects");
		expect(
			within(redirects).getByRole("button", { name: "Add rule" }),
		).toBeDisabled();
		const access = await open("Access");
		expect(
			within(access).getByRole("button", { name: "Add user" }),
		).toBeDisabled();
	});
});
