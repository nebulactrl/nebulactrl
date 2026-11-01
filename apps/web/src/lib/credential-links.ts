// Credential setup guides: for every integration that asks for a token, where
// to create it and which permissions it needs. Where the provider can prefill
// its token form from the URL (Cloudflare, GitLab) the link carries the
// permissions, so the person only confirms. Everywhere else the link goes to
// the exact page and the guide lists the permissions to tick, plus a policy to
// copy where the provider takes one (Route 53, Headscale).
//
// Pure: no DOM, no network. `components/common/credential-help.tsx` renders a
// guide next to the token field. Every URL constant names the doc it was
// checked against (verified 2026-10-26).

import {
	DEFAULT_TAG,
	OWNER_TAG,
	REQUIRED_OAUTH_SCOPES,
} from "#/features/proxy/lib/tailscale";
import { curatedDnsGuide, legoGenericGuide } from "./dns-provider-guides";

export interface CredentialPermission {
	/** What to select, as the provider words it: "Zone · DNS · Edit". */
	label: string;
	/** What NebulaCtrl uses it for. */
	why?: string;
	/** Not part of the prefilled link; the integration works without it. */
	optional?: boolean;
}

export interface CredentialPolicy {
	/** "IAM policy" or "Run on your Headscale server". */
	title: string;
	/** The copy-ready text. */
	text: string;
}

export interface CredentialGuide {
	provider: string;
	/** The button text; null when the provider has no page to open. */
	cta: string | null;
	/** The page to open, null until it can be built (an instance URL is missing). */
	url: string | null;
	/** True when `url` already carries the permissions. */
	prefilled: boolean;
	/** Why there is no link yet, shown beside the disabled button. */
	whyNoLink?: string;
	permissions: readonly CredentialPermission[];
	policy?: CredentialPolicy;
	note?: string;
	/** The "Then paste it here" line. */
	paste: string;
	/** The provider's own page about this credential. */
	docs: string;
}

const PASTE = "Then paste it here.";

// ---------------------------------------------------------------- Cloudflare

/**
 * Cloudflare prefills the "Create token" form from the URL.
 * https://developers.cloudflare.com/fundamentals/api/how-to/account-owned-token-template/
 * (user token format: `permissionGroupKeys` = URL-encoded JSON array of
 * `{key, type}`, `accountId=*`, `zoneId=all`, optional `name`).
 */
export const CLOUDFLARE_TOKEN_PAGE =
	"https://dash.cloudflare.com/profile/api-tokens";

/** https://developers.cloudflare.com/fundamentals/api/reference/permissions/ */
export const CLOUDFLARE_DOCS =
	"https://developers.cloudflare.com/fundamentals/api/get-started/create-token/";

export interface CloudflareGroup {
	key: string;
	type: "read" | "edit";
}

// `dns` and `zone` are in the template doc's permission table
// (https://developers.cloudflare.com/fundamentals/api/how-to/account-owned-token-template/#permission-reference).
// `argotunnel` is the dashboard's key for "Account · Cloudflare Tunnel". The
// doc does not list it, but Coolify
// (app/Models/IntegrationToken.php, cloudflareTokenTemplateUrl) and the
// Cloudflare Tunnel Ingress Controller docs
// (https://tunnel.strrl.dev/reference/cloudflare-credentials/) both ship it.
// Cloudflare drops an unknown key from a user-token URL without a notice, so
// the guide always lists the permissions too.
const CF_DNS_EDIT: CloudflareGroup = { key: "dns", type: "edit" };
const CF_ZONE_READ: CloudflareGroup = { key: "zone", type: "read" };
const CF_TUNNEL_EDIT: CloudflareGroup = { key: "argotunnel", type: "edit" };

/**
 * The template link: the permission groups, all accounts, all zones. The
 * person narrows the zones on the form.
 */
export function cloudflareTokenUrl(
	groups: readonly CloudflareGroup[],
	name: string,
): string {
	const keys = encodeURIComponent(JSON.stringify(groups));
	return `${CLOUDFLARE_TOKEN_PAGE}?permissionGroupKeys=${keys}&accountId=%2A&zoneId=all&name=${encodeURIComponent(name)}`;
}

/**
 * Authenticated Origin Pulls with Cloudflare's own certificate is the zone
 * setting `tls_client_auth`; changing a zone setting needs "Zone Settings Write"
 * (https://developers.cloudflare.com/api/resources/zones/subresources/settings/methods/edit/,
 * https://developers.cloudflare.com/ssl/origin-configuration/authenticated-origin-pull/set-up/global/).
 * Not part of the prefilled link: the dashboard's key for it is not documented
 * next to the others, so the person adds it by hand.
 */
const AOP_PERMISSION: CredentialPermission = {
	label: "Zone · Zone Settings · Edit",
	why: "turn on Authenticated Origin Pulls so the origin lock also requires Cloudflare's client certificate; add it by hand",
	optional: true,
};

const CF_NOTE =
	"The link selects these for you. If a permission is missing on the form, add it by hand. Limit the token to the zones NebulaCtrl should manage.";

export interface CloudflareGuideOptions {
	/** Also manage tunnels (a server without open ports). */
	tunnel: boolean;
}

/** The integration's token: DNS records, and optionally tunnels, in one template. */
export function cloudflareIntegrationGuide({
	tunnel,
}: CloudflareGuideOptions): CredentialGuide {
	const groups = tunnel
		? [CF_DNS_EDIT, CF_ZONE_READ, CF_TUNNEL_EDIT]
		: [CF_DNS_EDIT, CF_ZONE_READ];
	const permissions: CredentialPermission[] = [
		{
			label: "Zone · Zone · Read",
			why: "list your zones for the zone picker",
		},
		{
			label: "Zone · DNS · Edit",
			why: "create, update and delete records",
		},
	];
	if (tunnel) {
		permissions.push({
			label: "Account · Cloudflare Tunnel · Edit",
			why: "tunnels (a server without open ports)",
		});
	}
	permissions.push({
		label: "Zone · Zone Settings · Read",
		why: "warn about SSL modes that break proxied records; add it by hand",
		optional: true,
	});
	permissions.push(AOP_PERMISSION);
	return {
		provider: "Cloudflare",
		cta: tunnel
			? "Create a Cloudflare token with DNS and tunnel access"
			: "Create a Cloudflare token with DNS edit access",
		url: cloudflareTokenUrl(
			groups,
			tunnel ? "NebulaCtrl DNS and Tunnel" : "NebulaCtrl DNS",
		),
		prefilled: true,
		permissions,
		note: CF_NOTE,
		paste: PASTE,
		docs: CLOUDFLARE_DOCS,
	};
}

/**
 * The token permission the origin lock's Authenticated Origin Pulls layer
 * needs. Shown next to a domain whose zone refused the setting: the person adds
 * the permission to the token NebulaCtrl already uses, then pastes it again.
 */
export function cloudflareAopGuide(): CredentialGuide {
	return {
		provider: "Cloudflare",
		cta: "Edit the Cloudflare token",
		url: CLOUDFLARE_TOKEN_PAGE,
		prefilled: false,
		permissions: [{ ...AOP_PERMISSION, optional: false }],
		note: "Open the token NebulaCtrl uses, add the permission above and save. Until then the domain stays locked by IP only.",
		paste:
			"If you created a new token, paste it in Team settings → Integrations → Cloudflare.",
		docs: "https://developers.cloudflare.com/ssl/origin-configuration/authenticated-origin-pull/set-up/global/",
	};
}

/**
 * A DNS-01 resolver credential: lego needs Zone · Zone · Read and
 * Zone · DNS · Edit scoped to all zones (or the one being edited).
 * https://go-acme.github.io/lego/dns/cloudflare/
 */
export function cloudflareDns01Guide(): CredentialGuide {
	return {
		provider: "Cloudflare",
		cta: "Create a Cloudflare token with DNS edit access",
		url: cloudflareTokenUrl([CF_DNS_EDIT, CF_ZONE_READ], "NebulaCtrl DNS-01"),
		prefilled: true,
		permissions: [
			{
				label: "Zone · Zone · Read",
				why: "find the zone of the domain",
			},
			{
				label: "Zone · DNS · Edit",
				why: "publish and remove the _acme-challenge TXT record",
			},
		],
		note: CF_NOTE,
		paste: PASTE,
		docs: "https://go-acme.github.io/lego/dns/cloudflare/",
	};
}

// ----------------------------------------------------------------- Tailscale

/**
 * The admin console page that creates OAuth clients ("Credential" → "OAuth").
 * https://tailscale.com/docs/features/oauth-clients#setting-up-an-oauth-client
 * Tailscale documents no query parameters that prefill it, so the guide lists
 * the scopes and tags to select.
 */
export const TAILSCALE_TRUST_CREDENTIALS_URL =
	"https://console.tailscale.com/admin/settings/trust-credentials";

/** https://tailscale.com/docs/reference/trust-credentials (scopes `auth_keys`, `devices:core`). */
export const TAILSCALE_DOCS =
	"https://tailscale.com/docs/reference/trust-credentials";

export interface TailscaleGuideOptions {
	/** The tags minted keys carry; the OAuth client's owner tag owns them. */
	defaultTags?: readonly string[];
}

/** The Tailscale OAuth client NebulaCtrl mints node keys with. */
export function tailscaleOAuthGuide({
	defaultTags = [DEFAULT_TAG],
}: TailscaleGuideOptions = {}): CredentialGuide {
	const owned = defaultTags.filter((tag) => tag !== OWNER_TAG);
	const scopeWhy: Record<(typeof REQUIRED_OAUTH_SCOPES)[number], string> = {
		auth_keys: "mint the keys proxy nodes join with",
		"devices:core": "list and delete the nodes it created",
	};
	return {
		provider: "Tailscale",
		cta: "Create a Tailscale OAuth client with key and device access",
		url: TAILSCALE_TRUST_CREDENTIALS_URL,
		prefilled: false,
		permissions: [
			...REQUIRED_OAUTH_SCOPES.map((scope) => ({
				label: `${scope} (Write)`,
				why: scopeWhy[scope],
			})),
			{
				label: `Tag ${OWNER_TAG}`,
				why:
					owned.length > 0
						? `it owns ${owned.join(", ")} once the tailnet policy below is applied`
						: "the owner tag of the tags minted keys carry",
			},
		],
		note: "Open Credential, then OAuth, tick these scopes and choose the tag. Apply the tailnet policy below first so the tag exists.",
		paste: "Then paste the client ID and secret here.",
		docs: TAILSCALE_DOCS,
	};
}

/**
 * The Headscale API key is made on the server; there is no page to open.
 * https://headscale.net/stable/ref/api/
 */
export function headscaleApiKeyGuide(): CredentialGuide {
	return {
		provider: "Headscale",
		cta: null,
		url: null,
		prefilled: false,
		permissions: [
			{
				label: "An API key",
				why: "it can mint pre-auth keys and list or delete nodes",
			},
		],
		policy: {
			title: "Run on your Headscale server",
			text: "headscale apikeys create",
		},
		note: "The key is shown once. The default expiry is 90 days, so create a new one before it ends.",
		paste: PASTE,
		docs: "https://headscale.net/stable/ref/api/",
	};
}

/** A reusable pre-auth key carrying the default tags, made on the Headscale server. */
export function headscalePreauthGuide({
	defaultTags = [DEFAULT_TAG],
}: TailscaleGuideOptions = {}): CredentialGuide {
	return {
		provider: "Headscale",
		cta: null,
		url: null,
		prefilled: false,
		permissions: [
			{
				label: "Reusable",
				why: "every proxy node registers with the same key",
			},
			{
				label: `Tagged ${defaultTags.join(", ")}`,
				why: "the default tags NebulaCtrl gives its nodes",
			},
		],
		note: "Create it on your Headscale server with the headscale preauthkeys command, as reusable and with those tags.",
		paste: PASTE,
		docs: "https://headscale.net/stable/ref/api/",
	};
}

// ------------------------------------------------------------------ Git hosts

/** An instance URL as its origin plus any path prefix, without a trailing slash; null when it is not http(s). */
export function normalizeBaseUrl(raw: string): string | null {
	const text = raw.trim();
	if (text === "") return null;
	let url: URL;
	try {
		url = new URL(text);
	} catch {
		return null;
	}
	if (url.protocol !== "https:" && url.protocol !== "http:") return null;
	if (url.username !== "" || url.password !== "") return null;
	return `${url.origin}${url.pathname.replace(/\/+$/, "")}`;
}

const NEEDS_INSTANCE = "Enter the instance URL above to get the link.";
const BAD_INSTANCE = "Enter a full instance URL, like https://git.example.com.";

/**
 * GitLab prefills a personal access token from `name`, `description` and
 * `scopes` (comma separated, at least one valid scope).
 * https://docs.gitlab.com/user/profile/personal_access_tokens/#prefill-personal-access-token-details
 */
export const GITLAB_DEFAULT_BASE = "https://gitlab.com";
export const GITLAB_SCOPES = ["api", "read_repository"] as const;

/**
 * The prefilled page of a GitLab instance; null when `baseUrl` is not a URL.
 * Empty means gitlab.com. `scopes` default to what connecting a provider needs.
 */
export function gitlabTokenUrl(
	baseUrl: string,
	scopes: readonly string[] = GITLAB_SCOPES,
	name = "NebulaCtrl",
): string | null {
	const base =
		baseUrl.trim() === "" ? GITLAB_DEFAULT_BASE : normalizeBaseUrl(baseUrl);
	if (base === null) return null;
	return `${base}/-/user_settings/personal_access_tokens?name=${encodeURIComponent(name)}&scopes=${scopes.map(encodeURIComponent).join(",")}`;
}

export function gitlabGuide(baseUrl: string): CredentialGuide {
	const url = gitlabTokenUrl(baseUrl);
	return {
		provider: "GitLab",
		cta: "Create a GitLab token with API and repository access",
		url,
		prefilled: true,
		...(url === null ? { whyNoLink: BAD_INSTANCE } : {}),
		permissions: [
			{
				label: "api",
				why: "webhooks, commit statuses and merge request comments",
			},
			{ label: "read_repository", why: "clone private repositories" },
		],
		note: "The token's role must be Maintainer on the projects, so NebulaCtrl can manage their webhooks.",
		paste: PASTE,
		docs: "https://docs.gitlab.com/user/profile/personal_access_tokens/",
	};
}

/**
 * Gitea and Forgejo create tokens on the Applications page of the user
 * settings and take no prefill, so the guide lists the scopes.
 * Gitea: https://docs.gitea.com/development/api-usage#generating-and-listing-api-tokens
 * Forgejo: https://forgejo.org/docs/latest/user/oauth2-provider/ (`/user/settings/applications`)
 * and https://forgejo.org/docs/latest/user/authentication/token-scope/ (scope names).
 */
export function giteaTokenUrl(baseUrl: string): string | null {
	const base = normalizeBaseUrl(baseUrl);
	return base === null ? null : `${base}/user/settings/applications`;
}

export const GITEA_SCOPES: readonly CredentialPermission[] = [
	{
		label: "repository (Read and Write)",
		why: "list repositories and branches, webhooks, commit statuses",
	},
	{ label: "user (Read)", why: "tell who the token belongs to" },
	{
		label: "issue (Read and Write)",
		why: "pull request preview comments",
		optional: true,
	},
];

const GITEA_CLONE_SCOPES: readonly CredentialPermission[] = [
	{ label: "repository (Read)", why: "clone private repositories" },
];

/** `connect` is a provider connection; `clone` is a service's read-only repository token. */
export function giteaGuide(
	provider: "gitea" | "forgejo",
	baseUrl: string,
	purpose: "connect" | "clone" = "connect",
): CredentialGuide {
	const label = provider === "gitea" ? "Gitea" : "Forgejo";
	const url = giteaTokenUrl(baseUrl);
	const clone = purpose === "clone";
	return {
		provider: label,
		cta: clone
			? `Create a ${label} token with repository read access`
			: `Create a ${label} token with repository access`,
		url,
		prefilled: false,
		...(url === null
			? {
					whyNoLink: baseUrl.trim() === "" ? NEEDS_INSTANCE : BAD_INSTANCE,
				}
			: {}),
		permissions: clone ? GITEA_CLONE_SCOPES : GITEA_SCOPES,
		note: clone
			? "Open Manage access tokens, name it NebulaCtrl and set the scope as listed."
			: "Open Manage access tokens, name it NebulaCtrl and set each scope as listed. The token's owner must administer the repositories, so NebulaCtrl can manage their webhooks.",
		paste: PASTE,
		docs:
			provider === "gitea"
				? "https://docs.gitea.com/development/api-usage"
				: "https://forgejo.org/docs/latest/user/authentication/token-scope/",
	};
}

/**
 * Bitbucket Cloud app passwords are replaced by API tokens, which need scopes
 * to clone. NebulaCtrl clones with the static user name `x-token-auth`.
 * https://support.atlassian.com/bitbucket-cloud/docs/create-an-api-token/
 * https://support.atlassian.com/bitbucket-cloud/docs/using-api-tokens/
 * (cloning needs `read:repository:bitbucket`). The page itself is Atlassian's
 * account-wide API token page, confirmed in
 * https://support.atlassian.com/atlassian-account/docs/manage-api-tokens-for-your-atlassian-account/
 */
export const BITBUCKET_TOKEN_URL =
	"https://id.atlassian.com/manage-profile/security/api-tokens";

export function bitbucketGuide(): CredentialGuide {
	return {
		provider: "Bitbucket",
		cta: "Create a Bitbucket API token with repository read access",
		url: BITBUCKET_TOKEN_URL,
		prefilled: false,
		permissions: [
			{ label: "App: Bitbucket" },
			{
				label: "read:repository:bitbucket",
				why: "clone private repositories",
			},
		],
		note: "Choose Create API token with scopes. A repository access token with read access also works: repository Settings, Security, Access tokens.",
		paste: PASTE,
		docs: "https://support.atlassian.com/bitbucket-cloud/docs/create-an-api-token/",
	};
}

/**
 * The guide of a git provider's access token; null for GitHub, which connects
 * through its App manifest flow and has no token to paste.
 */
export function gitProviderGuide(
	provider: string,
	baseUrl: string,
): CredentialGuide | null {
	switch (provider) {
		case "gitlab":
			return gitlabGuide(baseUrl);
		case "gitea":
		case "forgejo":
			return giteaGuide(provider, baseUrl);
		case "bitbucket":
			return bitbucketGuide();
		default:
			return null;
	}
}

// ---------------------------------------------------------------- DNS hosts

/**
 * Hetzner DNS lives in the Cloud API; the token belongs to a project, so the
 * console has no page to deep-link. Security → API tokens inside the project.
 * https://docs.hetzner.com/cloud/api/getting-started/generating-api-token/
 * (provider side: https://go-acme.github.io/lego/dns/hetzner/)
 */
export const HETZNER_CONSOLE_URL = "https://console.hetzner.com/";

export function hetznerGuide(): CredentialGuide {
	return {
		provider: "Hetzner",
		cta: "Create a Hetzner token with read and write access",
		url: HETZNER_CONSOLE_URL,
		prefilled: false,
		permissions: [
			{
				label: "Read & Write",
				why: "publish and remove the _acme-challenge TXT record",
			},
		],
		note: "Open the project that owns the zone, then Security, API tokens, Generate API token.",
		paste: PASTE,
		docs: "https://docs.hetzner.com/cloud/api/getting-started/generating-api-token/",
	};
}

/**
 * https://docs.digitalocean.com/reference/api/create-personal-access-token/
 * (Full Access = `api:write`; custom scopes listed in
 * https://docs.digitalocean.com/reference/api/scopes/). The tokens page is
 * linked from https://docs.digitalocean.com/reference/doctl/how-to/install/.
 */
export const DIGITALOCEAN_TOKEN_URL =
	"https://cloud.digitalocean.com/account/api/tokens";

export function digitaloceanGuide(): CredentialGuide {
	return {
		provider: "DigitalOcean",
		cta: "Create a DigitalOcean token with domain write access",
		url: DIGITALOCEAN_TOKEN_URL,
		prefilled: false,
		permissions: [
			{
				label: "Full Access (api:write)",
				why: "simplest: every scope of your team role",
			},
			{
				label:
					"Or custom scopes domain:read, domain:create, domain:update, domain:delete",
				why: "only the domains and their records",
			},
		],
		note: "Choose Generate New Token, then either Full Access or Custom Scopes.",
		paste: PASTE,
		docs: "https://docs.digitalocean.com/reference/api/create-personal-access-token/",
	};
}

/**
 * IAM console. The policy is lego's least-privilege variant, without its
 * record-name condition: write access is limited to one hosted zone.
 * https://go-acme.github.io/lego/dns/route53/#least-privilege-policy-for-production-purposes
 * https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies_create-console.html
 */
export const AWS_IAM_CONSOLE_URL = "https://console.aws.amazon.com/iam/";

const HOSTED_ZONE_ID = /^[A-Z0-9]{1,32}$/;

/** The IAM policy for DNS-01 in one hosted zone; every zone when the ID is blank or not an ID. */
export function route53Policy(hostedZoneId: string): string {
	const id = hostedZoneId.trim();
	const zone = `arn:aws:route53:::hostedzone/${HOSTED_ZONE_ID.test(id) ? id : "*"}`;
	return JSON.stringify(
		{
			Version: "2012-10-17",
			Statement: [
				{
					Effect: "Allow",
					Action: "route53:GetChange",
					Resource: "arn:aws:route53:::change/*",
				},
				{
					Effect: "Allow",
					Action: "route53:ListHostedZonesByName",
					Resource: "*",
				},
				{
					Effect: "Allow",
					Action: ["route53:ListResourceRecordSets"],
					Resource: [zone],
				},
				{
					Effect: "Allow",
					Action: ["route53:ChangeResourceRecordSets"],
					Resource: [zone],
				},
			],
		},
		null,
		2,
	);
}

export function route53Guide(hostedZoneId: string): CredentialGuide {
	const scoped = HOSTED_ZONE_ID.test(hostedZoneId.trim());
	return {
		provider: "Amazon Route 53",
		cta: "Open AWS IAM to create a Route 53 policy",
		url: AWS_IAM_CONSOLE_URL,
		prefilled: false,
		permissions: [
			{
				label: "route53:GetChange",
				why: "wait for the TXT record to be live",
			},
			{
				label: "route53:ChangeResourceRecordSets",
				why: "publish and remove the _acme-challenge record",
			},
			{
				label: "route53:ListResourceRecordSets",
				why: "find the record to remove",
			},
			{ label: "route53:ListHostedZonesByName", why: "find the hosted zone" },
		],
		policy: { title: "IAM policy", text: route53Policy(hostedZoneId) },
		note: `Go to Policies, Create policy, JSON, paste this, then attach it to an IAM user and create an access key for that user. ${
			scoped
				? "It is limited to the hosted zone you entered."
				: "It covers every hosted zone: enter the hosted zone ID below to narrow it."
		}`,
		paste: "Then paste the access key here.",
		docs: "https://go-acme.github.io/lego/dns/route53/",
	};
}

// ------------------------------------------------------------------- MaxMind

/**
 * https://support.maxmind.com/knowledge-base/articles/generate-a-maxmind-license-key
 * ("Manage License Keys" in the account portal, login required).
 */
export const MAXMIND_LICENSE_KEY_URL =
	"https://www.maxmind.com/en/accounts/current/license-key";

export function maxmindGuide(): CredentialGuide {
	return {
		provider: "MaxMind",
		cta: "Create a MaxMind license key for GeoLite2",
		url: MAXMIND_LICENSE_KEY_URL,
		prefilled: false,
		permissions: [
			{
				label: "Generate new license key",
				why: "it downloads the GeoLite2 databases",
			},
		],
		note: "The key is shown once. Your account ID is on the same page.",
		paste: "Then paste the account ID and key here.",
		docs: "https://support.maxmind.com/knowledge-base/articles/generate-a-maxmind-license-key",
	};
}

// ------------------------------------------------------------------- DNS-01

/**
 * The guide of a DNS-01 provider: the original four and the curated
 * providers in dns-provider-guides.ts have their own; any other lego code
 * gets the generic guide that points at lego's page. `values` are the form's
 * current field values and `name` the provider's display name (the generic
 * guide's title).
 */
export function dns01Guide(
	provider: string,
	values: Readonly<Record<string, string>> = {},
	name = provider,
): CredentialGuide {
	switch (provider) {
		case "cloudflare":
			return cloudflareDns01Guide();
		case "route53":
			return route53Guide(values.AWS_HOSTED_ZONE_ID ?? "");
		case "hetzner":
			return hetznerGuide();
		case "digitalocean":
			return digitaloceanGuide();
		default:
			return curatedDnsGuide(provider) ?? legoGenericGuide(provider, name);
	}
}

// ------------------------------------------------ git token of a service

/**
 * GitHub prefills a fine-grained personal access token from URL parameters:
 * `name` (40 characters at most), `description`, `target_name` (the resource
 * owner), `expires_in` and one parameter per permission, such as
 * `contents=read`.
 * https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens#pre-filling-fine-grained-personal-access-token-details-using-url-parameters
 */
export const GITHUB_FINE_GRAINED_TOKEN_URL =
	"https://github.com/settings/personal-access-tokens/new";

const GITHUB_OWNER = /^[A-Za-z0-9-]{1,39}$/;

/** The read-only token link of a repository's owner (a user or organization). */
export function githubCloneTokenUrl(owner: string): string {
	const query = [
		`name=${encodeURIComponent("NebulaCtrl clone")}`,
		`description=${encodeURIComponent("Read-only clone token for NebulaCtrl builds")}`,
	];
	if (GITHUB_OWNER.test(owner)) {
		query.push(`target_name=${encodeURIComponent(owner)}`);
	}
	query.push("contents=read", "metadata=read");
	return `${GITHUB_FINE_GRAINED_TOKEN_URL}?${query.join("&")}`;
}

export interface GitRemote {
	/** Lower-case host name: `github.com`. */
	host: string;
	/** `https://github.com`; the instance without a path. */
	origin: string;
	/** The first path segment: the user or organization. */
	owner: string;
}

/** A repository's HTTP(S) URL taken apart; null for anything else (empty, SSH, a typo). */
export function parseGitRemote(raw: string): GitRemote | null {
	let url: URL;
	try {
		url = new URL(raw.trim());
	} catch {
		return null;
	}
	if (url.protocol !== "https:" && url.protocol !== "http:") return null;
	return {
		host: url.hostname.toLowerCase(),
		origin: url.origin,
		owner: url.pathname.split("/").find((part) => part !== "") ?? "",
	};
}

export type GitHostKind =
	| "github"
	| "gitlab"
	| "gitea"
	| "forgejo"
	| "bitbucket";

/**
 * Which git host a name belongs to, from the well-known public hosts and the
 * usual self-hosting names (`gitlab.corp.example`); null when it cannot tell.
 */
export function gitHostKind(host: string): GitHostKind | null {
	if (host === "github.com") return "github";
	if (host === "bitbucket.org") return "bitbucket";
	if (host === "gitlab.com" || host.startsWith("gitlab.")) return "gitlab";
	if (host === "codeberg.org" || host.startsWith("forgejo.")) return "forgejo";
	if (host.startsWith("gitea.")) return "gitea";
	return null;
}

/**
 * A service's read-only repository token, for the host of its repository URL.
 * Null until the URL parses. An unknown host gets the permissions of each
 * host and no link.
 */
export function gitCloneGuide(gitUrl: string): CredentialGuide | null {
	const remote = parseGitRemote(gitUrl);
	if (remote === null) return null;
	const kind = gitHostKind(remote.host);
	switch (kind) {
		case "github":
			return {
				provider: "GitHub",
				cta: "Create a GitHub token with read access to this repository",
				url: githubCloneTokenUrl(remote.owner),
				prefilled: true,
				permissions: [
					{
						label: "Repository access: Only select repositories",
						why: "choose this repository",
					},
					{ label: "Contents: Read-only", why: "clone the repository" },
					{
						label: "Metadata: Read-only",
						why: "GitHub adds it by itself",
					},
				],
				note: "The link fills in the name, the owner and the permissions. For an organization's repository the organization must allow fine-grained tokens.",
				paste: PASTE,
				docs: "https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens",
			};
		case "gitlab": {
			const url = gitlabTokenUrl(
				remote.origin,
				["read_repository"],
				"NebulaCtrl clone",
			);
			return {
				...gitlabGuide(remote.origin),
				cta: "Create a GitLab token with repository read access",
				url,
				permissions: [
					{ label: "read_repository", why: "clone private repositories" },
				],
				note: "A project or group access token with this scope works too.",
			};
		}
		case "gitea":
		case "forgejo":
			return giteaGuide(kind, remote.origin, "clone");
		case "bitbucket":
			return bitbucketGuide();
		case null:
			return {
				provider: "Git",
				cta: null,
				url: null,
				prefilled: false,
				permissions: [
					{ label: "GitLab: read_repository" },
					{ label: "Gitea and Forgejo: repository (Read)" },
					{ label: "Bitbucket: read:repository:bitbucket" },
				],
				note: `NebulaCtrl cannot tell which software runs ${remote.host}. Create a read-only token on it with the permission that matches.`,
				paste: PASTE,
				docs: "https://docs.gitlab.com/user/profile/personal_access_tokens/",
			};
	}
}

// --------------------------------------------------------------- registries

export type RegistryKind = "dockerhub" | "ghcr" | "gitlab" | "other";

/** The registry host of an image reference; empty for Docker Hub's short names (`traefik/whoami`). */
export function registryHostOf(image: string): string {
	const ref = image.trim();
	const slash = ref.indexOf("/");
	if (slash === -1) return "";
	const first = ref.slice(0, slash).toLowerCase();
	return first.includes(".") || first.includes(":") || first === "localhost"
		? first
		: "";
}

export function registryKindOf(image: string): RegistryKind {
	if (image.trim() === "") return "other";
	const host = registryHostOf(image);
	if (
		host === "" ||
		host === "docker.io" ||
		host === "index.docker.io" ||
		host === "registry-1.docker.io"
	) {
		return "dockerhub";
	}
	if (host === "ghcr.io") return "ghcr";
	if (host === "registry.gitlab.com") return "gitlab";
	return "other";
}

/**
 * GitHub Packages only accepts a classic token; its docs link
 * `settings/tokens/new?scopes=write:packages`, so `scopes` (and `description`)
 * prefill the form.
 * https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry
 */
export const GHCR_TOKEN_URL = `https://github.com/settings/tokens/new?scopes=read:packages&description=${encodeURIComponent("NebulaCtrl registry")}`;

/**
 * Docker Hub access tokens: Account settings, Personal access tokens. A
 * private image needs Repo Read-only.
 * https://docs.docker.com/security/access-tokens/personal-access-tokens/
 * https://docs.docker.com/security/access-tokens/reference/
 * (the direct settings page is `app.docker.com/settings/personal-access-tokens`;
 * the docs only link `app.docker.com`.)
 */
export const DOCKER_HUB_TOKEN_URL =
	"https://app.docker.com/settings/personal-access-tokens";

/**
 * A registry login that pulls a private image. GitLab: pulling needs
 * `read_registry`.
 * https://docs.gitlab.com/user/packages/container_registry/authenticate_with_container_registry/
 */
export function registryGuide(image: string): CredentialGuide {
	switch (registryKindOf(image)) {
		case "dockerhub":
			return {
				provider: "Docker Hub",
				cta: "Create a Docker Hub token with read-only access",
				url: DOCKER_HUB_TOKEN_URL,
				prefilled: false,
				permissions: [
					{
						label: "Access permissions: Repo Read-only",
						why: "pull private images",
					},
				],
				note: "Open Account settings, Personal access tokens, Generate new token. The user name is your Docker ID.",
				paste: "Then enter your Docker ID and paste the token here.",
				docs: "https://docs.docker.com/security/access-tokens/personal-access-tokens/",
			};
		case "ghcr":
			return {
				provider: "GitHub",
				cta: "Create a GitHub token that can pull packages",
				url: GHCR_TOKEN_URL,
				prefilled: true,
				permissions: [
					{
						label: "read:packages",
						why: "download container images and read their metadata",
					},
				],
				note: "GitHub Packages accepts only a classic token. If your organization requires SSO, authorize the token for it. The user name is your GitHub login.",
				paste: "Then enter your GitHub login and paste the token here.",
				docs: "https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry",
			};
		case "gitlab":
			return {
				provider: "GitLab",
				cta: "Create a GitLab token that can pull images",
				url: gitlabTokenUrl(
					GITLAB_DEFAULT_BASE,
					["read_registry"],
					"NebulaCtrl registry",
				),
				prefilled: true,
				permissions: [{ label: "read_registry", why: "pull container images" }],
				note: "A deploy token with read_registry works too. The user name is your GitLab user name, or the deploy token's.",
				paste: "Then enter the user name and paste the token here.",
				docs: "https://docs.gitlab.com/user/packages/container_registry/authenticate_with_container_registry/",
			};
		case "other":
			return {
				provider: "Registry",
				cta: null,
				url: null,
				prefilled: false,
				permissions: [
					{
						label: "Pull access to the image",
						why: "a read-only token or robot account is enough",
					},
				],
				note: "Create the credential in your registry's own settings, with pull access only.",
				paste: "Then enter the user name and paste the token or password here.",
				docs: "https://docs.docker.com/reference/cli/docker/login/",
			};
	}
}

/**
 * GitHub's classic token page with `write:packages` selected (it includes
 * read). Builds push with it; servers can pull with it too.
 */
export const GHCR_PUSH_TOKEN_URL = `https://github.com/settings/tokens/new?scopes=write:packages&description=${encodeURIComponent("NebulaCtrl builds")}`;

/**
 * The credential a build server pushes images with, for a registry host
 * (`ghcr.io`, `registry.example.com:5000`). Where the provider can prefill
 * the form (GitHub, GitLab) the link carries the scope. A separate read-only
 * pull credential uses `registryGuide`.
 * https://docs.docker.com/security/access-tokens/reference/
 * https://docs.gitlab.com/user/packages/container_registry/authenticate_with_container_registry/
 */
export function registryPushGuide(host: string): CredentialGuide {
	switch (host.trim() === "" ? "other" : registryKindOf(`${host.trim()}/x`)) {
		case "dockerhub":
			return {
				provider: "Docker Hub",
				cta: "Create a Docker Hub token that can push",
				url: DOCKER_HUB_TOKEN_URL,
				prefilled: false,
				permissions: [
					{
						label: "Access permissions: Read & Write",
						why: "push built images and pull them back",
					},
				],
				note: "Open Account settings, Personal access tokens, Generate new token. The user name is your Docker ID. A free account has few private repositories, and every service builds into its own.",
				paste: "Then enter your Docker ID and paste the token here.",
				docs: "https://docs.docker.com/security/access-tokens/personal-access-tokens/",
			};
		case "ghcr":
			return {
				provider: "GitHub",
				cta: "Create a GitHub token that can push packages",
				url: GHCR_PUSH_TOKEN_URL,
				prefilled: true,
				permissions: [
					{
						label: "write:packages",
						why: "upload container images (includes read:packages)",
					},
					{
						label: "delete:packages",
						why: "only if you clean up old images yourself",
						optional: true,
					},
				],
				note: "GitHub Packages accepts only a classic token. If your organization requires SSO, authorize the token for it. The user name is your GitHub login; set the namespace to your login or organization in lowercase.",
				paste: "Then enter your GitHub login and paste the token here.",
				docs: "https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry",
			};
		case "gitlab":
			return {
				provider: "GitLab",
				cta: "Create a GitLab token that can push images",
				url: gitlabTokenUrl(
					GITLAB_DEFAULT_BASE,
					["write_registry"],
					"NebulaCtrl builds",
				),
				prefilled: true,
				permissions: [
					{ label: "write_registry", why: "push container images" },
					{
						label: "read_registry",
						why: "pull them back with the same token",
						optional: true,
					},
				],
				note: "A deploy token with write_registry works too. The user name is your GitLab user name, or the deploy token's. The namespace is your group and project path.",
				paste: "Then enter the user name and paste the token here.",
				docs: "https://docs.gitlab.com/user/packages/container_registry/authenticate_with_container_registry/",
			};
		case "other":
			return {
				provider: "Registry",
				cta: null,
				url: null,
				prefilled: false,
				permissions: [
					{
						label: "Push and pull access to the namespace",
						why: "a robot account or access token works",
					},
				],
				note: "Create the credential in your registry's own settings (Harbor: a robot account; a self-hosted registry: its htpasswd user). ECR, Artifact Registry and ACR tokens expire within hours and are not supported.",
				paste: "Then enter the user name and paste the token or password here.",
				docs: "https://docs.docker.com/reference/cli/docker/login/",
			};
	}
}

// ------------------------------------------------------------------- backups

export type BackupHostKind =
	| "aws"
	| "r2"
	| "b2"
	| "hetzner"
	| "wasabi"
	| "selfhosted";

/** True for `host` itself and for any subdomain of it; `evilr2.example` is not under `r2.example`. */
function isHostOrSubdomain(host: string, domain: string): boolean {
	return host === domain || host.endsWith(`.${domain}`);
}

/** Which object storage an endpoint belongs to; an empty endpoint is AWS S3. */
export function backupHostKind(endpoint: string): BackupHostKind {
	const text = endpoint.trim();
	if (text === "") return "aws";
	let host: string;
	try {
		host = new URL(text.includes("://") ? text : `https://${text}`).hostname;
	} catch {
		return "selfhosted";
	}
	host = host.toLowerCase();
	if (isHostOrSubdomain(host, "amazonaws.com")) return "aws";
	if (isHostOrSubdomain(host, "r2.cloudflarestorage.com")) return "r2";
	if (isHostOrSubdomain(host, "backblazeb2.com")) return "b2";
	if (isHostOrSubdomain(host, "your-objectstorage.com")) return "hetzner";
	if (isHostOrSubdomain(host, "wasabisys.com")) return "wasabi";
	return "selfhosted";
}

/**
 * The bucket-scoped policy a backup destination needs: put, get, delete and
 * abort multipart on the objects, list on the bucket. A key prefix narrows both.
 * Statement shapes follow AWS's own bucket and prefix examples.
 * https://docs.aws.amazon.com/AmazonS3/latest/userguide/example-policies-s3.html
 * Wasabi takes the same document (its policies use `arn:aws:s3:::` resources):
 * https://docs.wasabi.com/docs/creating-a-policy
 */
export function s3BackupPolicy(bucket: string, prefix: string): string {
	const name = bucket.trim() === "" ? "YOUR-BUCKET" : bucket.trim();
	const path = prefix.trim().replace(/^\/+|\/+$/g, "");
	const objects = `arn:aws:s3:::${name}/${path === "" ? "" : `${path}/`}*`;
	const list: Record<string, unknown> = {
		Effect: "Allow",
		Action: "s3:ListBucket",
		Resource: `arn:aws:s3:::${name}`,
	};
	if (path !== "") {
		list.Condition = { StringLike: { "s3:prefix": [path, `${path}/*`] } };
	}
	return JSON.stringify(
		{
			Version: "2012-10-17",
			Statement: [
				list,
				{
					Effect: "Allow",
					Action: [
						"s3:PutObject",
						"s3:GetObject",
						"s3:DeleteObject",
						"s3:AbortMultipartUpload",
					],
					Resource: objects,
				},
			],
		},
		null,
		2,
	);
}

const S3_ACTIONS: readonly CredentialPermission[] = [
	{ label: "s3:PutObject", why: "upload backups" },
	{ label: "s3:GetObject", why: "restore and verify" },
	{ label: "s3:DeleteObject", why: "retention" },
	{ label: "s3:ListBucket", why: "list stored backups" },
	{ label: "s3:AbortMultipartUpload", why: "clean up failed uploads" },
];

export interface BackupGuideInput {
	endpoint: string;
	bucket: string;
	prefix: string;
}

/** R2 tokens live on the R2 overview page: https://developers.cloudflare.com/r2/api/tokens/ */
export const R2_OVERVIEW_URL =
	"https://dash.cloudflare.com/?to=/:account/r2/overview";

/**
 * Backblaze B2: Application Keys under B2 Cloud Storage; pick one bucket and
 * Read and Write. The page path is the console's route; the doc names the menu
 * entry.
 * https://www.backblaze.com/docs/cloud-storage-create-and-manage-app-keys
 */
export const B2_APP_KEYS_URL = "https://secure.backblaze.com/app_keys.htm";

/** Hetzner Console, S3 credentials of the project: https://docs.hetzner.com/storage/object-storage/getting-started/generating-s3-keys/ */
export const HETZNER_OBJECT_STORAGE_URL = "https://console.hetzner.com/";

/** Wasabi console: https://docs.wasabi.com/docs/creating-a-user-account-and-access-key */
export const WASABI_CONSOLE_URL = "https://console.wasabisys.com/";

const KEYS_PASTE = "Then paste the access key ID and secret here.";

/** The S3 credentials of a backup destination, for the store its endpoint names. */
export function backupGuide({
	endpoint,
	bucket,
	prefix,
}: BackupGuideInput): CredentialGuide {
	const named = bucket.trim() === "" ? "the bucket" : bucket.trim();
	switch (backupHostKind(endpoint)) {
		case "aws":
			return {
				provider: "AWS S3",
				cta: "Open AWS IAM to create a key for this bucket",
				url: AWS_IAM_CONSOLE_URL,
				prefilled: false,
				permissions: S3_ACTIONS,
				policy: {
					title: "IAM policy for this bucket",
					text: s3BackupPolicy(bucket, prefix),
				},
				note: `Go to Policies, Create policy, JSON and paste this. Attach it to a new IAM user, then create an access key for that user.${
					bucket.trim() === ""
						? " Enter the bucket below to fill its name in."
						: ""
				}`,
				paste: KEYS_PASTE,
				docs: "https://docs.aws.amazon.com/IAM/latest/UserGuide/access-keys-admin-managed.html",
			};
		case "r2":
			return {
				provider: "Cloudflare R2",
				cta: "Create a Cloudflare R2 token with object read and write",
				url: R2_OVERVIEW_URL,
				prefilled: false,
				permissions: [
					{
						label: "Permissions: Object Read & Write",
						why: "write, read and list objects",
					},
					{
						label: `Specify bucket: ${named}`,
						why: "limit the token to this bucket",
					},
				],
				note: "Under Account Details, next to API Tokens, choose Manage, then Create Account API token. The endpoint is https://<ACCOUNT_ID>.r2.cloudflarestorage.com. The secret is shown once.",
				paste: KEYS_PASTE,
				docs: "https://developers.cloudflare.com/r2/api/tokens/",
			};
		case "b2":
			return {
				provider: "Backblaze B2",
				cta: "Create a Backblaze B2 key for this bucket",
				url: B2_APP_KEYS_URL,
				prefilled: false,
				permissions: [
					{
						label: `Allow access to bucket: ${named}`,
						why: "not All",
					},
					{ label: "Type of access: Read and Write" },
				],
				note: "Choose Add a New Application Key. The keyID is the access key ID and the applicationKey the secret; it is shown once. The bucket's page shows the S3 endpoint.",
				paste: KEYS_PASTE,
				docs: "https://www.backblaze.com/docs/cloud-storage-create-and-manage-app-keys",
			};
		case "hetzner":
			return {
				provider: "Hetzner",
				cta: "Open Hetzner Console to generate S3 credentials",
				url: HETZNER_OBJECT_STORAGE_URL,
				prefilled: false,
				permissions: [
					{
						label: "S3 credentials",
						why: "for the project that owns the bucket",
					},
				],
				note: "Open the project, then S3 credentials, Generate credentials. The secret is shown once.",
				paste: KEYS_PASTE,
				docs: "https://docs.hetzner.com/storage/object-storage/getting-started/generating-s3-keys/",
			};
		case "wasabi":
			return {
				provider: "Wasabi",
				cta: "Open Wasabi to create a key for this bucket",
				url: WASABI_CONSOLE_URL,
				prefilled: false,
				permissions: S3_ACTIONS,
				policy: {
					title: "Policy for this bucket",
					text: s3BackupPolicy(bucket, prefix),
				},
				note: "Create a policy with this JSON and attach it to a new user, then create an access key for that user. The secret is shown once.",
				paste: KEYS_PASTE,
				docs: "https://docs.wasabi.com/docs/creating-a-user-account-and-access-key",
			};
		case "selfhosted":
			return {
				provider: "S3-compatible store",
				cta: null,
				url: null,
				prefilled: false,
				permissions: S3_ACTIONS,
				note: "MinIO, Garage and SeaweedFS: create an access key limited to this bucket, with read and write, in the store's admin UI or CLI.",
				paste: KEYS_PASTE,
				docs: "https://docs.aws.amazon.com/AmazonS3/latest/userguide/example-policies-s3.html",
			};
	}
}

// ------------------------------------------------------ notification channels

/**
 * Slack: create an app, turn on Incoming Webhooks, add one to a channel.
 * https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/
 * (the doc links `https://api.slack.com/apps?new_app=1`).
 */
export const SLACK_NEW_APP_URL = "https://api.slack.com/apps?new_app=1";

export function slackWebhookGuide(): CredentialGuide {
	return {
		provider: "Slack",
		cta: "Create a Slack app with an incoming webhook",
		url: SLACK_NEW_APP_URL,
		prefilled: false,
		permissions: [
			{ label: "Create New App, From scratch" },
			{ label: "Incoming Webhooks: On" },
			{
				label: "Add New Webhook to Workspace",
				why: "pick the channel that gets the alerts",
			},
		],
		paste: "Then paste the webhook URL here.",
		docs: "https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/",
	};
}

/**
 * Discord has no page to open: webhooks are made inside a server.
 * https://support.discord.com/hc/en-us/articles/228383668-Intro-to-Webhooks
 */
export function discordWebhookGuide(): CredentialGuide {
	return {
		provider: "Discord",
		cta: null,
		url: null,
		prefilled: false,
		permissions: [
			{ label: "Server Settings, Integrations" },
			{ label: "Create Webhook", why: "then pick the channel" },
			{ label: "Copy Webhook URL" },
		],
		note: "You need the Manage Webhooks permission on the server.",
		paste: "Then paste the webhook URL here.",
		docs: "https://support.discord.com/hc/en-us/articles/228383668-Intro-to-Webhooks",
	};
}

/**
 * Telegram: BotFather makes the bot and its token; the chat id appears in
 * `getUpdates` once someone has messaged the bot.
 * https://core.telegram.org/bots#how-do-i-create-a-bot
 * https://core.telegram.org/bots/api#getupdates
 */
export const TELEGRAM_BOTFATHER_URL = "https://t.me/BotFather";

export function telegramGuide(): CredentialGuide {
	return {
		provider: "Telegram",
		cta: "Open BotFather to create a Telegram bot",
		url: TELEGRAM_BOTFATHER_URL,
		prefilled: false,
		permissions: [
			{ label: "/newbot", why: "BotFather replies with the bot token" },
			{
				label: "Add the bot to the chat",
				why: "as an admin for a channel",
			},
		],
		policy: {
			title: "Find the chat id: send the bot a message, then open this",
			text: "https://api.telegram.org/bot<BOT_TOKEN>/getUpdates",
		},
		note: "The chat id is message.chat.id in the answer; a group's starts with a minus. A public channel works by its @name.",
		paste: "Then paste the bot token here.",
		docs: "https://core.telegram.org/bots#how-do-i-create-a-bot",
	};
}

/**
 * Email goes through any SMTP server, so there is no provider page; the guide
 * says what to ask for.
 */
export function smtpGuide(): CredentialGuide {
	return {
		provider: "SMTP",
		cta: null,
		url: null,
		prefilled: false,
		permissions: [
			{
				label: "Host, port and connection type",
				why: "from your mail provider's SMTP settings",
			},
			{
				label: "A user that may only send",
				why: "an app password where the provider offers one",
			},
		],
		note: "Use a mailbox or relay user made for NebulaCtrl, not a personal login.",
		paste: "Then enter the user name and paste the password here.",
		docs: "https://www.rfc-editor.org/rfc/rfc6409",
	};
}

// -------------------------------------------------------------------- focus

/**
 * After the person comes back from the provider: the first of `ids` whose
 * field is empty, so focus lands where the token goes. Null when all are
 * filled or none exists.
 */
export function firstEmptyId(
	ids: readonly string[],
	read: (id: string) => string | null,
): string | null {
	for (const id of ids) {
		const value = read(id);
		if (value !== null && value.trim() === "") return id;
	}
	return null;
}
