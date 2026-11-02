import { describe, expect, test } from "bun:test";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type {
	TailnetConfig,
	TailnetView,
	TailscaleIntegration,
} from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { type ApiStub, stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { type SettingsDraft, toDraft } from "./draft";
import {
	emptyTailnetDraft,
	MAX_TCP_FORWARDS,
	type TailnetDraft,
} from "./tailnet-draft";
import { TailnetSection } from "./tailnet-section";

const ENV = {
	id: "env-1",
	projectId: "proj-1",
	name: "Production",
	slug: "production",
	isProduction: true,
	kind: "standard",
	createdAt: "2026-01-01T00:00:00Z",
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
};

const integration = (
	over: Overrides<TailscaleIntegration> = {},
): TailscaleIntegration =>
	make("TailscaleIntegration", {
		clientId: "k123",
		defaultTags: ["tag:nebula-svc"],
		tailnet: "-",
		lastTest: { ok: null, at: null },
		...over,
	});

const view = (over: Overrides<TailnetView> = {}): TailnetView =>
	make("TailnetView", {
		fqdn: "web-production.tail1234.ts.net.",
		hostname: "web-production",
		ips: ["100.64.0.5", "fd7a:115c::5"],
		provider: "tailscale",
		state: "online",
		url: "https://web-production.tail1234.ts.net",
		web: true,
		...over,
	});

const tailnet = (over: Partial<TailnetDraft> = {}): TailnetDraft => ({
	...emptyTailnetDraft(true),
	...over,
});

const base = (over: Partial<Service> = {}): Service =>
	makeService({ id: "svc-web", slug: "web", ...over });

interface Setup {
	admin?: boolean;
	integration?: Partial<TailscaleIntegration> | null;
	routes?: Parameters<typeof stubApi>[0];
}

function api({ admin = true, integration: integ = {}, routes }: Setup = {}) {
	return stubApi({
		"GET /me": {
			body: make("MeBody", {
				principal: {
					label: "robin",
					type: "user",
					scopes: admin ? ["read", "admin"] : ["read"],
				},
			}),
		},
		"GET /integrations/tailscale": integ
			? { body: integration(integ) }
			: problem(500, { code: "internal" }),
		"GET /projects": { body: { items: [] } },
		"GET /projects/:projectId/environments": { body: { items: [ENV] } },
		...routes,
	});
}

let latest: SettingsDraft;

function Harness({
	service,
	initial,
	errors = {},
}: {
	service: Service;
	initial?: Partial<SettingsDraft>;
	errors?: Parameters<typeof TailnetSection>[0]["errors"];
}) {
	const [draft, setDraft] = useState<SettingsDraft>({
		...toDraft(service),
		...initial,
	});
	latest = draft;
	return (
		<TailnetSection
			service={service}
			draft={draft}
			errors={errors}
			set={(key, value) => setDraft((d) => ({ ...d, [key]: value }))}
		/>
	);
}

const inEnv = {
	path: "/p/proj-1/production",
	routePath: "/p/$projectId/$envSlug",
};

/** Waits for the environment slug, so the hostname placeholder is the final one. */
const ready = () => screen.findByPlaceholderText("web-production");

const sw = (name: string | RegExp) => screen.getByRole("switch", { name });
const isDisabled = (el: HTMLElement) =>
	el.getAttribute("aria-disabled") === "true";

/** Runs the 1.3 s hold of a hold-to-confirm button on the next tick instead; resolves once Funnel is staged. */
async function holdToConfirmFunnel(button: HTMLElement) {
	const real = window.setTimeout;
	window.setTimeout = ((fn: () => void, ms?: number, ...args: unknown[]) =>
		real(fn, ms === 1300 ? 0 : ms, ...args)) as typeof window.setTimeout;
	try {
		fireEvent.keyDown(button, { key: "Enter" });
		await waitFor(() => expect(latest.tailnet?.funnel).toBe(true));
	} finally {
		window.setTimeout = real;
	}
}

describe("TailnetSection switch", () => {
	test("is off for a service that is not on a tailnet and hides every tailnet field", async () => {
		api();
		await renderWithProviders(<Harness service={base()} />, inEnv);
		expect(sw("Reachable over your tailnet")).not.toBeChecked();
		expect(screen.getByText("Off: not on a tailnet")).toBeInTheDocument();
		expect(screen.queryByLabelText("Hostname")).toBeNull();
		expect(screen.queryByText("Extra tags")).toBeNull();
		expect(screen.queryByText("Advanced")).toBeNull();
	});

	test("turning it on stages a fresh draft with the web exposure on when there is a port", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(<Harness service={base()} />, inEnv);
		await user.click(sw("Reachable over your tailnet"));
		expect(latest.tailnet).toEqual(emptyTailnetDraft(true));
		expect(
			await screen.findByText("On: this service gets its own tailnet device"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Hostname")).toBeInTheDocument();
		expect(sw("Web")).toBeChecked();
	});

	test("without an HTTP port the new draft has no web exposure and Web cannot be turned on", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={base()} initial={{ port: "" }} />,
			inEnv,
		);
		await user.click(sw("Reachable over your tailnet"));
		expect(latest.tailnet?.web).toBe(false);
		expect(sw("Web")).not.toBeChecked();
		expect(isDisabled(sw("Web"))).toBe(true);
		expect(
			screen.getByText(/Set a port in Networking first/),
		).toBeInTheDocument();
		expect(screen.getByText(/the service's port \(none\)/)).toBeInTheDocument();
	});

	test("cannot be turned on for a one-shot job", async () => {
		api();
		await renderWithProviders(
			<Harness service={base()} initial={{ oneshot: true }} />,
			inEnv,
		);
		expect(isDisabled(sw("Reachable over your tailnet"))).toBe(true);
	});

	test("shows the section's own problem under the switch", async () => {
		api();
		await renderWithProviders(
			<Harness
				service={base()}
				errors={{ tailnet: "Tailnet services cannot be one-shot jobs" }}
			/>,
			inEnv,
		);
		expect(
			screen.getByText("Tailnet services cannot be one-shot jobs"),
		).toBeInTheDocument();
	});

	test("turning it off keeps the settings staged and warns the device will be removed", async () => {
		api();
		const user = userEvent.setup();
		const service = base({
			tailnet: view(),
			config: {
				...makeService().config,
				tailnet: { enabled: true, web: true, hostname: "custom" },
			},
		});
		await renderWithProviders(<Harness service={service} />, inEnv);
		expect(sw("Reachable over your tailnet")).toBeChecked();
		await user.click(sw("Reachable over your tailnet"));
		expect(latest.tailnet).toMatchObject({
			enabled: false,
			hostname: "custom",
		});
		expect(
			screen.getByText("Turning this off removes the device"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/Removes device web-production\.tail1234\.ts\.net from your tailnet/,
			),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Hostname")).toBeNull();

		// Turning it back on restores what was there, not a blank draft.
		await user.click(sw("Reachable over your tailnet"));
		expect(latest.tailnet).toMatchObject({ enabled: true, hostname: "custom" });
		expect(
			screen.queryByText("Turning this off removes the device"),
		).toBeNull();
	});

	test("names the device by its hostname while the node has no fqdn yet", async () => {
		api();
		const service = base({
			tailnet: view({
				fqdn: "",
				hostname: "web-production",
				state: "provisioning",
			}),
		});
		await renderWithProviders(
			<Harness service={service} initial={{ tailnet: null }} />,
			inEnv,
		);
		expect(
			screen.getByText(/Removes device web-production from your tailnet/),
		).toBeInTheDocument();
	});
});

describe("TailnetSection integration", () => {
	test("a connected Tailscale integration shows HTTPS wording and no warnings", async () => {
		api();
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		await ready();
		// Once the provider is known the wording names one provider only.
		expect(
			await screen.findByText(/^HTTPS on 443 → the service's port 80\./),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Becomes <hostname>\.<tailnet>\.ts\.net/),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Tailscale or Headscale is not connected"),
		).toBeNull();
		expect(screen.queryByText(/Cannot check the integration/)).toBeNull();
	});

	test("an unconfigured integration warns and links to Integrations settings", async () => {
		api({ integration: { configured: false } });
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		expect(
			await screen.findByText("Tailscale or Headscale is not connected"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/You can stage this change now\./),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Team settings → Integrations" }),
		).toHaveAttribute("href", "/settings/integrations");
	});

	test("a reader who is not an admin is not asked for the integration", async () => {
		const stub = api({ admin: false });
		const { queryClient } = await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		await waitFor(() =>
			expect(stub.calls.some((c) => c.path === "/me")).toBe(true),
		);
		await waitFor(() => expect(queryClient.isFetching()).toBe(0));
		await act(async () => {});
		expect(stub.calls.some((c) => c.path === "/integrations/tailscale")).toBe(
			false,
		);
		expect(
			screen.getByText(/Cannot check the integration from here/),
		).toBeInTheDocument();
		// Provider unknown: both are named.
		expect(
			screen.getByText(
				/HTTPS on 443 with Tailscale, HTTP on 80 with Headscale/,
			),
		).toBeInTheDocument();
	});

	test("an integration that cannot be read looks like a missing admin right", async () => {
		const stub = api({ integration: null });
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		await waitFor(() =>
			expect(stub.calls.some((c) => c.path === "/integrations/tailscale")).toBe(
				true,
			),
		);
		await waitFor(() =>
			expect(
				screen.getByText(/Cannot check the integration from here/),
			).toBeInTheDocument(),
		);
	});

	test("Headscale: HTTP wording, no TLS option, no Funnel", async () => {
		api({ integration: { provider: "headscale" } });
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{
					tailnet: tailnet({
						tcp: [{ listen: "5432", target: "5432", tls: false }],
					}),
				}}
			/>,
			inEnv,
		);
		expect(
			await screen.findByText(
				/Headscale does not provide HTTPS certificates\./,
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(/^HTTP on 80 → the service's port 80\./),
		).toBeInTheDocument();
		expect(
			screen.getByText(/The device's name on your Headscale tailnet/),
		).toBeInTheDocument();
		expect(screen.queryByRole("checkbox", { name: "TLS" })).toBeNull();
		expect(
			screen.queryByText(/TLS terminates with the device's certificate/),
		).toBeNull();
		expect(
			screen.getByText(/Funnel is not available with Headscale/),
		).toBeInTheDocument();
		expect(isDisabled(sw(/^Funnel/))).toBe(true);
		await user.click(sw(/^Funnel/));
		expect(screen.queryByRole("alertdialog")).toBeNull();
		expect(latest.tailnet?.funnel).toBe(false);
	});

	test("Tailscale offers a TLS option per TCP row", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{
					tailnet: tailnet({
						tcp: [{ listen: "5432", target: "5432", tls: false }],
					}),
				}}
			/>,
			inEnv,
		);
		const tls = await screen.findByRole("checkbox", { name: "TLS" });
		await user.click(tls);
		expect(latest.tailnet?.tcp).toEqual([
			{ listen: "5432", target: "5432", tls: true },
		]);
		expect(
			screen.getByText(/TLS terminates with the device's certificate/),
		).toBeInTheDocument();
	});
});

describe("TailnetSection hostname", () => {
	test("placeholder is the derived default and typing stages the hostname", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		const field = await ready();
		await user.type(field, "api-edge");
		expect(latest.tailnet?.hostname).toBe("api-edge");
		expect(screen.getByLabelText("Hostname")).toHaveValue("api-edge");
	});

	test("outside an environment the placeholder says the environment is appended", async () => {
		api();
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
		);
		expect(
			await screen.findByPlaceholderText("web-<environment>"),
		).toBeInTheDocument();
	});

	test("shows the hostname problem", async () => {
		api();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet({ hostname: "Bad_Name" }) }}
				errors={{ tailnetHostname: "Hostname may only contain a-z, 0-9 and -" }}
			/>,
			inEnv,
		);
		await ready();
		expect(
			screen.getByText("Hostname may only contain a-z, 0-9 and -"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Hostname")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});
});

describe("TailnetSection web exposure", () => {
	test("turning Web off also turns Funnel off, which depends on it", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet({ funnel: true }) }}
			/>,
			inEnv,
		);
		await ready();
		expect(sw("Web")).toBeChecked();
		await user.click(sw("Web"));
		expect(latest.tailnet).toMatchObject({ web: false, funnel: false });
		expect(screen.getByText("Off: no web exposure")).toBeInTheDocument();
		expect(
			screen.getByText(
				/Funnel publishes the web exposure\. Turn the web exposure on first\./,
			),
		).toBeInTheDocument();
		expect(isDisabled(sw(/^Funnel/))).toBe(true);
	});

	test("turning Web on keeps the rest of the draft", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet({ web: false, hostname: "keep" }) }}
			/>,
			inEnv,
		);
		await ready();
		await user.click(sw("Web"));
		expect(latest.tailnet).toMatchObject({
			web: true,
			hostname: "keep",
			funnel: false,
		});
	});

	test("shows the web problem", async () => {
		api();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet() }}
				errors={{ tailnetWeb: "The web exposure needs an HTTP port" }}
			/>,
			inEnv,
		);
		expect(
			await screen.findByText("The web exposure needs an HTTP port"),
		).toBeInTheDocument();
	});
});

describe("TailnetSection TCP ports", () => {
	const row = (listen: string, target = listen, tls = false) => ({
		listen,
		target,
		tls,
	});

	test("Add port appends a blank row that is typed into", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		await ready();
		await user.click(screen.getByRole("button", { name: "Add port" }));
		expect(latest.tailnet?.tcp).toEqual([row("", "")]);
		const listen = screen.getByLabelText("TCP port 1: tailnet port");
		const target = screen.getByLabelText("TCP port 1: service port");
		// The service port defaults to the tailnet port.
		expect(target).toHaveAttribute("placeholder", "same");
		await user.type(listen, "2222");
		expect(target).toHaveAttribute("placeholder", "2222");
		await user.type(target, "22");
		expect(latest.tailnet?.tcp).toEqual([row("2222", "22")]);
	});

	test("a blank row is not flagged but a bad port is", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet({ tcp: [row("", "")] }) }}
			/>,
			inEnv,
		);
		const listen = await screen.findByLabelText("TCP port 1: tailnet port");
		expect(listen).toHaveAttribute("aria-invalid", "false");
		await user.type(listen, "abc");
		expect(listen).toHaveAttribute("aria-invalid", "true");
		expect(screen.getByLabelText("TCP port 1: service port")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		await user.clear(listen);
		await user.type(listen, "5432");
		expect(listen).toHaveAttribute("aria-invalid", "false");
	});

	test("a row can be removed by its number", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{
					tailnet: tailnet({ tcp: [row("5432"), row("6379"), row("3306")] }),
				}}
			/>,
			inEnv,
		);
		await ready();
		await user.click(screen.getByRole("button", { name: "Remove TCP port 2" }));
		expect(latest.tailnet?.tcp.map((r) => r.listen)).toEqual(["5432", "3306"]);
		expect(screen.getByLabelText("TCP port 2: tailnet port")).toHaveValue(
			"3306",
		);
		expect(screen.queryByLabelText("TCP port 3: tailnet port")).toBeNull();
	});

	test("a preset fills a blank row, then is disabled as already listed", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet({ tcp: [row("", "")] }) }}
			/>,
			inEnv,
		);
		await ready();
		await user.click(
			screen.getByRole("button", { name: "Expose Postgres 5432" }),
		);
		expect(latest.tailnet?.tcp).toEqual([row("5432")]);
		const postgres = screen.getByRole("button", {
			name: "Expose Postgres 5432",
		});
		expect(postgres).toBeDisabled();
		expect(postgres).toHaveAttribute("title", "Already listed");
		expect(
			screen.getByRole("button", { name: "Expose Redis 6379" }),
		).toBeEnabled();
		await user.click(screen.getByRole("button", { name: "Expose Redis 6379" }));
		expect(latest.tailnet?.tcp.map((r) => r.listen)).toEqual(["5432", "6379"]);
	});

	test("Add port is disabled at the limit", async () => {
		api();
		const full = Array.from({ length: MAX_TCP_FORWARDS }, (_, i) =>
			row(String(2000 + i)),
		);
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet({ tcp: full }) }}
			/>,
			inEnv,
		);
		await ready();
		expect(screen.getByRole("button", { name: "Add port" })).toBeDisabled();
	});

	test("shows the TCP problem", async () => {
		api();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet() }}
				errors={{ tailnetTcp: "Tailnet port 5432 is listed twice" }}
			/>,
			inEnv,
		);
		expect(
			await screen.findByText("Tailnet port 5432 is listed twice"),
		).toBeInTheDocument();
	});
});

describe("TailnetSection tags", () => {
	const policy = {
		defaultTags: ["tag:nebula-svc"],
		allowedTags: ["tag:db", "tag:ci", "tag:nebula-svc"],
	};

	test("an integration with an allow-list offers its tags as toggles, sorted, defaults excluded", async () => {
		api({ integration: policy });
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		const ci = await screen.findByRole("button", { name: "tag:ci" });
		expect(
			screen.getByText("Every device already carries tag:nebula-svc."),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "tag:nebula-svc" })).toBeNull();
		expect(screen.queryByLabelText("Add an extra tag")).toBeNull();
		expect(ci).toHaveAttribute("aria-pressed", "false");

		await user.click(screen.getByRole("button", { name: "tag:db" }));
		await user.click(ci);
		expect(latest.tailnet?.extraTags).toEqual(["tag:ci", "tag:db"]);
		expect(screen.getByRole("button", { name: "tag:ci" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);

		await user.click(screen.getByRole("button", { name: "tag:ci" }));
		expect(latest.tailnet?.extraTags).toEqual(["tag:db"]);
	});

	test("an integration that allows only its default tags says so", async () => {
		api({ integration: { allowedTags: [] } });
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		expect(
			await screen.findByText(/The integration allows no extra tags\./),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Add an extra tag")).toBeNull();
	});

	test("a staged tag the integration does not allow is flagged and removable", async () => {
		api({ integration: policy });
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet({ extraTags: ["tag:gone"] }) }}
			/>,
			inEnv,
		);
		expect(
			await screen.findByText(
				/tag:gone is not allowed by the integration\. Remove it or allow it in/,
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Remove tag:gone" }));
		expect(latest.tailnet?.extraTags).toEqual([]);
		expect(screen.queryByText(/not allowed by the integration/)).toBeNull();
	});

	test("several disallowed tags read in the plural", async () => {
		api({ integration: policy });
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet({ extraTags: ["tag:a", "tag:b"] }) }}
			/>,
			inEnv,
		);
		expect(
			await screen.findByText(
				/tag:a, tag:b are not allowed by the integration\. Remove them or allow them in/,
			),
		).toBeInTheDocument();
	});

	test("without a readable integration tags are typed, added with the button or Enter", async () => {
		api({ admin: false });
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		const input = await screen.findByLabelText("Add an extra tag");
		const add = screen.getByRole("button", { name: "Add tag" });
		expect(add).toBeDisabled();
		expect(
			screen.getByText("Added to the integration's default tags."),
		).toBeInTheDocument();

		await user.type(input, "  tag:zeta ");
		expect(add).toBeEnabled();
		await user.click(add);
		expect(latest.tailnet?.extraTags).toEqual(["tag:zeta"]);
		expect(input).toHaveValue("");

		await user.type(input, "tag:alpha{Enter}");
		expect(latest.tailnet?.extraTags).toEqual(["tag:alpha", "tag:zeta"]);
		// Duplicates collapse.
		await user.type(input, "tag:alpha{Enter}");
		expect(latest.tailnet?.extraTags).toEqual(["tag:alpha", "tag:zeta"]);
		// Enter on blank text adds nothing.
		await user.type(input, "   {Enter}");
		expect(latest.tailnet?.extraTags).toEqual(["tag:alpha", "tag:zeta"]);

		await user.click(screen.getByRole("button", { name: "Remove tag:alpha" }));
		expect(latest.tailnet?.extraTags).toEqual(["tag:zeta"]);
	});

	test("shows the tag problem", async () => {
		api();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet() }}
				errors={{ tailnetTags: "Tags must look like tag:name" }}
			/>,
			inEnv,
		);
		expect(
			await screen.findByText("Tags must look like tag:name"),
		).toBeInTheDocument();
	});
});

describe("TailnetSection tailnet only and Funnel", () => {
	test("Tailnet only stages hidePublic and the cue follows", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		await ready();
		expect(
			screen.getByText("Off: public domains keep working"),
		).toBeInTheDocument();
		await user.click(sw("Tailnet only"));
		expect(latest.tailnet?.hidePublic).toBe(true);
		expect(
			screen.getByText("On: public domains are not routed"),
		).toBeInTheDocument();
	});

	test("Advanced is closed unless Funnel is already on", async () => {
		api();
		const { unmount } = await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		await ready();
		expect(screen.getByText("Advanced").closest("details")).not.toHaveAttribute(
			"open",
		);
		unmount();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet({ funnel: true }) }}
			/>,
			inEnv,
		);
		await ready();
		expect(screen.getByText("Advanced").closest("details")).toHaveAttribute(
			"open",
		);
		// The header and the row both carry the public mark.
		expect(
			screen.getAllByText("Public via Funnel").length,
		).toBeGreaterThanOrEqual(2);
		expect(
			screen.getByText("On: anyone on the internet can reach it"),
		).toBeInTheDocument();
	});

	test("turning Funnel on asks for the hostname and a hold, then stages it", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet({ hostname: " Edge-Web " }) }}
			/>,
			inEnv,
		);
		await ready();
		await user.click(sw(/^Funnel/));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent(
			"Publish this service on the internet with Funnel?",
		);
		expect(dialog).toHaveTextContent(
			"Anyone on the internet can reach this service",
		);
		// The typed name is the hostname as the control plane will see it.
		expect(dialog).toHaveTextContent("Type edge-web to confirm");
		const hold$ = within(dialog).getByRole("button", {
			name: "Hold to turn on funnel",
		});
		// Holding before the name is typed does nothing.
		fireEvent.keyDown(hold$, { key: "Enter" });
		expect(latest.tailnet?.funnel).toBe(false);

		await user.type(within(dialog).getByRole("textbox"), "edge-web");
		await holdToConfirmFunnel(hold$);
		expect(
			screen.getByText("On: anyone on the internet can reach it"),
		).toBeInTheDocument();
	});

	test("the Funnel confirmation uses the derived hostname when none is typed", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		await ready();
		await user.click(sw(/^Funnel/));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Type web-production to confirm");
	});

	test("cancelling the Funnel confirmation leaves it off", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={base()} initial={{ tailnet: tailnet() }} />,
			inEnv,
		);
		await ready();
		await user.click(sw(/^Funnel/));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(latest.tailnet?.funnel).toBe(false);
		expect(sw(/^Funnel/)).not.toBeChecked();
	});

	test("turning Funnel off needs no confirmation", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet({ funnel: true }) }}
			/>,
			inEnv,
		);
		await ready();
		await user.click(sw(/^Funnel/));
		expect(screen.queryByRole("alertdialog")).toBeNull();
		expect(latest.tailnet?.funnel).toBe(false);
	});

	test("shows the Funnel problem", async () => {
		api();
		await renderWithProviders(
			<Harness
				service={base()}
				initial={{ tailnet: tailnet() }}
				errors={{ tailnetFunnel: "Funnel needs the web exposure" }}
			/>,
			inEnv,
		);
		expect(
			await screen.findByText("Funnel needs the web exposure"),
		).toBeInTheDocument();
	});
});

describe("TailnetSection status", () => {
	const onTailnet = (v: Partial<TailnetView> = {}, config?: TailnetConfig) =>
		base({
			tailnet: view(v),
			config: {
				...makeService().config,
				tailnet: config ?? {
					enabled: true,
					web: true,
					hostname: "web-production",
				},
			},
		});

	test("an online node shows its state, trimmed name, addresses and an Open link", async () => {
		api();
		await renderWithProviders(<Harness service={onTailnet()} />, inEnv);
		const status = await screen.findByRole("region", {
			name: "Tailnet status",
		});
		expect(within(status).getByText("Online")).toBeInTheDocument();
		expect(
			within(status).getByText("web-production.tail1234.ts.net"),
		).toBeInTheDocument();
		expect(
			within(status).getByText("100.64.0.5 fd7a:115c::5"),
		).toBeInTheDocument();
		expect(
			within(status).getByRole("button", { name: "Copy tailnet name" }),
		).toBeInTheDocument();
		expect(
			within(status).getByRole("button", { name: "Copy tailnet addresses" }),
		).toBeInTheDocument();
		const open = within(status).getByRole("link", { name: "Open" });
		expect(open).toHaveAttribute(
			"href",
			"https://web-production.tail1234.ts.net",
		);
		expect(open).toHaveAttribute("target", "_blank");
		expect(
			within(status).queryByRole("button", { name: "Re-provision" }),
		).toBeNull();
		expect(within(status).queryByText("Public via Funnel")).toBeNull();
		expect(within(status).queryByText("Tailnet only")).toBeNull();
	});

	test("an offline node has no Open link; provisioning shows an ellipsis", async () => {
		api();
		const { unmount } = await renderWithProviders(
			<Harness service={onTailnet({ state: "offline" })} />,
			inEnv,
		);
		const status = await screen.findByRole("region", {
			name: "Tailnet status",
		});
		expect(within(status).getByText("Offline")).toBeInTheDocument();
		expect(within(status).queryByRole("link", { name: "Open" })).toBeNull();
		unmount();
		await renderWithProviders(
			<Harness
				service={onTailnet({
					state: "provisioning",
					fqdn: "",
					ips: null,
					url: "",
				})}
			/>,
			inEnv,
		);
		const next = await screen.findByRole("region", { name: "Tailnet status" });
		expect(within(next).getByText("Provisioning…")).toBeInTheDocument();
		// No name, no addresses yet: no copy buttons.
		expect(within(next).queryByRole("button", { name: /Copy/ })).toBeNull();
	});

	test("an online node without a url has no Open link", async () => {
		api();
		await renderWithProviders(
			<Harness service={onTailnet({ url: "" })} />,
			inEnv,
		);
		const status = await screen.findByRole("region", {
			name: "Tailnet status",
		});
		expect(within(status).queryByRole("link", { name: "Open" })).toBeNull();
	});

	test("marks Funnel, tailnet-only and when the node last reported", async () => {
		api();
		await renderWithProviders(
			<Harness
				service={onTailnet({
					funnel: true,
					hidePublic: true,
					observedAt: "2026-01-01T00:00:00Z",
				})}
			/>,
			inEnv,
		);
		const status = await screen.findByRole("region", {
			name: "Tailnet status",
		});
		expect(within(status).getByText("Public via Funnel")).toBeInTheDocument();
		expect(within(status).getByText("Tailnet only")).toBeInTheDocument();
		expect(within(status).getByText(/reported/)).toBeInTheDocument();
		expect(status.querySelector("time")).toHaveAttribute(
			"datetime",
			"2026-01-01T00:00:00Z",
		);
	});

	test("a node that needs attention shows its message and offers Re-provision", async () => {
		api();
		await renderWithProviders(
			<Harness
				service={onTailnet({
					state: "needs_attention",
					message: "auth key expired",
				})}
			/>,
			inEnv,
		);
		const status = await screen.findByRole("region", {
			name: "Tailnet status",
		});
		expect(within(status).getByText("Needs attention")).toBeInTheDocument();
		expect(within(status).getByText("auth key expired")).toBeInTheDocument();
		expect(
			within(status).getByRole("button", { name: "Re-provision" }),
		).toBeEnabled();
	});

	test("a node flagged for re-provisioning offers it even while online", async () => {
		api();
		await renderWithProviders(
			<Harness
				service={onTailnet({ needsReprovision: true, message: "tags changed" })}
			/>,
			inEnv,
		);
		const status = await screen.findByRole("region", {
			name: "Tailnet status",
		});
		expect(within(status).getByText("tags changed")).toBeInTheDocument();
		expect(
			within(status).getByRole("button", { name: "Re-provision" }),
		).toBeInTheDocument();
	});

	test("Re-provision asks first, then posts with an idempotency key and says it started", async () => {
		let key: string | null = null;
		const stub: ApiStub = api({
			routes: {
				"POST /services/:serviceId/tailnet/reprovision": (request) => {
					key = request.headers.get("Idempotency-Key");
					return new Response(null, { status: 202 });
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				service={onTailnet({ state: "needs_attention", message: "x" })}
			/>,
			inEnv,
		);
		await user.click(
			await screen.findByRole("button", { name: "Re-provision" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Re-provision the tailnet device?");
		expect(dialog).toHaveTextContent(
			"Creates a new device with a new tailnet IP. The old device is deleted.",
		);
		expect(stub.calls.some((c) => c.method === "POST")).toBe(false);
		await user.click(
			within(dialog).getByRole("button", { name: "Re-provision" }),
		);
		expect(await screen.findByText("Re-provisioning")).toBeInTheDocument();
		expect(
			screen.getByText(
				"A new device is being created; the status updates here.",
			),
		).toBeInTheDocument();
		const posts = stub.calls.filter((c) => c.method === "POST");
		expect(posts.map((c) => c.path)).toEqual([
			"/services/svc-web/tailnet/reprovision",
		]);
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
	});

	test("declining the Re-provision question sends nothing", async () => {
		const stub = api();
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={onTailnet({ needsReprovision: true })} />,
			inEnv,
		);
		await user.click(
			await screen.findByRole("button", { name: "Re-provision" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(stub.calls.some((c) => c.method === "POST")).toBe(false);
	});

	test("a tailnet error code from Re-provision gets its friendly sentence", async () => {
		api({
			routes: {
				"POST /services/:serviceId/tailnet/reprovision": problem(422, {
					code: "tailnet_not_configured",
					detail: "no integration",
				}),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={onTailnet({ needsReprovision: true })} />,
			inEnv,
		);
		await user.click(
			await screen.findByRole("button", { name: "Re-provision" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Re-provision" }),
		);
		expect(
			await screen.findByText("Could not re-provision"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Connect Tailscale or Headscale in Team settings → Integrations first.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("no integration")).toBeNull();
	});

	test("any other Re-provision failure shows the server's message", async () => {
		api({
			routes: {
				"POST /services/:serviceId/tailnet/reprovision": problem(500, {
					code: "internal",
					detail: "coordinator timed out",
				}),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={onTailnet({ needsReprovision: true })} />,
			inEnv,
		);
		await user.click(
			await screen.findByRole("button", { name: "Re-provision" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Re-provision" }),
		);
		expect(
			await screen.findByText("Could not re-provision"),
		).toBeInTheDocument();
		expect(screen.getByText("coordinator timed out")).toBeInTheDocument();
	});

	test("lists what the next Deploy changes against the node as reported", async () => {
		api();
		await renderWithProviders(
			<Harness
				service={onTailnet(
					{ hostname: "web-production", web: true, funnel: false },
					{
						enabled: true,
						hostname: "renamed",
						web: false,
						funnel: false,
						hidePublic: true,
						tcp: [{ listen: 5432, target: 5432 }],
					},
				)}
			/>,
			inEnv,
		);
		expect(
			await screen.findByText(
				"Staged, applies on the next Deploy: Hostname web-production → renamed; Web exposure off; TCP ports; Tailnet only on.",
			),
		).toBeInTheDocument();
	});

	test("says the device will be created when it has no node yet", async () => {
		api();
		const service = base({
			config: {
				...makeService().config,
				tailnet: { enabled: true, web: true },
			},
		});
		await renderWithProviders(<Harness service={service} />, inEnv);
		expect(
			await screen.findByText(
				"Staged, applies on the next Deploy: The device is created.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("region", { name: "Tailnet status" })).toBeNull();
	});

	test("says the device will be removed when the saved config turned it off", async () => {
		api();
		const service = base({
			tailnet: view(),
			config: { ...makeService().config, tailnet: { enabled: false } },
		});
		await renderWithProviders(<Harness service={service} />, inEnv);
		expect(
			await screen.findByText(
				"Staged, applies on the next Deploy: The device is removed from your tailnet.",
			),
		).toBeInTheDocument();
	});

	test("shows no staged note when the node matches the saved config", async () => {
		api();
		await renderWithProviders(<Harness service={onTailnet()} />, inEnv);
		await screen.findByRole("region", { name: "Tailnet status" });
		expect(screen.queryByText(/applies on the next Deploy/)).toBeNull();
	});
});
