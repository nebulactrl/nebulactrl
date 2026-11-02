import { afterEach, describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { getMySecurityQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import type { SecurityBody } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { SecuritySection } from "./security-section";

afterEach(() => {
	window.history.replaceState(null, "", "/");
});

const me = {
	id: "u1",
	name: "Ada Lovelace",
	email: "ada@example.com",
	instanceAdmin: false,
	workspaceId: "w1",
	createdAt: "2026-01-01T00:00:00Z",
	principal: { type: "user", label: "Ada", scopes: [] },
};

const security = (over: Partial<SecurityBody> = {}): SecurityBody => ({
	enrollmentNeeded: false,
	hasPassword: true,
	identities: null,
	isInstanceAdmin: false,
	passkeys: null,
	recoveryCodesLeft: 0,
	requiredByTeams: null,
	sessionMfa: false,
	totpEnabled: false,
	totpPending: false,
	...over,
});

const identity = {
	id: "i1",
	email: "ada@corp.example",
	linkedAt: "2026-01-01T00:00:00Z",
	providerId: "p-okta",
	providerKind: "oidc",
	providerName: "Okta",
};

function stubSection(
	sec: SecurityBody,
	providers: { id: string; kind: "oidc"; name: string }[] = [],
) {
	return stubApi({
		"GET /me": { body: me },
		"GET /me/security": { body: sec },
		"GET /auth/providers": { body: { providers } },
	});
}

describe("SecuritySection", () => {
	test("shows passkeys and two-step verification for this account", async () => {
		const api = stubSection(security());
		await renderWithProviders(<SecuritySection />);
		expect(await screen.findByText("Passkeys")).toBeInTheDocument();
		expect(screen.getByText("Two-step verification")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Turn on" })).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("hides single sign-on when there is neither a linked account nor a provider", async () => {
		stubSection(security());
		await renderWithProviders(<SecuritySection />);
		await screen.findByText("Passkeys");
		expect(screen.queryByText("Single sign-on")).not.toBeInTheDocument();
	});

	test("shows single sign-on with a link button when a provider is set up", async () => {
		stubSection(security(), [{ id: "p-okta", kind: "oidc", name: "Okta" }]);
		await renderWithProviders(<SecuritySection />);
		expect(await screen.findByText("Single sign-on")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Link your SSO account" }),
		).toBeInTheDocument();
	});

	test("lists linked accounts even when no provider is currently offered", async () => {
		stubSection(security({ identities: [identity] }));
		await renderWithProviders(<SecuritySection />);
		expect(await screen.findByText("ada@corp.example")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Unlink" })).toBeInTheDocument();
	});

	test("the signed-in email is what the recovery codes are labelled with", async () => {
		stubApi({
			"GET /me": { body: me },
			"GET /me/security": {
				body: security({ totpEnabled: true, recoveryCodesLeft: 5 }),
			},
			"GET /auth/providers": { body: { providers: [] } },
			"POST /me/2fa/recovery-codes": { body: { recoveryCodes: ["aaaa-0001"] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<SecuritySection />);
		await user.click(
			await screen.findByRole("button", { name: "New recovery codes" }),
		);
		await user.type(
			await screen.findByLabelText("Authenticator or recovery code"),
			"123456",
		);
		await user.click(screen.getByRole("button", { name: "Create new codes" }));
		expect(await screen.findByText("aaaa-0001")).toBeInTheDocument();
	});

	test("explains a load failure, and Try again recovers", async () => {
		let attempts = 0;
		stubApi({
			"GET /me": { body: me },
			"GET /auth/providers": { body: { providers: [] } },
			"GET /me/security": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json(security());
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SecuritySection />);
		expect(
			await screen.findByText("Security settings did not load"),
		).toBeInTheDocument();
		expect(screen.queryByText("Passkeys")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByText("Passkeys")).toBeInTheDocument();
	});

	test("explains a failure to load the profile as well", async () => {
		stubApi({
			"GET /me": {
				status: 500,
				body: { status: 500, title: "x", detail: "boom", code: "internal" },
			},
			"GET /me/security": { body: security() },
			"GET /auth/providers": { body: { providers: [] } },
		});
		await renderWithProviders(<SecuritySection />);
		expect(
			await screen.findByText("Security settings did not load"),
		).toBeInTheDocument();
	});
});

describe("SecuritySection returning from a link", () => {
	test("#sso=linked announces success, clears the fragment and refreshes cached accounts", async () => {
		window.history.replaceState(null, "", "/account/security#sso=linked");
		// Cached data that never goes stale: only the invalidation can refresh it.
		const queryClient = new QueryClient({
			defaultOptions: {
				queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
			},
		});
		queryClient.setQueryData(getMySecurityQueryKey(), security());
		const api = stubSection(security({ identities: [identity] }));
		await renderWithProviders(<SecuritySection />, { queryClient });
		expect(
			await screen.findByText("Single sign-on account linked"),
		).toBeInTheDocument();
		expect(window.location.hash).toBe("");
		expect(window.location.pathname).toBe("/account/security");
		expect(await screen.findByText("ada@corp.example")).toBeInTheDocument();
		expect(api.calls.filter((c) => c.path === "/me/security")).toHaveLength(1);
	});

	test("#sso-error shows what went wrong and does not reload the accounts", async () => {
		window.history.replaceState(
			null,
			"",
			"/account/security?tab=x#sso-error=identity_conflict",
		);
		const api = stubSection(security());
		await renderWithProviders(<SecuritySection />);
		expect(
			await screen.findByText("Could not link the account"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"That account at the provider is already linked to another NebulaCtrl account.",
			),
		).toBeInTheDocument();
		expect(window.location.hash).toBe("");
		expect(window.location.search).toBe("?tab=x");
		await screen.findByText("Passkeys");
		expect(api.calls.filter((c) => c.path === "/me/security")).toHaveLength(1);
		expect(
			screen.queryByText("Single sign-on account linked"),
		).not.toBeInTheDocument();
	});

	test("an unrelated fragment is left alone", async () => {
		window.history.replaceState(null, "", "/account/security#other");
		stubSection(security());
		await renderWithProviders(<SecuritySection />);
		await screen.findByText("Passkeys");
		expect(window.location.hash).toBe("#other");
	});
});
