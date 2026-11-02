import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PLAIN_DOMAIN_RULE, type ServiceDomain } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import type { RuleContext } from "#/features/proxy/lib/rule";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { RuleSection } from "./rule-section";

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

const emptyContext: RuleContext = { otherDomains: [], redirects: [] };

interface Patch {
	ifMatch: string | null;
	body: unknown;
}

/** PATCH /domains/:id answering 200 and recording If-Match and the body. */
function patchOk(seen: Patch[]) {
	return async (request: Request) => {
		seen.push({
			ifMatch: request.headers.get("If-Match"),
			body: await request.clone().json(),
		});
		return Response.json({});
	};
}

function problem(status: number, code: string, detail: string) {
	return {
		status,
		body: { status, code, title: code, detail },
	};
}

async function renderRule(
	domain: ServiceDomain,
	options: {
		context?: RuleContext;
		disabled?: boolean;
		generation?: number;
	} = {},
) {
	const service = makeService({
		id: "svc-1",
		generation: options.generation ?? 1,
		domains: [domain],
	});
	return renderWithProviders(
		<RuleSection
			service={service}
			domain={domain}
			context={options.context ?? emptyContext}
			disabled={options.disabled ?? false}
		/>,
	);
}

const field = (label: string) => screen.getByLabelText(label);
const save = () => screen.getByRole("button", { name: /Save domain/ });
const user = () => userEvent.setup({ delay: null });

async function replace(input: HTMLElement, text: string) {
	const u = user();
	await u.clear(input);
	if (text !== "") await u.type(input, text);
}

describe("RuleSection: serve domain", () => {
	it("shows the saved rule and offers no save until something changes", async () => {
		await renderRule(
			makeDomain({
				pathPrefix: "/api",
				addPrefix: "/v2",
				stripPrefix: true,
			}),
		);
		expect(field("Hostname")).toHaveValue("app.example.com");
		expect(field("Path prefix")).toHaveValue("/api");
		expect(field("Add prefix")).toHaveValue("/v2");
		expect(
			screen.getByRole("switch", { name: "Strip the prefix" }),
		).toBeChecked();
		expect(
			screen.getByRole("button", { name: "Serve this service" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(screen.queryByLabelText("Redirect to host")).toBeNull();
		expect(screen.queryByRole("button", { name: /Save domain/ })).toBeNull();
		expect(screen.queryByRole("button", { name: "Discard" })).toBeNull();
	});

	it("saves a renamed host with If-Match set to the service generation, sending only the changed field", async () => {
		const seen: Patch[] = [];
		const api = stubApi({ "PATCH /domains/:domainId": patchOk(seen) });
		await renderRule(makeDomain(), { generation: 7 });
		await replace(field("Hostname"), " New.Example.COM. ");
		await user().click(save());
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0]).toEqual({
			ifMatch: '"7"',
			body: { hostname: "new.example.com" },
		});
		expect(api.calls[0]?.path).toBe("/domains/dom-1");
		expect(
			await screen.findByText("Domain settings saved"),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.queryByRole("button", { name: /Save domain/ })).toBeNull(),
		);
		expect(api.unmatched).toEqual([]);
	});

	it("discards edits back to the saved rule", async () => {
		const api = stubApi({});
		await renderRule(makeDomain());
		await replace(field("Hostname"), "other.example.com");
		expect(save()).toBeEnabled();
		await user().click(screen.getByRole("button", { name: "Discard" }));
		expect(field("Hostname")).toHaveValue("app.example.com");
		expect(screen.queryByRole("button", { name: /Save domain/ })).toBeNull();
		expect(api.calls).toEqual([]);
	});

	it("is not dirty when an edit is undone by normalisation alone", async () => {
		await renderRule(makeDomain());
		await replace(field("Hostname"), "APP.example.com.");
		expect(screen.queryByRole("button", { name: /Save domain/ })).toBeNull();
	});

	it("blocks Save on a malformed hostname and says why", async () => {
		const api = stubApi({});
		await renderRule(makeDomain());
		await replace(field("Hostname"), "not a host");
		expect(
			screen.getByText(
				"Use a hostname such as app.example.com (no scheme, no path).",
			),
		).toBeInTheDocument();
		expect(field("Hostname")).toBeInvalid();
		expect(save()).toBeDisabled();
		await user().click(save());
		expect(api.calls).toEqual([]);
	});

	it("blocks Save on an empty hostname", async () => {
		await renderRule(makeDomain());
		await replace(field("Hostname"), "");
		expect(screen.getByText("Enter a hostname.")).toBeInTheDocument();
		expect(save()).toBeDisabled();
	});

	it("rejects a host and path that another domain of the service already has", async () => {
		await renderRule(makeDomain(), {
			context: {
				otherDomains: [{ hostname: "taken.example.com", pathPrefix: "" }],
				redirects: [],
			},
		});
		await replace(field("Hostname"), "taken.example.com");
		expect(
			screen.getByText("This host and path already exist on this service."),
		).toBeInTheDocument();
		expect(save()).toBeDisabled();
	});

	it("allows the same host under a different path prefix", async () => {
		await renderRule(makeDomain({ hostname: "taken.example.com" }), {
			context: {
				otherDomains: [{ hostname: "taken.example.com", pathPrefix: "" }],
				redirects: [],
			},
		});
		await replace(field("Path prefix"), "/docs");
		expect(
			screen.queryByText("This host and path already exist on this service."),
		).toBeNull();
		expect(save()).toBeEnabled();
	});

	it("rejects a path prefix with a trailing slash and a bad add prefix", async () => {
		await renderRule(makeDomain());
		await replace(field("Path prefix"), "/api/");
		await replace(field("Add prefix"), "v2");
		expect(
			screen.getAllByText(
				"Use /segment or /segment/segment, without a trailing slash.",
			),
		).toHaveLength(2);
		expect(field("Path prefix")).toBeInvalid();
		expect(field("Add prefix")).toBeInvalid();
		expect(save()).toBeDisabled();
	});

	it("strips a prefix and shows the example from the typed prefixes", async () => {
		const seen: Patch[] = [];
		stubApi({ "PATCH /domains/:domainId": patchOk(seen) });
		await renderRule(makeDomain());
		expect(screen.getByText(/Needs a path prefix above\./)).toBeInTheDocument();
		await replace(field("Path prefix"), "/api");
		await replace(field("Add prefix"), "/v2");
		await user().click(
			screen.getByRole("switch", { name: "Strip the prefix" }),
		);
		expect(screen.getByText("/api/users")).toBeInTheDocument();
		expect(screen.getByText("/v2/users")).toBeInTheDocument();
		await user().click(save());
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0]?.body).toEqual({
			pathPrefix: "/api",
			addPrefix: "/v2",
			stripPrefix: true,
		});
	});

	it("refuses strip without a path prefix", async () => {
		await renderRule(makeDomain());
		await user().click(
			screen.getByRole("switch", { name: "Strip the prefix" }),
		);
		expect(screen.getByText("Strip needs a path prefix.")).toBeInTheDocument();
		expect(save()).toBeDisabled();
	});
});

describe("RuleSection: switching to a redirect", () => {
	it("shows the redirect fields instead of the serve ones", async () => {
		await renderRule(makeDomain());
		await user().click(screen.getByRole("button", { name: "Redirect to…" }));
		expect(screen.getByLabelText("This host")).toHaveValue("app.example.com");
		expect(field("Redirect to host")).toHaveValue("");
		expect(screen.getByText("A hostname, not a URL.")).toBeInTheDocument();
		expect(screen.queryByLabelText("Add prefix")).toBeNull();
		expect(
			screen.queryByRole("switch", { name: "Strip the prefix" }),
		).toBeNull();
		expect(
			screen.getByRole("switch", { name: "Keep the path and query" }),
		).toBeChecked();
		expect(
			screen.getByText(/Redirect only this path and below/),
		).toBeInTheDocument();
	});

	it("needs a valid target, not the domain itself", async () => {
		await renderRule(makeDomain());
		await user().click(screen.getByRole("button", { name: "Redirect to…" }));
		expect(screen.getByText("Enter a hostname.")).toBeInTheDocument();
		expect(save()).toBeDisabled();
		await replace(field("Redirect to host"), "https://x.test");
		expect(
			screen.getByText(
				"Use a hostname such as app.example.com (no scheme, no path).",
			),
		).toBeInTheDocument();
		await replace(field("Redirect to host"), "APP.example.com");
		expect(
			screen.getByText("A domain cannot redirect to itself."),
		).toBeInTheDocument();
		expect(field("Redirect to host")).toBeInvalid();
	});

	it("detects a whole-host redirect loop", async () => {
		await renderRule(makeDomain(), {
			context: {
				otherDomains: [],
				redirects: [{ from: "b.example.com", to: "app.example.com" }],
			},
		});
		await user().click(screen.getByRole("button", { name: "Redirect to…" }));
		await replace(field("Redirect to host"), "b.example.com");
		expect(
			screen.getByText(
				"This would loop: b.example.com redirects back to app.example.com.",
			),
		).toBeInTheDocument();
		expect(save()).toBeDisabled();
	});

	it("does not call it a loop when only a path prefix is redirected", async () => {
		await renderRule(makeDomain({ pathPrefix: "/old" }), {
			context: {
				otherDomains: [],
				redirects: [{ from: "b.example.com", to: "app.example.com" }],
			},
		});
		await user().click(screen.getByRole("button", { name: "Redirect to…" }));
		await replace(field("Redirect to host"), "b.example.com");
		expect(screen.queryByText(/This would loop/)).toBeNull();
		expect(save()).toBeEnabled();
	});

	it("warns what saving as a redirect throws away", async () => {
		await renderRule(
			makeDomain({
				stripPrefix: true,
				pathPrefix: "/api",
				addPrefix: "/v2",
				options: { compress: true },
				authUsers: ["alice"],
			}),
		);
		expect(screen.queryByText(/Saving this as a redirect clears/)).toBeNull();
		await user().click(screen.getByRole("button", { name: "Redirect to…" }));
		expect(
			screen.getByText(
				"Saving this as a redirect clears the strip-prefix setting, the added prefix, its access, header and limit options, its basic-auth users.",
			),
		).toBeInTheDocument();
	});

	it("shows no discard warning when there is nothing to lose", async () => {
		await renderRule(makeDomain());
		await user().click(screen.getByRole("button", { name: "Redirect to…" }));
		expect(screen.queryByText(/Saving this as a redirect clears/)).toBeNull();
	});

	it("saves a redirect, clearing prefixes and options in the same PATCH", async () => {
		const seen: Patch[] = [];
		stubApi({ "PATCH /domains/:domainId": patchOk(seen) });
		await renderRule(
			makeDomain({
				stripPrefix: true,
				pathPrefix: "/api",
				addPrefix: "/v2",
				options: { compress: true },
			}),
		);
		await user().click(screen.getByRole("button", { name: "Redirect to…" }));
		await replace(field("Redirect to host"), "Target.Example.com");
		await user().click(save());
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0]?.body).toEqual({
			kind: "redirect",
			redirectTo: "target.example.com",
			stripPrefix: false,
			addPrefix: "",
			options: {},
		});
	});

	it("sends the redirect kind and keeps-path choice only when they differ", async () => {
		const seen: Patch[] = [];
		stubApi({ "PATCH /domains/:domainId": patchOk(seen) });
		await renderRule(makeDomain());
		await user().click(screen.getByRole("button", { name: "Redirect to…" }));
		await replace(field("Redirect to host"), "target.example.com");
		expect(screen.getByRole("button", { name: "Permanent" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(
			screen.getByText(/Browsers and search engines remember it/),
		).toBeInTheDocument();
		await user().click(screen.getByRole("button", { name: "Temporary" }));
		expect(screen.getByText(/Nothing is remembered/)).toBeInTheDocument();
		await user().click(
			screen.getByRole("switch", { name: "Keep the path and query" }),
		);
		await user().click(save());
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0]?.body).toEqual({
			kind: "redirect",
			redirectTo: "target.example.com",
			redirectCode: 307,
			preservePath: false,
		});
	});
});

describe("RuleSection: redirect domain", () => {
	const redirect = () =>
		makeDomain({
			kind: "redirect",
			redirectTo: "target.example.com",
			redirectCode: 301,
			preservePath: false,
		});

	it("shows the saved redirect settings", async () => {
		await renderRule(redirect());
		expect(field("Redirect to host")).toHaveValue("target.example.com");
		expect(screen.getByRole("button", { name: "Permanent" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(
			screen.getByRole("switch", { name: "Keep the path and query" }),
		).not.toBeChecked();
		expect(screen.queryByText(/Saving this as a redirect clears/)).toBeNull();
	});

	it("keeps 301 when staying permanent and moves to temporary with 307", async () => {
		const seen: Patch[] = [];
		stubApi({ "PATCH /domains/:domainId": patchOk(seen) });
		await renderRule(redirect());
		await user().click(screen.getByRole("button", { name: "Temporary" }));
		await user().click(save());
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0]?.body).toEqual({ redirectCode: 307 });
	});

	it("switching back to serve clears the redirect target", async () => {
		const seen: Patch[] = [];
		stubApi({ "PATCH /domains/:domainId": patchOk(seen) });
		await renderRule(redirect());
		await user().click(
			screen.getByRole("button", { name: "Serve this service" }),
		);
		await user().click(save());
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0]?.body).toEqual({ kind: "serve", redirectTo: "" });
	});

	it("explains a redirect kept by another domain's www option", async () => {
		await renderRule(
			makeDomain({
				kind: "redirect",
				redirectTo: "example.com",
				hostname: "www.example.com",
				wwwRedirectFor: "example.com",
			}),
		);
		expect(
			screen.getByText(/This redirect is kept by the www redirect of/),
		).toBeInTheDocument();
		expect(screen.getAllByText("example.com").length).toBeGreaterThan(0);
	});
});

describe("RuleSection: save failures", () => {
	it("shows a path-prefix rejection under that field", async () => {
		stubApi({
			"PATCH /domains/:domainId": problem(
				422,
				"invalid_path_prefix",
				"Path prefix overlaps another domain.",
			),
		});
		await renderRule(makeDomain());
		await replace(field("Path prefix"), "/api");
		await user().click(save());
		expect(
			await screen.findByText("Path prefix overlaps another domain."),
		).toBeInTheDocument();
		// Inline under the field: not repeated as a general line.
		expect(
			screen.getAllByText("Path prefix overlaps another domain."),
		).toHaveLength(1);
		// Edits are kept so they can be corrected.
		expect(field("Path prefix")).toHaveValue("/api");
	});

	it("shows a redirect-target rejection under the target field", async () => {
		stubApi({
			"PATCH /domains/:domainId": problem(
				422,
				"redirect_loop",
				"That redirect would loop.",
			),
		});
		await renderRule(makeDomain());
		await user().click(screen.getByRole("button", { name: "Redirect to…" }));
		await replace(field("Redirect to host"), "target.example.com");
		await user().click(save());
		expect(
			await screen.findByText("That redirect would loop."),
		).toBeInTheDocument();
		expect(screen.getAllByText("That redirect would loop.")).toHaveLength(1);
	});

	it("shows an unmapped failure as a general message and keeps the edit", async () => {
		stubApi({
			"PATCH /domains/:domainId": problem(
				500,
				"boom",
				"The control plane exploded.",
			),
		});
		await renderRule(makeDomain());
		await replace(field("Hostname"), "new.example.com");
		await user().click(save());
		expect(
			await screen.findByText("The control plane exploded."),
		).toBeInTheDocument();
		expect(field("Hostname")).toHaveValue("new.example.com");
		expect(save()).toBeEnabled();
	});

	it("shows the upgrade banner naming the server when the agent is too old", async () => {
		stubApi({
			"PATCH /domains/:domainId": problem(
				422,
				"agent_too_old",
				"agent too old",
			),
		});
		await renderRule(makeDomain());
		await replace(field("Hostname"), "new.example.com");
		await user().click(save());
		const banner = await screen.findByTestId("agent-too-old");
		expect(
			within(banner).getByText(/The agent on hetzner-01 is too old/),
		).toBeInTheDocument();
	});

	it("drops the edit when someone else changed the service (412)", async () => {
		stubApi({
			"PATCH /domains/:domainId": problem(
				412,
				"generation_conflict",
				"The service changed.",
			),
		});
		await renderRule(makeDomain());
		await replace(field("Hostname"), "new.example.com");
		await user().click(save());
		await waitFor(() =>
			expect(field("Hostname")).toHaveValue("app.example.com"),
		);
		expect(screen.queryByRole("button", { name: /Save domain/ })).toBeNull();
	});

	it("locks the buttons while the save is in flight", async () => {
		let release: () => void = () => {};
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		stubApi({
			"PATCH /domains/:domainId": async () => {
				await gate;
				return Response.json({});
			},
		});
		await renderRule(makeDomain());
		await replace(field("Hostname"), "new.example.com");
		await user().click(save());
		await waitFor(() => expect(save()).toBeDisabled());
		expect(screen.getByRole("button", { name: "Discard" })).toBeDisabled();
		release();
		expect(
			await screen.findByText("Domain settings saved", {}, { timeout: 8000 }),
		).toBeInTheDocument();
		await waitFor(
			() =>
				expect(
					screen.queryByRole("button", { name: /Save domain/ }),
				).toBeNull(),
			{ timeout: 10_000 },
		);
	}, 15_000);
});

describe("RuleSection: www redirect switch", () => {
	const apex = (overrides: Partial<ServiceDomain> = {}) =>
		makeDomain({
			hostname: "example.com",
			wwwPartner: "www.example.com",
			wwwRedirect: false,
			...overrides,
		});

	it("is offered on a whole-host apex domain and stages www on", async () => {
		const seen: Patch[] = [];
		stubApi({ "PATCH /domains/:domainId": patchOk(seen) });
		await renderRule(apex(), { generation: 3 });
		const toggle = screen.getByRole("switch", {
			name: "Redirect www.example.com to this domain",
		});
		expect(toggle).not.toBeChecked();
		expect(
			screen.getByText(/Also route www.example.com and send its visitors here/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Saves at once · applies on next deploy\./),
		).toBeInTheDocument();
		await user().click(toggle);
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0]).toEqual({ ifMatch: '"3"', body: { wwwRedirect: true } });
	});

	it("stays offered, checked, to switch an existing redirect off", async () => {
		const seen: Patch[] = [];
		stubApi({ "PATCH /domains/:domainId": patchOk(seen) });
		await renderRule(
			apex({
				hostname: "docs.example.com",
				wwwRedirect: true,
				wwwPartner: "www.docs.example.com",
			}),
		);
		const toggle = screen.getByRole("switch", {
			name: "Redirect www.docs.example.com to this domain",
		});
		expect(toggle).toBeChecked();
		expect(
			screen.getByText(
				/www.docs.example.com answers with a permanent redirect \(301\)/,
			),
		).toBeInTheDocument();
		await user().click(toggle);
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0]?.body).toEqual({ wwwRedirect: false });
	});

	it("is not offered for a subdomain without an existing redirect", async () => {
		await renderRule(
			apex({ hostname: "a.b.example.com", wwwPartner: "www.a.b.example.com" }),
		);
		expect(
			screen.queryByText(/Redirect www.a.b.example.com to this domain/),
		).toBeNull();
	});

	it("is not offered when the domain has no www partner", async () => {
		await renderRule(apex({ wwwPartner: "" }));
		expect(screen.queryByRole("switch", { name: /to this domain/ })).toBeNull();
	});

	it("is not offered for a path-scoped domain or a redirect domain", async () => {
		const { unmount } = await renderRule(apex({ pathPrefix: "/app" }));
		expect(screen.queryByRole("switch", { name: /to this domain/ })).toBeNull();
		unmount();
		await renderRule(
			apex({ kind: "redirect", redirectTo: "other.example.com" }),
		);
		expect(screen.queryByRole("switch", { name: /to this domain/ })).toBeNull();
	});

	it("hides while the rule has unsaved edits", async () => {
		await renderRule(apex());
		expect(
			screen.getByRole("switch", { name: /to this domain/ }),
		).toBeInTheDocument();
		await replace(field("Path prefix"), "/x");
		expect(screen.queryByRole("switch", { name: /to this domain/ })).toBeNull();
	});

	it("uses the local-name wording for a local hostname", async () => {
		await renderRule(
			apex({ hostname: "example.test", wwwPartner: "www.example.test" }),
		);
		expect(
			screen.getByText(
				/Local hostnames need no DNS record and no public certificate\./,
			),
		).toBeInTheDocument();
	});
});

describe("RuleSection: agent too old", () => {
	it("disables every control", async () => {
		await renderRule(
			makeDomain({ wwwPartner: "www.example.com", hostname: "example.com" }),
			{
				disabled: true,
			},
		);
		expect(field("Hostname")).toBeDisabled();
		expect(field("Path prefix")).toBeDisabled();
		expect(field("Add prefix")).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Serve this service" }),
		).toBeDisabled();
		expect(screen.getByRole("button", { name: "Redirect to…" })).toBeDisabled();
		expect(
			screen.getByRole("switch", { name: "Strip the prefix" }),
		).toHaveAttribute("data-disabled");
		expect(
			screen.getByRole("switch", { name: /to this domain/ }),
		).toHaveAttribute("data-disabled");
	});
});
