import { describe, expect, mock, setDefaultTimeout, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { listAuthProvidersQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import type { ProviderBody } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { ProviderDialog } from "./provider-dialog";

setDefaultTimeout(30_000);

const problem = (status: number, detail: string, code = "bad_request") => ({
	status,
	body: { status, title: "Error", detail, code },
});

const UUID =
	/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

const existing: ProviderBody = {
	id: "p1",
	name: "Okta",
	kind: "oidc",
	issuerUrl: "https://dev-1.okta.com/oauth2/default",
	clientId: "cid",
	allowedDomains: ["corp.example"],
	scopes: ["groups"],
	trustMfa: true,
	enabled: false,
	createdAt: "2026-01-01T00:00:00Z",
	redirectUri: "https://nebula.example/api/v1/auth/oidc/p1/callback",
};

const system = {
	"GET /system": {
		body: make("SystemInfo", { publicUrl: "https://nebula.example" }),
	},
};

function renderDialog(
	props: Partial<Parameters<typeof ProviderDialog>[0]> = {},
	queryClient = testQueryClient(),
) {
	return renderWithProviders(
		<ProviderDialog open onOpenChange={() => {}} {...props} />,
		{ queryClient },
	);
}

/** Fills a field in one go: typing long URLs key by key is slow. */
async function paste(
	user: ReturnType<typeof userEvent.setup>,
	field: HTMLElement,
	text: string,
) {
	await user.click(field);
	await user.paste(text);
}

/** The live region under Test connection (the dialog has another, empty one). */
function statusRegion() {
	const region = document.querySelector('[role="status"].text-xs');
	if (!region) throw new Error("test connection status region not rendered");
	return region;
}

/** The role=switch element for a label (the label also names a hidden checkbox). */
function switchNamed(label: string) {
	const found = screen
		.getAllByLabelText(label)
		.find((el) => el.getAttribute("role") === "switch");
	if (!found) throw new Error(`no switch labelled ${label}`);
	return found;
}

async function fillRequired(user: ReturnType<typeof userEvent.setup>) {
	await user.type(screen.getByLabelText("Name"), "Company SSO");
	await paste(
		user,
		screen.getByLabelText("Issuer URL"),
		"https://login.example.com/realms/main",
	);
	await user.type(screen.getByLabelText("Client ID"), "client-1");
	await user.type(screen.getByLabelText("Client secret"), "s3cret");
}

describe("ProviderDialog: adding", () => {
	test("opens on the generic OpenID Connect preset with every provider choosable", async () => {
		stubApi(system);
		await renderDialog();
		expect(
			await screen.findByRole("heading", { name: "Add a sign-in provider" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("radio", { name: "Any OpenID Connect provider" }),
		).toBeChecked();
		for (const label of [
			"Google",
			"Microsoft Entra ID",
			"Okta",
			"Auth0",
			"authentik",
			"Keycloak",
			"Zitadel",
			"Pocket ID",
			"GitLab",
			"GitHub (OAuth)",
		]) {
			expect(screen.getByRole("radio", { name: label })).not.toBeChecked();
		}
		expect(screen.getByLabelText("Client secret")).toHaveAttribute(
			"placeholder",
			"",
		);
	});

	test("shows the redirect URI to register, built from the public URL and a reserved id", async () => {
		stubApi(system);
		await renderDialog();
		const uri = await screen.findByText(
			/^https:\/\/nebula\.example\/api\/v1\/auth\/oidc\/.+\/callback$/,
		);
		const id = /\/oidc\/(.+)\/callback/.exec(uri.textContent ?? "")?.[1];
		expect(id).toMatch(UUID);
		expect(
			screen.getByText(/ID is reserved now, so the address will not change/),
		).toBeInTheDocument();
		expect(
			screen.queryByText(/NEBULACTRL_PUBLIC_URL is not set/),
		).not.toBeInTheDocument();
	});

	test("without a configured public URL it uses this page's address and says so", async () => {
		stubApi({ "GET /system": { body: make("SystemInfo", { publicUrl: "" }) } });
		await renderDialog();
		expect(
			await screen.findByText(/NEBULACTRL_PUBLIC_URL is not set/),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/^http:\/\/localhost\/api\/v1\/auth\/oidc\/.+\/callback$/,
			),
		).toBeInTheDocument();
	});

	test("choosing a preset fills name and issuer, names the redirect field and links the guide", async () => {
		stubApi(system);
		const user = userEvent.setup();
		await renderDialog();
		await user.click(await screen.findByRole("radio", { name: "Okta" }));
		expect(screen.getByLabelText("Name")).toHaveValue("Okta");
		expect(screen.getByLabelText("Issuer URL")).toHaveValue(
			"https://{domain}/oauth2/default",
		);
		expect(screen.getByText("Sign-in redirect URIs")).toBeInTheDocument();
		expect(screen.getByText(/Create App Integration/)).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: /Okta documentation/ }),
		).toHaveAttribute(
			"href",
			"https://help.okta.com/oie/en-us/content/topics/apps/apps_app_integration_wizard_oidc.htm",
		);
		// Okta has no hosted console page to link to.
		expect(
			screen.queryByRole("link", { name: /Open Okta to create the client/ }),
		).not.toBeInTheDocument();
		expect(
			screen.getByText(/dev-123456\.okta\.com/, { exact: false }),
		).toBeInTheDocument();
	});

	test("a preset with a console page links to it", async () => {
		stubApi(system);
		const user = userEvent.setup();
		await renderDialog();
		await user.click(await screen.findByRole("radio", { name: "Google" }));
		expect(screen.getByLabelText("Issuer URL")).toHaveValue(
			"https://accounts.google.com",
		);
		expect(
			screen.getByRole("link", { name: /Open Google to create the client/ }),
		).toHaveAttribute(
			"href",
			"https://console.developers.google.com/auth/clients",
		);
	});

	test("an unreplaced {placeholder} blocks Test connection and saving, with what to put there", async () => {
		const api = stubApi(system);
		const user = userEvent.setup();
		await renderDialog();
		await user.click(
			await screen.findByRole("radio", { name: "Microsoft Entra ID" }),
		);
		expect(
			screen.getByRole("button", { name: "Test connection" }),
		).toBeDisabled();
		expect(
			screen.getByText(/Replace the highlighted part of/),
		).toBeInTheDocument();
		await user.type(screen.getByLabelText("Client ID"), "c");
		await user.type(screen.getByLabelText("Client secret"), "s");
		await user.click(screen.getByRole("button", { name: "Add provider" }));
		expect(
			await screen.findByText(
				/Replace \{tenant\} in the issuer URL: your Directory \(tenant\) ID/,
			),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method !== "GET")).toEqual([]);
	});

	test("warns about an Entra issuer that can never match", async () => {
		stubApi(system);
		const user = userEvent.setup();
		await renderDialog();
		await user.click(
			await screen.findByRole("radio", { name: "Microsoft Entra ID" }),
		);
		const issuer = screen.getByLabelText("Issuer URL");
		await user.clear(issuer);
		await paste(user, issuer, "https://login.microsoftonline.com/common/v2.0");
		expect(
			screen.getByText(/Entra reports a tenant-specific issuer for "common"/),
		).toBeInTheDocument();
		await user.clear(issuer);
		await paste(
			user,
			issuer,
			"https://login.microsoftonline.com/0b9c1f56-1111-4222-8333-444455556666/v2.0",
		);
		expect(
			screen.queryByText(/Entra reports a tenant-specific issuer/),
		).not.toBeInTheDocument();
	});

	test("submitting an incomplete form explains the first problem and sends nothing", async () => {
		const api = stubApi(system);
		const user = userEvent.setup();
		await renderDialog();
		await screen.findByRole("heading", { name: "Add a sign-in provider" });
		expect(
			screen.queryByText("Give the provider a name."),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Add provider" }));
		expect(
			await screen.findByText("Give the provider a name."),
		).toBeInTheDocument();
		await user.type(screen.getByLabelText("Name"), "SSO");
		expect(await screen.findByText("Enter the client ID.")).toBeInTheDocument();
		await user.type(screen.getByLabelText("Client ID"), "c");
		expect(
			await screen.findByText("Enter the client secret."),
		).toBeInTheDocument();
		await user.type(screen.getByLabelText("Client secret"), "s");
		expect(
			await screen.findByText("Enter the issuer URL."),
		).toBeInTheDocument();
		await paste(
			user,
			screen.getByLabelText("Issuer URL"),
			"ftp://login.example.com",
		);
		await user.click(screen.getByRole("button", { name: "Add provider" }));
		expect(
			await screen.findByText("The URL must start with https://."),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method !== "GET")).toEqual([]);
	});

	test("creates the provider with the reserved id, then shows the redirect URI to register", async () => {
		const created: ProviderBody = {
			...existing,
			id: "new-id",
			name: "Company SSO",
			redirectUri: "https://nebula.example/api/v1/auth/oidc/new-id/callback",
		};
		const api = stubApi({
			...system,
			"POST /auth-providers": { status: 201, body: created },
		});
		const onOpenChange = mock((_open: boolean) => {});
		const queryClient = testQueryClient();
		queryClient.setQueryData(listAuthProvidersQueryKey(), { items: [] });
		const user = userEvent.setup();
		await renderDialog({ onOpenChange }, queryClient);
		await screen.findByText(
			/^https:\/\/nebula\.example\/api\/v1\/auth\/oidc\//,
		);
		const shown = screen.getByText(
			/\/api\/v1\/auth\/oidc\/.+\/callback/,
		).textContent;

		await fillRequired(user);
		await user.type(screen.getByLabelText("Extra scopes"), "groups  offline");
		await user.type(
			screen.getByLabelText("Allowed email domains"),
			"@Corp.Example, other.io",
		);
		await user.click(screen.getByText("Trust the provider's MFA"));
		await user.click(screen.getByRole("button", { name: "Add provider" }));

		expect(await screen.findByText("Company SSO is ready")).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/auth-providers");
		expect(post?.body).toEqual({
			id: expect.stringMatching(UUID),
			kind: "oidc",
			name: "Company SSO",
			issuerUrl: "https://login.example.com/realms/main",
			clientId: "client-1",
			clientSecret: "s3cret",
			scopes: ["groups", "offline"],
			allowedDomains: ["corp.example", "other.io"],
			trustMfa: true,
			enabled: true,
		});
		// The id sent is the one in the callback address that was shown before saving.
		const sentId = /\/oidc\/(.+)\/callback/.exec(shown ?? "")?.[1];
		expect(post?.body).toMatchObject({ id: sentId });

		expect(screen.getByText(created.redirectUri)).toBeInTheDocument();
		expect(screen.getByText(/in the field Redirect URI\./)).toBeInTheDocument();
		await waitFor(() =>
			expect(
				queryClient.getQueryState(listAuthProvidersQueryKey())?.isInvalidated,
			).toBe(true),
		);
		expect(onOpenChange).not.toHaveBeenCalled();
		await user.click(screen.getByRole("button", { name: "Done" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("shows the server's reason when saving fails and keeps the form", async () => {
		stubApi({
			...system,
			"POST /auth-providers": problem(
				409,
				"That provider id is taken.",
				"provider_id_taken",
			),
		});
		const user = userEvent.setup();
		await renderDialog();
		await screen.findByRole("heading", { name: "Add a sign-in provider" });
		await fillRequired(user);
		await user.click(screen.getByRole("button", { name: "Add provider" }));
		expect(
			await screen.findByText("That provider id is taken."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("Company SSO");
		expect(screen.queryByText("Company SSO is ready")).not.toBeInTheDocument();
	});

	test("Cancel closes without a request", async () => {
		const api = stubApi(system);
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderDialog({ onOpenChange });
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls.filter((c) => c.method !== "GET")).toEqual([]);
	});
});

describe("ProviderDialog: GitHub", () => {
	test("hides what OIDC alone needs and sends a GitHub provider that never trusts MFA", async () => {
		const api = stubApi({
			...system,
			"POST /auth-providers": {
				status: 201,
				body: { ...existing, kind: "github", name: "GitHub" },
			},
		});
		const user = userEvent.setup();
		await renderDialog();
		await user.click(
			await screen.findByRole("radio", { name: "GitHub (OAuth)" }),
		);
		expect(screen.getByLabelText("Name")).toHaveValue("GitHub");
		expect(
			screen.getByLabelText("GitHub Enterprise Server URL"),
		).toHaveAttribute("placeholder", "Leave empty for github.com");
		expect(
			screen.queryByRole("button", { name: "Test connection" }),
		).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Extra scopes")).not.toBeInTheDocument();
		expect(
			screen.queryByLabelText("Trust the provider's MFA"),
		).not.toBeInTheDocument();
		expect(screen.getByText("Authorization callback URL")).toBeInTheDocument();

		await user.type(screen.getByLabelText("Client ID"), "gh-id");
		await user.type(screen.getByLabelText("Client secret"), "gh-secret");
		await paste(
			user,
			screen.getByLabelText("GitHub Enterprise Server URL"),
			"https://ghe.example.com//",
		);
		await user.click(screen.getByRole("button", { name: "Add provider" }));
		await screen.findByText(/is ready/);
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			id: expect.stringMatching(UUID),
			kind: "github",
			name: "GitHub",
			issuerUrl: "https://ghe.example.com",
			clientId: "gh-id",
			clientSecret: "gh-secret",
			scopes: [],
			allowedDomains: [],
			trustMfa: false,
			enabled: true,
		});
	});

	test("an empty address is fine for github.com", async () => {
		const api = stubApi({
			...system,
			"POST /auth-providers": {
				status: 201,
				body: { ...existing, kind: "github" },
			},
		});
		const user = userEvent.setup();
		await renderDialog();
		await user.click(
			await screen.findByRole("radio", { name: "GitHub (OAuth)" }),
		);
		await user.type(screen.getByLabelText("Client ID"), "gh-id");
		await user.type(screen.getByLabelText("Client secret"), "gh-secret");
		await user.click(screen.getByRole("button", { name: "Add provider" }));
		await screen.findByText(/is ready/);
		expect(api.calls.find((c) => c.method === "POST")?.body).toMatchObject({
			kind: "github",
			issuerUrl: "",
		});
	});
});

describe("ProviderDialog: Test connection", () => {
	const typeIssuer = async (user: ReturnType<typeof userEvent.setup>) => {
		await screen.findByRole("heading", { name: "Add a sign-in provider" });
		await paste(
			user,
			screen.getByLabelText("Issuer URL"),
			"https://login.example.com/realms/main",
		);
	};

	test("is disabled until an issuer is entered", async () => {
		stubApi(system);
		await renderDialog();
		expect(
			await screen.findByRole("button", { name: "Test connection" }),
		).toBeDisabled();
	});

	test("posts the trimmed issuer and reports what was found", async () => {
		const api = stubApi({
			...system,
			"POST /instance/sign-in-providers/test": {
				body: {
					ok: true,
					issuer: "https://login.example.com/realms/main",
					authorizationEndpoint: "https://login.example.com/realms/main/auth",
				},
			},
		});
		const user = userEvent.setup();
		await renderDialog();
		await typeIssuer(user);
		await user.click(screen.getByRole("button", { name: "Test connection" }));
		await waitFor(() => expect(statusRegion()).toHaveTextContent("Found"));
		expect(statusRegion()).toHaveTextContent(
			"Found https://login.example.com/realms/main. People will be sent to https://login.example.com/realms/main/auth.",
		);
		const call = api.calls.find((c) => c.method === "POST");
		expect(call?.path).toBe("/instance/sign-in-providers/test");
		expect(call?.body).toEqual({
			issuerUrl: "https://login.example.com/realms/main",
		});
	});

	test("shows the reason when the issuer cannot be used", async () => {
		stubApi({
			...system,
			"POST /instance/sign-in-providers/test": {
				body: { ok: false, code: "host_not_found", message: "No such host." },
			},
		});
		const user = userEvent.setup();
		await renderDialog();
		await typeIssuer(user);
		await user.click(screen.getByRole("button", { name: "Test connection" }));
		await waitFor(() =>
			expect(statusRegion()).toHaveTextContent("No such host."),
		);
	});

	test("falls back to a generic reason when the server gives none", async () => {
		stubApi({
			...system,
			"POST /instance/sign-in-providers/test": { body: { ok: false } },
		});
		const user = userEvent.setup();
		await renderDialog();
		await typeIssuer(user);
		await user.click(screen.getByRole("button", { name: "Test connection" }));
		await waitFor(() =>
			expect(statusRegion()).toHaveTextContent("The issuer cannot be used."),
		);
	});

	test("shows a failed request's message, and clears the result when the issuer changes", async () => {
		stubApi({
			...system,
			"POST /instance/sign-in-providers/test": problem(
				500,
				"The control plane could not reach it.",
			),
		});
		const user = userEvent.setup();
		await renderDialog();
		await typeIssuer(user);
		await user.click(screen.getByRole("button", { name: "Test connection" }));
		await waitFor(() =>
			expect(statusRegion()).toHaveTextContent(
				"The control plane could not reach it.",
			),
		);
		await user.type(screen.getByLabelText("Issuer URL"), "x");
		expect(statusRegion()).toBeEmptyDOMElement();
	});
});

describe("ProviderDialog: editing", () => {
	test("opens on the saved values without the preset picker, keeping the secret write-only", async () => {
		stubApi(system);
		await renderDialog({ provider: existing });
		expect(
			await screen.findByRole("heading", { name: "Edit Okta" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("radio")).not.toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("Okta");
		expect(screen.getByLabelText("Issuer URL")).toHaveValue(
			"https://dev-1.okta.com/oauth2/default",
		);
		expect(screen.getByLabelText("Client ID")).toHaveValue("cid");
		expect(screen.getByLabelText("Client secret")).toHaveValue("");
		expect(screen.getByLabelText("Client secret")).toHaveAttribute(
			"placeholder",
			"Unchanged",
		);
		expect(screen.getByLabelText("Extra scopes")).toHaveValue("groups");
		expect(screen.getByLabelText("Allowed email domains")).toHaveValue(
			"corp.example",
		);
		expect(switchNamed("Trust the provider's MFA")).toHaveAttribute(
			"aria-checked",
			"true",
		);
		expect(switchNamed("Enabled")).toHaveAttribute("aria-checked", "false");
		expect(screen.getByText(existing.redirectUri)).toBeInTheDocument();
		expect(screen.getByText("Sign-in redirect URIs")).toBeInTheDocument();
		expect(screen.queryByText(/ID is reserved now/)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
	});

	test("saving unchanged fields PUTs the provider without a secret or id and closes", async () => {
		const api = stubApi({
			...system,
			"PUT /auth-providers/:id": { body: existing },
		});
		const onOpenChange = mock((_open: boolean) => {});
		const queryClient = testQueryClient();
		queryClient.setQueryData(listAuthProvidersQueryKey(), {
			items: [existing],
		});
		const user = userEvent.setup();
		await renderDialog({ provider: existing, onOpenChange }, queryClient);
		await user.click(await screen.findByRole("button", { name: "Save" }));
		expect(await screen.findByText("Saved Okta")).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/auth-providers/p1");
		expect(put?.body).toEqual({
			kind: "oidc",
			name: "Okta",
			issuerUrl: "https://dev-1.okta.com/oauth2/default",
			clientId: "cid",
			scopes: ["groups"],
			allowedDomains: ["corp.example"],
			trustMfa: true,
			enabled: false,
		});
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(
			queryClient.getQueryState(listAuthProvidersQueryKey())?.isInvalidated,
		).toBe(true);
	});

	test("a typed secret and a toggled switch are sent", async () => {
		const api = stubApi({
			...system,
			"PUT /auth-providers/:id": { body: existing },
		});
		const user = userEvent.setup();
		await renderDialog({ provider: existing });
		await user.type(await screen.findByLabelText("Client secret"), "rotated");
		await user.click(screen.getByText("Enabled"));
		await user.click(screen.getByText("Trust the provider's MFA"));
		await user.click(screen.getByRole("button", { name: "Save" }));
		await screen.findByText("Saved Okta");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toMatchObject({
			clientSecret: "rotated",
			enabled: true,
			trustMfa: false,
		});
	});

	test("the domain hint follows whether any domain is set", async () => {
		stubApi(system);
		const user = userEvent.setup();
		await renderDialog({ provider: existing });
		const domains = await screen.findByLabelText("Allowed email domains");
		expect(
			screen.getByText(/Only verified emails in these domains/),
		).toBeInTheDocument();
		await user.clear(domains);
		expect(
			screen.getByText("Empty: any verified email may sign in."),
		).toBeInTheDocument();
	});

	test("a failed save shows the reason", async () => {
		stubApi({
			...system,
			"PUT /auth-providers/:id": problem(
				422,
				"The issuer does not match its discovery document.",
			),
		});
		const user = userEvent.setup();
		await renderDialog({ provider: existing });
		await user.click(await screen.findByRole("button", { name: "Save" }));
		expect(
			await screen.findByText(
				"The issuer does not match its discovery document.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("Saved Okta")).not.toBeInTheDocument();
	});

	test("clearing a required field blocks saving with the reason", async () => {
		const api = stubApi(system);
		const user = userEvent.setup();
		await renderDialog({ provider: existing });
		await user.clear(await screen.findByLabelText("Client ID"));
		await user.click(screen.getByRole("button", { name: "Save" }));
		const dialog = screen.getByRole("dialog");
		expect(
			await within(dialog).findByText("Enter the client ID."),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method !== "GET")).toEqual([]);
	});

	test("an edited GitHub provider shows the GitHub fields only", async () => {
		stubApi(system);
		await renderDialog({
			provider: {
				...existing,
				kind: "github",
				issuerUrl: "",
				name: "GitHub",
				scopes: null,
				allowedDomains: null,
			},
		});
		expect(
			await screen.findByRole("heading", { name: "Edit GitHub" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("GitHub Enterprise Server URL")).toHaveValue(
			"",
		);
		expect(screen.queryByLabelText("Extra scopes")).not.toBeInTheDocument();
		expect(screen.getByText("Authorization callback URL")).toBeInTheDocument();
	});
});
