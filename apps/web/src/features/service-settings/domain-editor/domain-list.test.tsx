import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Server } from "#/api/gen/types.gen";
import {
	PLAIN_DOMAIN_RULE,
	type Service,
	type ServiceDomain,
} from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { type ApiRoutes, stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { AddDomainForm, DomainList } from "./domain-list";

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

/** The server the service sits on: id and name are what `makeService` points at. */
const server = (overrides: Overrides<Server> = {}) =>
	make("Server", {
		id: "server-1",
		name: "hetzner-01",
		publicIp: "203.0.113.5",
		proxy: { status: "ok", v2: true, generation: 1, appliedGeneration: 1 },
		...overrides,
	});

const dns01 = (
	providers: { provider: string; providerName: string }[] = [],
) => ({
	body: make("ServerDNS01", {
		resolvers: providers.map((p) =>
			make("DNSResolverView", {
				...p,
				credentialId: "cred-1",
				credentialName: "prod",
			}),
		),
	}),
});

/** Routes every section of the list reads, so a test only adds what it cares about. */
const baseRoutes = (
	extra: ApiRoutes = {},
	servers: Server[] = [server()],
): ApiRoutes => ({
	"GET /servers": { body: make("PageServer", { items: servers }) },
	"GET /integrations/cloudflare": {
		body: make("CloudflareConnectionView", { configured: false }),
	},
	"GET /servers/:serverId/dns01": dns01(),
	"GET /me": { body: make("MeBody") },
	...extra,
});

const countOf = (
	api: { calls: { method: string; path: string }[] },
	method: string,
	path: string,
) => api.calls.filter((c) => c.method === method && c.path === path).length;

/** Selects and hold keys are slow in happy-dom, more so on a busy machine. */
const SLOW = 20_000;

describe("DomainList", () => {
	test("shows no list at all for a service without domains", async () => {
		stubApi(baseRoutes());
		await renderWithProviders(<DomainList service={makeService()} />);
		expect(
			screen.queryByRole("list", { name: "Domains" }),
		).not.toBeInTheDocument();
	});

	test("lists every domain of the service with its path prefix", async () => {
		stubApi(baseRoutes());
		const service = makeService({
			domains: [
				makeDomain({ id: "dom-1", hostname: "app.example.com" }),
				makeDomain({
					id: "dom-2",
					hostname: "api.example.com",
					pathPrefix: "/v1",
				}),
			],
		});
		await renderWithProviders(<DomainList service={service} />);
		const list = screen.getByRole("list", { name: "Domains" });
		expect(within(list).getAllByRole("listitem")).toHaveLength(2);
		expect(
			within(list).getByRole("link", { name: "app.example.com" }),
		).toBeInTheDocument();
		expect(
			within(list).getByRole("link", { name: "api.example.com/v1" }),
		).toBeInTheDocument();
	});

	test("uses the public IP of the service's server in the DNS hint", async () => {
		stubApi(baseRoutes({}, [server({ publicIp: "198.51.100.77" })]));
		const service = makeService({ domains: [makeDomain({ dnsOk: false })] });
		await renderWithProviders(<DomainList service={service} />);
		expect(
			await screen.findByText(
				"Point an A record for app.example.com to 198.51.100.77.",
			),
		).toBeInTheDocument();
	});

	test("marks a domain staged when the pending changes say it was added", async () => {
		const api = stubApi(
			baseRoutes({
				"GET /services/:serviceId/pending-changes": {
					body: {
						items: [
							{ field: "domains.app.example.com", from: "", to: "added" },
							{ field: "domains.other.example.com", from: "", to: "added" },
						],
					},
				},
			}),
		);
		const service = makeService({
			id: "svc-1",
			hasStagedChanges: true,
			domains: [
				makeDomain({ id: "dom-1", hostname: "app.example.com" }),
				makeDomain({ id: "dom-2", hostname: "api.example.com" }),
			],
		});
		await renderWithProviders(<DomainList service={service} />);
		expect(await screen.findAllByText("New")).toHaveLength(1);
		const items = screen.getAllByRole("listitem");
		expect(
			within(items[0] as HTMLElement).getByText("New"),
		).toBeInTheDocument();
		expect(
			within(items[1] as HTMLElement).queryByText("New"),
		).not.toBeInTheDocument();
		expect(
			api.calls.some((c) => c.path === "/services/svc-1/pending-changes"),
		).toBe(true);
	});

	test("does not read pending changes of a service without staged changes", async () => {
		const api = stubApi(baseRoutes());
		const service = makeService({
			id: "svc-1",
			hasStagedChanges: false,
			domains: [makeDomain()],
		});
		await renderWithProviders(<DomainList service={service} />);
		await screen.findByRole("list", { name: "Domains" });
		await waitFor(() =>
			expect(countOf(api, "GET", "/servers")).toBeGreaterThan(0),
		);
		expect(api.calls.some((c) => c.path.endsWith("/pending-changes"))).toBe(
			false,
		);
	});

	test("an online server whose agent predates proxy v2 gets the upgrade banner", async () => {
		stubApi(
			baseRoutes({}, [
				server({
					proxy: {
						status: "ok",
						v2: false,
						generation: 0,
						appliedGeneration: 0,
					},
				}),
			]),
		);
		const service = makeService({ domains: [makeDomain()] });
		await renderWithProviders(<DomainList service={service} />);
		expect(
			await screen.findByText("The agent on hetzner-01 is too old for this"),
		).toBeInTheDocument();
	});

	test("an up-to-date agent shows no banner", async () => {
		const api = stubApi(baseRoutes());
		await renderWithProviders(
			<DomainList service={makeService({ domains: [makeDomain()] })} />,
		);
		await waitFor(() => expect(countOf(api, "GET", "/servers")).toBe(1));
		await screen.findByRole("list", { name: "Domains" });
		expect(screen.queryByTestId("agent-too-old")).not.toBeInTheDocument();
	});

	test("an offline server gets no banner either, since its agent version is unknown", async () => {
		const api = stubApi(
			baseRoutes({}, [
				server({
					status: "offline",
					proxy: { status: "", v2: false, generation: 0, appliedGeneration: 0 },
				}),
			]),
		);
		await renderWithProviders(
			<DomainList service={makeService({ domains: [makeDomain()] })} />,
		);
		await waitFor(() => expect(countOf(api, "GET", "/servers")).toBe(1));
		expect(screen.queryByTestId("agent-too-old")).not.toBeInTheDocument();
	});

	test("removing a domain asks first, then DELETEs it and refreshes the routes", async () => {
		const api = stubApi(
			baseRoutes({ "DELETE /domains/:domainId": { status: 204 } }),
		);
		const user = userEvent.setup();
		const service = makeService({
			domains: [
				makeDomain({ id: "dom-1", hostname: "app.example.com" }),
				makeDomain({
					id: "dom-2",
					hostname: "api.example.com",
					pathPrefix: "/v1",
				}),
			],
		});
		await renderWithProviders(<DomainList service={service} />);
		await waitFor(() => expect(countOf(api, "GET", "/servers")).toBe(1));
		await user.click(
			screen.getByRole("button", { name: "Remove api.example.com/v1" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Remove api.example.com/v1?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/The route is removed on the next deploy/),
		).toBeInTheDocument();
		expect(countOf(api, "DELETE", "/domains/dom-2")).toBe(0);
		await user.click(
			within(dialog).getByRole("button", { name: "Remove domain" }),
		);
		await waitFor(() =>
			expect(countOf(api, "DELETE", "/domains/dom-2")).toBe(1),
		);
		expect(api.calls.some((c) => c.path === "/domains/dom-1")).toBe(false);
		// The server list is on screen, so the invalidation reads it again.
		await waitFor(() =>
			expect(countOf(api, "GET", "/servers")).toBeGreaterThan(1),
		);
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
	});

	test("cancelling the removal keeps the domain and sends nothing", async () => {
		const api = stubApi(baseRoutes());
		const user = userEvent.setup();
		await renderWithProviders(
			<DomainList service={makeService({ domains: [makeDomain()] })} />,
		);
		await user.click(
			screen.getByRole("button", { name: "Remove app.example.com" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		expect(
			screen.getByRole("link", { name: "app.example.com" }),
		).toBeInTheDocument();
	});

	test("a removal the API refuses keeps the dialog open with the reason", async () => {
		stubApi(
			baseRoutes({
				"DELETE /domains/:domainId": problem(409, {
					detail: "The domain is the last route of a published status page",
					code: "domain_in_use",
				}),
			}),
		);
		const user = userEvent.setup();
		await renderWithProviders(
			<DomainList service={makeService({ domains: [makeDomain()] })} />,
		);
		await user.click(
			screen.getByRole("button", { name: "Remove app.example.com" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Remove domain" }),
		);
		expect(
			await within(dialog).findByText(
				"The domain is the last route of a published status page",
			),
		).toBeInTheDocument();
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
	});
});

const stackService = (): Service =>
	makeService({
		kind: "stack",
		serverId: "server-1",
		config: {
			...makeService().config,
			stack: {
				components: [
					{ name: "web", index: 0, ports: [3000] },
					{ name: "worker", index: 1, ports: [] },
				],
			},
		} as unknown as Service["config"],
	});

const processService = (processes: unknown[]): Service =>
	makeService({ processes: processes as unknown as Service["processes"] });

async function showForm(
	service: Service,
	routes: ApiRoutes = {},
	props: { disabled?: boolean; disabledReason?: string } = {},
) {
	const api = stubApi(
		baseRoutes({
			"POST /services/:serviceId/domains": {
				status: 201,
				body: make("DomainView"),
			},
			...routes,
		}),
	);
	await renderWithProviders(
		<AddDomainForm
			service={service}
			disabled={props.disabled ?? false}
			{...(props.disabledReason
				? { disabledReason: props.disabledReason }
				: {})}
		/>,
	);
	return api;
}

const hostnameInput = () => screen.getByRole("textbox", { name: "New domain" });
const addButton = () => screen.getByRole("button", { name: "Add domain" });
const posts = (api: {
	calls: { method: string; path: string; body: unknown }[];
}) => api.calls.filter((c) => c.method === "POST");

describe("AddDomainForm", () => {
	test("Add domain stays off until a hostname is typed", async () => {
		const user = userEvent.setup();
		await showForm(makeService());
		expect(addButton()).toBeDisabled();
		await user.type(hostnameInput(), "   ");
		expect(addButton()).toBeDisabled();
		await user.type(hostnameInput(), "app.example.com");
		expect(addButton()).toBeEnabled();
	});

	test("adds the normalised hostname, clears the field and toasts that it is staged", async () => {
		const user = userEvent.setup();
		const api = await showForm(makeService({ id: "svc-1" }));
		await user.type(hostnameInput(), "  App.Example.COM.  ");
		await user.click(addButton());
		expect(await screen.findByText("Domain added")).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		const [post] = posts(api);
		expect(posts(api)).toHaveLength(1);
		expect(post?.path).toBe("/services/svc-1/domains");
		expect(post?.body).toEqual({ hostname: "app.example.com" });
		expect(hostnameInput()).toHaveValue("");
	});

	test("Enter in the field submits the form", async () => {
		const user = userEvent.setup();
		const api = await showForm(makeService({ id: "svc-1" }));
		await user.type(hostnameInput(), "app.example.com{Enter}");
		await waitFor(() => expect(posts(api)).toHaveLength(1));
		expect(posts(api)[0]?.body).toEqual({ hostname: "app.example.com" });
	});

	test("Enter on an empty field sends nothing", async () => {
		const user = userEvent.setup();
		const api = await showForm(makeService());
		await user.type(hostnameInput(), "{Enter}");
		expect(posts(api)).toHaveLength(0);
	});

	test("an invalid-domain answer shows the API message and keeps what was typed", async () => {
		const user = userEvent.setup();
		await showForm(makeService(), {
			"POST /services/:serviceId/domains": problem(422, {
				detail: "hostname is not a valid domain name",
				code: "invalid_hostname",
			}),
		});
		await user.type(hostnameInput(), "not a host");
		await user.click(addButton());
		expect(
			await screen.findByText("hostname is not a valid domain name"),
		).toBeInTheDocument();
		expect(hostnameInput()).toHaveValue("not a host");
		expect(screen.queryByText("Domain added")).not.toBeInTheDocument();
	});

	test("a hostname already in use shows the conflict", async () => {
		const user = userEvent.setup();
		await showForm(makeService(), {
			"POST /services/:serviceId/domains": problem(409, {
				detail: "app.example.com is already routed by another service",
				code: "domain_taken",
			}),
		});
		await user.type(hostnameInput(), "app.example.com");
		await user.click(addButton());
		expect(
			await screen.findByText(
				"app.example.com is already routed by another service",
			),
		).toBeInTheDocument();
	});

	test("an agent too old to stage domains shows the upgrade banner instead of the raw error", async () => {
		const user = userEvent.setup();
		await showForm(makeService({ serverId: "server-1" }), {
			"POST /services/:serviceId/domains": problem(422, {
				detail: "agent too old for proxy v2",
				code: "agent_too_old",
			}),
		});
		await user.type(hostnameInput(), "app.example.com");
		await user.click(addButton());
		expect(
			await screen.findByText("The agent on hetzner-01 is too old for this"),
		).toBeInTheDocument();
		expect(
			screen.queryByText("agent too old for proxy v2"),
		).not.toBeInTheDocument();
	});

	test("a disabled form locks the field and the button and explains why", async () => {
		await showForm(
			makeService(),
			{},
			{ disabled: true, disabledReason: "Deploy first" },
		);
		expect(hostnameInput()).toBeDisabled();
		expect(hostnameInput()).toHaveAttribute("title", "Deploy first");
		expect(addButton()).toBeDisabled();
	});

	test("offers to route and redirect the www counterpart of an apex domain", async () => {
		const user = userEvent.setup();
		const api = await showForm(makeService({ id: "svc-1" }));
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
		await user.type(hostnameInput(), "example.com");
		const toggle = await screen.findByRole("switch");
		expect(screen.getByText(/Also route/).textContent).toContain(
			"www.example.com",
		);
		expect(toggle).not.toBeChecked();
		await user.click(toggle);
		expect(toggle).toBeChecked();
		await user.click(addButton());
		await waitFor(() => expect(posts(api)).toHaveLength(1));
		expect(posts(api)[0]?.body).toEqual({
			hostname: "example.com",
			wwwRedirect: true,
		});
	});

	test("the www option resets after a successful add", async () => {
		const user = userEvent.setup();
		await showForm(makeService());
		await user.type(hostnameInput(), "example.com");
		await user.click(await screen.findByRole("switch"));
		await user.click(addButton());
		await screen.findByText("Domain added");
		await user.type(hostnameInput(), "example.org");
		expect(await screen.findByRole("switch")).not.toBeChecked();
	});

	test("a subdomain has no www counterpart to offer", async () => {
		const user = userEvent.setup();
		await showForm(makeService());
		await user.type(hostnameInput(), "app.example.com");
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
	});

	test(
		"a wildcard hostname asks how its certificate is obtained and sends the resolver",
		async () => {
			const user = userEvent.setup();
			const api = await showForm(makeService({ id: "svc-1" }), {
				"GET /servers/:serverId/dns01": dns01([
					{ provider: "route53", providerName: "Amazon Route 53" },
				]),
			});
			expect(
				screen.queryByRole("combobox", {
					name: "Certificate resolver for the wildcard domain",
				}),
			).not.toBeInTheDocument();
			await user.type(hostnameInput(), "*.example.com");
			const select = await screen.findByRole("combobox", {
				name: "Certificate resolver for the wildcard domain",
			});
			expect(select).toHaveTextContent("Covering wildcard certificate");
			await waitFor(() =>
				expect(
					api.calls.some((c) => c.path === "/servers/server-1/dns01"),
				).toBe(true),
			);
			await user.click(select);
			await user.click(
				await screen.findByRole("option", { name: "DNS-01 · Amazon Route 53" }),
			);
			await user.click(addButton());
			await waitFor(() => expect(posts(api)).toHaveLength(1));
			expect(posts(api)[0]?.body).toEqual({
				hostname: "*.example.com",
				dnsResolver: "route53",
			});
		},
		SLOW,
	);

	test("a wildcard left on the covering certificate sends no resolver", async () => {
		const user = userEvent.setup();
		const api = await showForm(makeService());
		await user.type(hostnameInput(), "*.example.com");
		await user.click(addButton());
		await waitFor(() => expect(posts(api)).toHaveLength(1));
		expect(posts(api)[0]?.body).toEqual({ hostname: "*.example.com" });
	});
});

describe("AddDomainForm for a stack", () => {
	test("needs a component before a domain can be added and says what is missing", async () => {
		const user = userEvent.setup();
		await showForm(stackService());
		await user.type(hostnameInput(), "app.example.com");
		expect(addButton()).toBeDisabled();
		expect(addButton()).toHaveAttribute(
			"title",
			"Pick the component the domain serves.",
		);
	});

	test(
		"sends the chosen component, using its single declared port by default",
		async () => {
			const user = userEvent.setup();
			const api = await showForm(stackService());
			await user.click(
				screen.getByRole("combobox", { name: "Component the domain serves" }),
			);
			await user.click(screen.getByRole("option", { name: "web" }));
			await user.type(hostnameInput(), "app.example.com");
			expect(addButton()).toBeEnabled();
			await user.click(addButton());
			await waitFor(() => expect(posts(api)).toHaveLength(1));
			expect(posts(api)[0]?.body).toEqual({
				hostname: "app.example.com",
				component: "web",
			});
		},
		SLOW,
	);

	test(
		"a component without a declared port needs one, and the port is sent as a number",
		async () => {
			const user = userEvent.setup();
			const api = await showForm(stackService());
			await user.click(
				screen.getByRole("combobox", { name: "Component the domain serves" }),
			);
			await user.click(screen.getByRole("option", { name: "worker" }));
			await user.type(hostnameInput(), "jobs.example.com");
			expect(addButton()).toBeDisabled();
			expect(addButton()).toHaveAttribute(
				"title",
				"worker declares no port: enter the port.",
			);
			await user.type(
				screen.getByRole("textbox", { name: "Container port" }),
				"70000",
			);
			expect(addButton()).toBeDisabled();
			expect(addButton()).toHaveAttribute(
				"title",
				"The port must be a number from 1 to 65535.",
			);
			await user.clear(screen.getByRole("textbox", { name: "Container port" }));
			await user.type(
				screen.getByRole("textbox", { name: "Container port" }),
				"8080",
			);
			expect(addButton()).toBeEnabled();
			await user.click(addButton());
			await waitFor(() => expect(posts(api)).toHaveLength(1));
			expect(posts(api)[0]?.body).toEqual({
				hostname: "jobs.example.com",
				component: "worker",
				targetPort: 8080,
			});
		},
		SLOW,
	);

	test("a stack never offers the www redirect", async () => {
		const user = userEvent.setup();
		await showForm(stackService());
		await user.type(hostnameInput(), "example.com");
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
	});
});

describe("AddDomainForm for a service with processes", () => {
	const web = { name: "web", port: 8080, default: true, removed: false };
	const admin = { name: "admin", port: 9000, default: false, removed: false };

	test(
		"pre-selects the default process and sends it as the stored empty name",
		async () => {
			const user = userEvent.setup();
			const api = await showForm(processService([web, admin]));
			expect(
				screen.getByRole("combobox", { name: "Process the domain serves" }),
			).toHaveTextContent("web");
			await user.type(hostnameInput(), "app.example.com");
			await user.click(addButton());
			await waitFor(() => expect(posts(api)).toHaveLength(1));
			expect(posts(api)[0]?.body).toEqual({
				hostname: "app.example.com",
				process: "",
			});
		},
		SLOW,
	);

	test(
		"a non-default process is sent by name",
		async () => {
			const user = userEvent.setup();
			const api = await showForm(processService([web, admin]));
			await user.click(
				screen.getByRole("combobox", { name: "Process the domain serves" }),
			);
			await user.click(screen.getByRole("option", { name: "admin" }));
			await user.type(hostnameInput(), "admin.example.com");
			await user.click(addButton());
			await waitFor(() => expect(posts(api)).toHaveLength(1));
			expect(posts(api)[0]?.body).toEqual({
				hostname: "admin.example.com",
				process: "admin",
			});
		},
		SLOW,
	);

	test("without a process that has a port a domain cannot be added", async () => {
		const user = userEvent.setup();
		await showForm(processService([{ ...web, port: 0 }]));
		await user.type(hostnameInput(), "app.example.com");
		expect(addButton()).toBeDisabled();
		expect(addButton()).toHaveAttribute(
			"title",
			"Give a process a port before adding a domain.",
		);
	});

	test("several ported processes, none the default, must be chosen", async () => {
		const user = userEvent.setup();
		await showForm(
			processService([
				{ ...web, default: false },
				{ ...admin, default: false },
			]),
		);
		await user.type(hostnameInput(), "app.example.com");
		expect(addButton()).toBeDisabled();
		expect(addButton()).toHaveAttribute(
			"title",
			"Choose which process serves this domain.",
		);
	});

	test(
		"the parent can own the picked process",
		async () => {
			const user = userEvent.setup();
			const picked: string[] = [];
			stubApi(baseRoutes());
			await renderWithProviders(
				<AddDomainForm
					service={processService([web, admin])}
					disabled={false}
					process="admin"
					onProcessChange={(name) => picked.push(name)}
				/>,
			);
			expect(
				screen.getByRole("combobox", { name: "Process the domain serves" }),
			).toHaveTextContent("admin");
			await user.click(
				screen.getByRole("combobox", { name: "Process the domain serves" }),
			);
			await user.click(screen.getByRole("option", { name: "web" }));
			expect(picked).toEqual(["web"]);
		},
		SLOW,
	);
});

describe("AddDomainForm zone picker", () => {
	test(
		"picking a Cloudflare zone fills the hostname",
		async () => {
			const user = userEvent.setup();
			await showForm(makeService(), {
				"GET /integrations/cloudflare": {
					body: make("CloudflareConnectionView", { configured: true }),
				},
				"GET /integrations/cloudflare/zones": {
					body: {
						items: [
							{
								id: "z1",
								name: "example.com",
								accountId: "a",
								accountName: "A",
								paused: false,
								status: "active",
							},
						],
					},
				},
			});
			await user.type(hostnameInput(), "shop.other.org");
			await user.click(
				await screen.findByRole("combobox", { name: "Cloudflare zone" }),
			);
			await user.click(screen.getByRole("option", { name: "example.com" }));
			expect(hostnameInput()).toHaveValue("shop.example.com");
		},
		SLOW,
	);
});
