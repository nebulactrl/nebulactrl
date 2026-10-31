// Presets for the "Add sign-in provider" dialog: what to prefill for each
// identity provider and where to register NebulaCtrl at it. Pure: no DOM, no
// network. Every issuer pattern, redirect-URI field name and console URL
// below was checked against the provider's own documentation on 2026-10-26;
// the doc is cited on each entry. Issuer values are matched byte for byte
// against the provider's discovery document, so a trailing slash is part of
// the value (Auth0, authentik).

import type { ProviderFormState, ProviderKind } from "./provider-form";

export type PresetId =
	| "google"
	| "entra"
	| "okta"
	| "auth0"
	| "authentik"
	| "keycloak"
	| "zitadel"
	| "pocket-id"
	| "gitlab"
	| "github"
	| "oidc";

export interface ProviderPreset {
	id: PresetId;
	/** The picker's label. */
	label: string;
	kind: ProviderKind;
	/** Prefilled sign-in button name. */
	name: string;
	/**
	 * Issuer pattern. `{placeholder}` tokens must be replaced before saving.
	 * Empty for GitHub (github.com) and the generic preset.
	 */
	issuer: string;
	/** Extra scopes beyond openid, email and profile, space separated. */
	scopes: string;
	/** What each `{placeholder}` of the issuer stands for. */
	placeholders: Readonly<Record<string, string>>;
	/** The provider's name for the field that takes the redirect URI. */
	redirectField: string;
	/** One or two sentences: what to create and what is easy to get wrong. */
	note: string;
	/** The provider's page for creating the OAuth/OIDC client; null when it lives on the person's own server. */
	consoleUrl: string | null;
	/** The path of that page on a self-hosted instance, appended to the issuer's origin. */
	consolePath?: string;
	/** The provider's documentation page this preset was checked against. */
	docsUrl: string;
}

export const PRESETS: readonly ProviderPreset[] = [
	{
		id: "google",
		label: "Google",
		kind: "oidc",
		name: "Google",
		// https://developers.google.com/identity/openid-connect/openid-connect
		// Discovery document https://accounts.google.com/.well-known/openid-configuration
		// has "issuer": "https://accounts.google.com". Clients page and the
		// "Authorized redirect URIs" field are described on the same page.
		issuer: "https://accounts.google.com",
		scopes: "",
		placeholders: {},
		redirectField: "Authorized redirect URIs",
		note: "Create an OAuth client of type Web application. Any Google account can reach the sign-in; add your Workspace domain under allowed email domains to keep everyone else out.",
		consoleUrl: "https://console.developers.google.com/auth/clients",
		docsUrl:
			"https://developers.google.com/identity/openid-connect/openid-connect",
	},
	{
		id: "entra",
		label: "Microsoft Entra ID",
		kind: "oidc",
		name: "Microsoft",
		// https://learn.microsoft.com/en-us/entra/identity-platform/v2-protocols-oidc
		// Authority URL: https://login.microsoftonline.com/{tenant}/v2.0. The
		// discovery document of "common" and "organizations" reports
		// "issuer": ".../{tenantid}/v2.0", which never equals what was entered,
		// so only a concrete tenant ID works here.
		issuer: "https://login.microsoftonline.com/{tenant}/v2.0",
		scopes: "",
		placeholders: {
			tenant: "your Directory (tenant) ID, a GUID from the app's Overview page",
		},
		redirectField: "Redirect URIs (platform: Web)",
		note: "Use the tenant ID, not common or organizations. Entra sends no email_verified claim: under Token configuration add the optional claims email and xms_edov (ID token) so NebulaCtrl can treat the address as verified.",
		// Entra admin center, Entra ID > App registrations (same doc).
		consoleUrl: "https://entra.microsoft.com",
		docsUrl:
			"https://learn.microsoft.com/en-us/entra/identity-platform/v2-protocols-oidc",
	},
	{
		id: "okta",
		label: "Okta",
		kind: "oidc",
		name: "Okta",
		// https://developer.okta.com/docs/concepts/auth-servers/
		// Custom authorization server issuer: https://{yourOktaDomain}/oauth2/{authorizationServerId};
		// "default" is the pre-configured one. The org authorization server's
		// issuer is plain https://{yourOktaDomain}.
		issuer: "https://{domain}/oauth2/default",
		scopes: "",
		placeholders: {
			domain: "your Okta domain, for example dev-123456.okta.com",
		},
		redirectField: "Sign-in redirect URIs",
		note: "In the Admin Console open Applications, Create App Integration, OIDC, Web Application. /oauth2/default is the default authorization server; to use the org authorization server instead, drop /oauth2/default.",
		consoleUrl: null,
		// Wizard and "Sign-in redirect URIs": https://help.okta.com/oie/en-us/content/topics/apps/apps_app_integration_wizard_oidc.htm
		docsUrl:
			"https://help.okta.com/oie/en-us/content/topics/apps/apps_app_integration_wizard_oidc.htm",
	},
	{
		id: "auth0",
		label: "Auth0",
		kind: "oidc",
		name: "Auth0",
		// https://auth0.com/docs/get-started/applications/application-settings
		// ("Domain", "Allowed Callback URLs") and the discovery document, e.g.
		// https://samples.auth0.com/.well-known/openid-configuration with
		// "issuer": "https://samples.auth0.com/": the issuer ends with a slash.
		issuer: "https://{domain}/",
		scopes: "",
		placeholders: {
			domain:
				"your tenant domain from the application's Settings, for example dev-abc123.us.auth0.com (or your custom domain)",
		},
		redirectField: "Allowed Callback URLs",
		note: "Create a Regular Web Application. Keep the trailing slash of the issuer: Auth0 reports it with one.",
		consoleUrl: "https://manage.auth0.com/#/applications",
		docsUrl:
			"https://auth0.com/docs/get-started/applications/application-settings",
	},
	{
		id: "authentik",
		label: "authentik",
		kind: "oidc",
		name: "authentik",
		// https://docs.goauthentik.io/add-secure-apps/providers/oauth2/
		// Issuer mode "Each provider has a different issuer, based on the
		// application slug" (default): https://authentik.company/application/o/<application_slug>/
		issuer: "https://{domain}/application/o/{slug}/",
		scopes: "",
		placeholders: {
			domain: "your authentik host, for example auth.example.com",
			slug: "the application's slug",
		},
		redirectField: "Redirect URIs",
		note: "Create an Application with an OAuth2/OpenID Provider (confidential client). The issuer is per application and ends with a slash.",
		consoleUrl: null,
		docsUrl: "https://docs.goauthentik.io/add-secure-apps/providers/oauth2/",
	},
	{
		id: "keycloak",
		label: "Keycloak",
		kind: "oidc",
		name: "Keycloak",
		// https://www.keycloak.org/securing-apps/oidc-layers
		// Discovery: /realms/{realm-name}/.well-known/openid-configuration.
		// Redirect field "Valid Redirect URIs", exact string matching:
		// https://www.keycloak.org/docs/latest/server_admin/
		issuer: "https://{domain}/realms/{realm}",
		scopes: "",
		placeholders: {
			domain: "your Keycloak host, for example keycloak.example.com",
			realm: "the realm's name",
		},
		redirectField: "Valid redirect URIs",
		note: "Create a client in that realm with Client authentication on and the standard flow enabled. A Keycloak older than 17, or one started with a relative path, serves the realm under /auth/realms.",
		consoleUrl: null,
		docsUrl: "https://www.keycloak.org/securing-apps/oidc-layers",
	},
	{
		id: "zitadel",
		label: "Zitadel",
		kind: "oidc",
		name: "Zitadel",
		// https://zitadel.com/docs/apis/openidoauth/endpoints : the discovery
		// endpoint is ${CUSTOM_DOMAIN}/.well-known/openid-configuration, so the
		// issuer is the instance domain itself.
		issuer: "https://{domain}",
		scopes: "",
		placeholders: {
			domain:
				"your instance domain, for example my-instance-abc123.zitadel.cloud",
		},
		redirectField: "Redirect URIs",
		note: "Create a Web application and pick the authentication method Code (client ID and secret). The issuer is the instance domain with no path.",
		consoleUrl: null,
		// https://zitadel.com/docs/guides/manage/console/applications
		docsUrl: "https://zitadel.com/docs/guides/manage/console/applications",
	},
	{
		id: "pocket-id",
		label: "Pocket ID",
		kind: "oidc",
		name: "Pocket ID",
		// https://pocket-id.org/docs/client-examples ("Connect an app": register
		// an OIDC client, paste the callback URL) and
		// https://pocket-id.org/docs/guides/scopes-and-claims (the email scope
		// returns email and email_verified). The issuer is the Pocket ID URL.
		issuer: "https://{domain}",
		scopes: "",
		placeholders: {
			domain: "your Pocket ID host, for example id.example.com",
		},
		redirectField: "Callback URLs",
		note: "In Pocket ID add an OIDC client and paste the callback URL. The issuer is the address you open Pocket ID at, with no path.",
		consoleUrl: null,
		docsUrl: "https://pocket-id.org/docs/client-examples",
	},
	{
		id: "gitlab",
		label: "GitLab",
		kind: "oidc",
		name: "GitLab",
		// https://docs.gitlab.com/integration/openid_connect_provider/ : discovery
		// is https://gitlab.com/.well-known/openid-configuration, or the
		// self-managed instance's URL; gitlab.com reports "issuer": "https://gitlab.com".
		// Applications page and the "Redirect URI" field:
		// https://docs.gitlab.com/integration/oauth_provider/
		issuer: "https://gitlab.com",
		scopes: "",
		placeholders: {},
		redirectField: "Redirect URI",
		note: "Tick the scopes openid, profile and email on the application. For a self-managed GitLab replace the address with its base URL.",
		consoleUrl: "https://gitlab.com/-/user_settings/applications",
		consolePath: "/-/user_settings/applications",
		docsUrl: "https://docs.gitlab.com/integration/oauth_provider/",
	},
	{
		id: "github",
		label: "GitHub (OAuth)",
		kind: "github",
		name: "GitHub",
		// https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app
		// Settings > Developer settings > New OAuth App; "Authorization callback URL".
		issuer: "",
		scopes: "",
		placeholders: {},
		redirectField: "Authorization callback URL",
		note: "GitHub is OAuth, not OpenID Connect: there is nothing to discover. Leave the address empty for github.com; for GitHub Enterprise Server enter its base URL. Only a verified primary email can sign in, and GitHub's own 2FA is not trusted.",
		consoleUrl: "https://github.com/settings/developers",
		docsUrl:
			"https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app",
	},
	{
		id: "oidc",
		label: "Any OpenID Connect provider",
		kind: "oidc",
		name: "",
		// https://openid.net/specs/openid-connect-discovery-1_0.html section 4.3:
		// the issuer in the document must be identical to the one used to fetch it.
		issuer: "",
		scopes: "",
		placeholders: {},
		redirectField: "Redirect URI",
		note: "Any provider that serves /.well-known/openid-configuration. The issuer must be exactly the issuer value in that document.",
		consoleUrl: null,
		docsUrl: "https://openid.net/specs/openid-connect-discovery-1_0.html",
	},
];

export function presetById(id: PresetId): ProviderPreset {
	const found = PRESETS.find((preset) => preset.id === id);
	if (!found) throw new Error(`unknown provider preset: ${id}`);
	return found;
}

// ------------------------------------------------------------ placeholders

const PLACEHOLDER = /\{([a-z][a-z0-9_-]*)\}/gi;

/** The `{name}` tokens in `text`, in order and without repeats. */
export function placeholdersIn(text: string): string[] {
	const seen = new Set<string>();
	for (const match of text.matchAll(PLACEHOLDER)) {
		if (match[1]) seen.add(match[1]);
	}
	return [...seen];
}

export interface IssuerPart {
	/** Offset of `text` in the source, a stable key. */
	start: number;
	text: string;
	/** A `{placeholder}` token still to be replaced. */
	placeholder: boolean;
}

/** Splits text into plain runs and `{placeholder}` tokens, for highlighting. */
export function issuerParts(text: string): IssuerPart[] {
	const parts: IssuerPart[] = [];
	let last = 0;
	for (const match of text.matchAll(PLACEHOLDER)) {
		if (match.index > last) {
			parts.push({
				start: last,
				text: text.slice(last, match.index),
				placeholder: false,
			});
		}
		parts.push({ start: match.index, text: match[0], placeholder: true });
		last = match.index + match[0].length;
	}
	if (last < text.length) {
		parts.push({ start: last, text: text.slice(last), placeholder: false });
	}
	return parts;
}

/** The first `{placeholder}` as an input selection range, or undefined. */
export function firstPlaceholderRange(
	text: string,
): [start: number, end: number] | undefined {
	const match = new RegExp(PLACEHOLDER.source, "i").exec(text);
	return match ? [match.index, match.index + match[0].length] : undefined;
}

/** What each `{placeholder}` left in the issuer stands for, in order. */
export function placeholderHints(
	issuer: string,
	preset?: ProviderPreset,
): { name: string; hint: string | undefined }[] {
	return placeholdersIn(issuer).map((name) => ({
		name,
		hint: preset?.placeholders[name],
	}));
}

/** What to tell the person about each placeholder left in the issuer. */
export function placeholderProblem(
	issuer: string,
	preset?: ProviderPreset,
): string | undefined {
	const left = placeholdersIn(issuer);
	if (left.length === 0) return undefined;
	const tokens = left.map((name) => `{${name}}`);
	const hint = preset?.placeholders[left[0] ?? ""];
	const what = tokens.length === 1 ? tokens[0] : tokens.join(" and ");
	return hint
		? `Replace ${what} in the issuer URL: ${hint}.`
		: `Replace ${what} in the issuer URL.`;
}

// ---------------------------------------------------------------- applying

/** The form after choosing a preset: kind, name, issuer pattern and scopes; credentials and domain rules are kept. */
export function applyPreset(
	form: ProviderFormState,
	preset: ProviderPreset,
): ProviderFormState {
	return {
		...form,
		kind: preset.kind,
		name: preset.name,
		issuerUrl: preset.issuer,
		scopes: preset.scopes,
		// GitHub never asserts MFA in a verifiable way.
		trustMfa: preset.kind === "github" ? false : form.trustMfa,
	};
}

/** A caution about an issuer that is a known trap for the preset, or undefined. */
export function issuerWarning(
	id: PresetId,
	issuer: string,
): string | undefined {
	if (id !== "entra") return undefined;
	const tenant = /^https:\/\/login\.microsoftonline\.com\/([^/]+)\//i.exec(
		issuer.trim(),
	)?.[1];
	if (tenant && /^(common|organizations|consumers)$/i.test(tenant)) {
		return `Entra reports a tenant-specific issuer for "${tenant}", so the check against this one fails. Use your Directory (tenant) ID.`;
	}
	return undefined;
}

/**
 * Where to create the client: the preset's own page, or on a self-hosted
 * instance (GitLab) the same page under the issuer's host once it is filled in.
 */
export function consoleUrlFor(
	preset: ProviderPreset,
	issuer: string,
): string | null {
	if (preset.consolePath) {
		const trimmed = issuer.trim();
		if (trimmed !== "" && placeholdersIn(trimmed).length === 0) {
			try {
				const url = new URL(trimmed);
				if (url.protocol === "https:" || url.protocol === "http:") {
					return `${url.origin}${preset.consolePath}`;
				}
			} catch {
				// Not a URL yet: fall back to the default page.
			}
		}
	}
	return preset.consoleUrl;
}

/**
 * The preset a saved provider most likely came from, to show its guidance when
 * editing. Matches by kind and issuer host; the generic preset is the fallback.
 */
export function presetForProvider(
	kind: ProviderKind,
	issuerUrl: string,
): ProviderPreset {
	if (kind === "github") return presetById("github");
	let host = "";
	try {
		host = new URL(issuerUrl).host.toLowerCase();
	} catch {
		return presetById("oidc");
	}
	if (host === "accounts.google.com") return presetById("google");
	if (host === "login.microsoftonline.com") return presetById("entra");
	if (host.endsWith(".okta.com") || host.endsWith(".oktapreview.com")) {
		return presetById("okta");
	}
	if (host.endsWith(".auth0.com")) return presetById("auth0");
	if (host === "gitlab.com") return presetById("gitlab");
	if (host.endsWith(".zitadel.cloud")) return presetById("zitadel");
	return presetById("oidc");
}
