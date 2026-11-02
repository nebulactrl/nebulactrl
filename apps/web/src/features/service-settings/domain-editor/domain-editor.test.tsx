import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DomainDns } from "#/api/gen/types.gen";
import {
	PLAIN_DOMAIN_RULE,
	type Service,
	type ServiceDomain,
} from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import type { DomainStaged } from "#/features/proxy/lib/staged";
import { NEW_NAME_NOTE } from "#/lib/dns-new-name";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DomainEditor } from "./domain-editor";

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

const noStaged: DomainStaged = { added: false, buckets: new Set() };

const context = { otherDomains: [], redirects: [] };

interface Options {
	service?: Service;
	staged?: DomainStaged;
	publicIp?: string | null;
	agentTooOld?: boolean;
	removing?: boolean;
}

async function show(domain: ServiceDomain, options: Options = {}) {
	const api = stubApi({
		"GET /integrations/cloudflare": {
			body: make("CloudflareConnectionView", { configured: false }),
		},
		"GET /servers/:serverId/dns01": {
			body: make("ServerDNS01"),
		},
		"GET /me": { body: make("MeBody") },
	});
	const removed: string[] = [];
	await renderWithProviders(
		<ul>
			<DomainEditor
				service={options.service ?? makeService({ id: "svc-1" })}
				domain={domain}
				publicIp={
					options.publicIp === undefined ? "203.0.113.5" : options.publicIp
				}
				staged={options.staged ?? noStaged}
				context={context}
				agentTooOld={options.agentTooOld ?? false}
				removing={options.removing ?? false}
				onRemove={() => removed.push(domain.id)}
			/>
		</ul>,
	);
	return { api, removed };
}

describe("DomainEditor header", () => {
	test("links the hostname and path prefix to the public URL in a new tab", async () => {
		await show(makeDomain({ hostname: "app.example.com", pathPrefix: "/api" }));
		const link = screen.getByRole("link", { name: "app.example.com/api" });
		expect(link).toHaveAttribute("href", "https://app.example.com/api");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noopener noreferrer");
	});

	test.each([
		["active", "active"],
		["pending", "pending"],
		["error", "error"],
	] as const)("shows the %s status of the domain", async (status, label) => {
		await show(makeDomain({ status }));
		expect(screen.getByText(label)).toBeInTheDocument();
	});

	test("a domain pointing at the server reads DNS ok", async () => {
		await show(makeDomain({ dnsOk: true }));
		expect(screen.getByText("DNS ok")).toBeInTheDocument();
		expect(screen.getByText("DNS points at this server.")).toBeInTheDocument();
	});

	test("a domain not pointing at the server tells which record to create", async () => {
		await show(makeDomain({ dnsOk: false }), { publicIp: "203.0.113.5" });
		expect(screen.getByText("DNS not pointing")).toBeInTheDocument();
		expect(
			screen.getByText("Point an A record for app.example.com to 203.0.113.5."),
		).toBeInTheDocument();
	});

	test("without a known server IP the hint names the server's public IP generically", async () => {
		await show(makeDomain({ dnsOk: false }), { publicIp: null });
		expect(
			screen.getByText(
				"Point an A record for app.example.com to the server's public IP.",
			),
		).toBeInTheDocument();
	});

	test("an unchecked domain reads DNS unchecked", async () => {
		await show(makeDomain({ dnsOk: null }));
		expect(screen.getByText("DNS unchecked")).toBeInTheDocument();
	});

	test("a domain in a Cloudflare zone shows the Cloudflare state with its hint", async () => {
		await show(
			makeDomain({ dns: cachedDns({ status: "managed", proxied: true }) }),
		);
		const badge = screen.getByText("Cloudflare · Managed");
		expect(badge.closest("[title]")).toHaveAttribute(
			"title",
			"NebulaCtrl created the record and keeps it in sync.",
		);
		expect(
			screen.getByText(
				"NebulaCtrl manages this record in your Cloudflare zone example.com, proxied through Cloudflare.",
			),
		).toBeInTheDocument();
	});

	test("the Cloudflare badge carries the sync message when there is one", async () => {
		await show(
			makeDomain({
				dns: cachedDns({ status: "missing", message: "Creating the record" }),
			}),
		);
		expect(
			screen.getByText("Cloudflare · No record").closest("[title]"),
		).toHaveAttribute("title", "Creating the record");
		expect(screen.getByText("DNS pending")).toBeInTheDocument();
	});

	test("a Cloudflare conflict shows the conflict once, in the DNS chip and message", async () => {
		await show(
			makeDomain({
				dns: cachedDns({
					status: "conflict",
					message: "A different A record exists.",
				}),
			}),
		);
		expect(screen.getByText("Conflicting record")).toBeInTheDocument();
		expect(
			screen.getByText("A different A record exists."),
		).toBeInTheDocument();
		expect(screen.queryByText(/^Cloudflare ·/)).not.toBeInTheDocument();
	});

	test("badges the generated, stack-target, process and TLS facts of a domain", async () => {
		await show(
			makeDomain({
				generated: true,
				component: "web",
				targetPort: 3000,
				tlsMode: "custom",
			}),
		);
		expect(screen.getByText("generated")).toBeInTheDocument();
		expect(screen.getByText("web:3000")).toBeInTheDocument();
		expect(screen.getByText("tls custom")).toBeInTheDocument();
	});

	test("a plain automatic-TLS domain has none of those badges", async () => {
		await show(makeDomain());
		expect(screen.queryByText("generated")).not.toBeInTheDocument();
		expect(screen.queryByText(/^tls /)).not.toBeInTheDocument();
		expect(screen.queryByText(/redirect$/)).not.toBeInTheDocument();
	});

	test("a service with processes says which process the domain serves", async () => {
		const service = makeService({
			id: "svc-1",
			processes: [
				{ name: "web", default: true },
				{ name: "admin", default: false },
			] as never,
		});
		await show(makeDomain({ process: "admin" }), { service });
		expect(screen.getByText("serves admin")).toBeInTheDocument();
	});

	test("a domain with no stored process serves the default one", async () => {
		const service = makeService({
			id: "svc-1",
			processes: [
				{ name: "web", default: true },
				{ name: "admin", default: false },
			] as never,
		});
		await show(makeDomain({ process: "" }), { service });
		expect(screen.getByText("serves web")).toBeInTheDocument();
	});

	test("a redirect shows its target, family and no certificate or option sections", async () => {
		const user = userEvent.setup();
		await show(
			makeDomain({
				kind: "redirect",
				redirectTo: "https://new.example.com",
				redirectCode: 302,
			}),
		);
		expect(screen.getByText("→ https://new.example.com")).toBeInTheDocument();
		expect(screen.getByText("temporary redirect")).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: "Edit app.example.com" }),
		);
		expect(
			screen.getByText(/A redirect answers every request itself/),
		).toBeInTheDocument();
		expect(document.querySelector("[data-section=certificate]")).toBeNull();
	});

	test("a permanent redirect code is labelled permanent", async () => {
		await show(
			makeDomain({
				kind: "redirect",
				redirectTo: "https://x.test",
				redirectCode: 308,
			}),
		);
		expect(screen.getByText("permanent redirect")).toBeInTheDocument();
	});

	test("shows the www redirect pairing from both sides", async () => {
		await show(
			makeDomain({ wwwRedirect: true, wwwPartner: "www.example.com" }),
		);
		expect(screen.getByText("+ www.example.com redirect")).toBeInTheDocument();
	});

	test("the redirect side of a www pair says it is kept by the target's option", async () => {
		await show(
			makeDomain({
				hostname: "www.example.com",
				kind: "redirect",
				redirectTo: "https://example.com",
				wwwRedirectFor: "example.com",
			}),
		);
		expect(screen.getByText("www redirect")).toBeInTheDocument();
	});

	test("a domain added since the last deploy is marked New with the generations", async () => {
		await show(makeDomain(), {
			staged: { added: true, buckets: new Set() },
			service: makeService({
				id: "svc-1",
				generation: 5,
				appliedGeneration: 3,
			}),
		});
		const badge = screen.getByText("New");
		expect(badge.closest("[title]")).toHaveAttribute(
			"title",
			"Staged: generation 5, deployed 3",
		);
	});

	test("a domain with a staged option change is marked Staged", async () => {
		await show(makeDomain(), {
			staged: { added: false, buckets: new Set(["tls"]) },
		});
		expect(screen.getByText("Staged")).toBeInTheDocument();
		expect(screen.queryByText("New")).not.toBeInTheDocument();
	});

	test("an unstaged domain shows no staged badge", async () => {
		await show(makeDomain());
		expect(screen.queryByText("Staged")).not.toBeInTheDocument();
		expect(screen.queryByText("New")).not.toBeInTheDocument();
	});

	test("a just-created domain whose DNS is fine gets the new-name heads-up", async () => {
		await show(
			makeDomain({ createdAt: new Date().toISOString(), dnsOk: true }),
		);
		expect(screen.getByText(NEW_NAME_NOTE)).toBeInTheDocument();
	});

	test.each([
		["an old domain", { createdAt: "2020-01-01T00:00:00Z", dnsOk: true }],
		[
			"a generated domain",
			{ createdAt: new Date().toISOString(), dnsOk: true, generated: true },
		],
		[
			"a domain whose DNS is not pointing",
			{ createdAt: new Date().toISOString(), dnsOk: false },
		],
	])("%s gets no new-name heads-up", async (_name, overrides) => {
		await show(makeDomain(overrides));
		expect(screen.queryByText(NEW_NAME_NOTE)).not.toBeInTheDocument();
	});
});

describe("DomainEditor card", () => {
	test("the chevron opens and closes the body, announcing it through aria-expanded", async () => {
		const user = userEvent.setup();
		await show(makeDomain());
		const toggle = screen.getByRole("button", { name: "Edit app.example.com" });
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		await user.click(toggle);
		const open = screen.getByRole("button", {
			name: "Collapse app.example.com",
		});
		expect(open).toHaveAttribute("aria-expanded", "true");
		// The body is the panel the chevron controls.
		const panel = document.getElementById(
			open.getAttribute("aria-controls") ?? "",
		);
		expect(panel).not.toBeNull();
		expect(
			await within(panel as HTMLElement).findByText("Certificate"),
		).toBeInTheDocument();
		await user.click(open);
		expect(
			screen.getByRole("button", { name: "Edit app.example.com" }),
		).toHaveAttribute("aria-expanded", "false");
	});

	test("the title includes the path prefix in the toggle label", async () => {
		await show(makeDomain({ pathPrefix: "/api" }));
		expect(
			screen.getByRole("button", { name: "Edit app.example.com/api" }),
		).toBeInTheDocument();
	});

	test("remove asks the list to remove this domain", async () => {
		const user = userEvent.setup();
		const { removed } = await show(makeDomain({ id: "dom-9" }));
		await user.click(
			screen.getByRole("button", { name: "Remove app.example.com" }),
		);
		expect(removed).toEqual(["dom-9"]);
	});

	test("remove is disabled while another removal runs", async () => {
		await show(makeDomain(), { removing: true });
		expect(
			screen.getByRole("button", { name: "Remove app.example.com" }),
		).toBeDisabled();
	});

	test("an open card reads the certificate DNS-01 state of its server", async () => {
		const user = userEvent.setup();
		const { api } = await show(makeDomain());
		await user.click(
			screen.getByRole("button", { name: "Edit app.example.com" }),
		);
		await waitFor(() =>
			expect(api.calls.map((c) => c.path)).toContain("/servers/server-1/dns01"),
		);
	});
});
