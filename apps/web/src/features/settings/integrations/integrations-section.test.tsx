import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { IntegrationsSection } from "./integrations-section";

const notConnected = make("TailscaleIntegration", {
	allowedTags: null,
	clientId: "",
	configured: false,
	defaultTags: null,
	headscaleCredential: "apikey",
	headscaleUser: "",
	httpsEnabled: null,
	lastTest: { at: null, ok: null },
	loginServer: "",
	provider: "tailscale_oauth",
	secretSet: false,
	tailnet: "-",
});

const cloudflare = make("CloudflareConnectionView", {
	accountId: "",
	accountName: "",
	accounts: null,
	configured: false,
	status: "",
	statusMessage: "",
	tokenExpiresAt: null,
	verifiedAt: null,
	warnings: null,
	zoneCount: 0,
	zonesSyncedAt: null,
});

describe("IntegrationsSection", () => {
	test("lists the four integration groups, each with its own content", async () => {
		const api = stubApi({
			"GET /git/connections": { body: { items: [] } },
			"GET /secret-managers": { body: { items: [] } },
			"GET /integrations/tailscale": { body: notConnected },
			"GET /integrations/cloudflare": { body: cloudflare },
			"GET /dns/credentials": { body: { items: [] } },
			"GET /platform-services": problem(500, { detail: "none" }),
			"GET /servers": problem(500, { detail: "none" }),
		});
		await renderWithProviders(<IntegrationsSection />);

		const git = screen.getByRole("region", { name: "Git providers" });
		expect(
			await within(git).findByText("Deploy on every push"),
		).toBeInTheDocument();
		const secrets = screen.getByRole("region", { name: "Secret managers" });
		expect(
			await within(secrets).findByText("Keep secrets where they already live"),
		).toBeInTheDocument();
		const tailscale = screen.getByRole("region", { name: "Tailscale" });
		expect(
			await within(tailscale).findByRole("button", {
				name: "Connect Tailscale",
			}),
		).toBeInTheDocument();
		const cf = screen.getByRole("region", { name: "Cloudflare" });
		expect(
			await within(cf).findByRole("button", { name: "Connect Cloudflare" }),
		).toBeInTheDocument();

		expect(
			screen
				.getAllByRole("region")
				.map((r) => r.getAttribute("aria-labelledby"))
				.filter((id) => id?.startsWith("integration-")),
		).toEqual([
			"integration-git",
			"integration-secret-managers",
			"integration-tailscale",
			"integration-cloudflare",
		]);
		expect(api.calls.filter((c) => c.method !== "GET")).toEqual([]);
	});

	test("one group failing to load does not take the others down", async () => {
		stubApi({
			"GET /git/connections": { body: { items: [] } },
			"GET /secret-managers": problem(500, {
				title: "x",
				detail: "boom",
				code: "internal",
			}),
			"GET /integrations/tailscale": problem(403, {
				title: "x",
				detail: "no",
				code: "forbidden",
			}),
			"GET /integrations/cloudflare": { body: cloudflare },
			"GET /dns/credentials": { body: { items: [] } },
			"GET /platform-services": problem(500, { detail: "none" }),
			"GET /servers": problem(500, { detail: "none" }),
		});
		await renderWithProviders(<IntegrationsSection />);
		expect(
			await screen.findByText("Secret managers did not load"),
		).toBeInTheDocument();
		expect(
			await screen.findByText("Only administrators can manage Tailscale"),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("button", { name: "Connect Cloudflare" }),
		).toBeInTheDocument();
		expect(await screen.findByText("Deploy on every push")).toBeInTheDocument();
	});
});
