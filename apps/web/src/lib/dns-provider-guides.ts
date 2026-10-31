// Credential guides of the curated DNS-01 providers beyond the first four (those
// are in credential-links.ts): where to create the token and what it needs. Every
// URL was opened on 2026-10-27; the comment above a guide names the page the
// wording comes from. Pages behind a login redirect to the sign-in and back,
// which is what "checked" means for them. Variable names are not here: they
// come from the control plane's provider catalog (pkg/agentprotocol), checked
// against lego v5.5.2, the release Traefik v3.7.16 vendors
// (https://go-acme.github.io/lego/dns/<code>/).
//
// Pure: no DOM, no network.

import type { CredentialGuide } from "./credential-links";

const PASTE = "Then paste it here.";

/** The lego documentation page of a provider code: the variables lego reads. */
export function legoDocsUrl(code: string): string {
	return `https://go-acme.github.io/lego/dns/${code}/`;
}

/**
 * The guide of a provider without a curated form: no token page to open, the
 * lego page that lists the variables and the authentication methods.
 */
export function legoGenericGuide(code: string, name: string): CredentialGuide {
	return {
		provider: name,
		cta: null,
		url: null,
		prefilled: false,
		whyNoLink: "Each provider has its own console; lego's page links it.",
		permissions: [
			{
				label: "The variables of one authentication method",
				why: "the lego page lists them with the provider's own docs",
			},
		],
		note: "Create the token or key in the provider's console, with access to DNS records of the zones you want certificates for. Fill in only the variables of the method you chose.",
		paste: PASTE,
		docs: legoDocsUrl(code),
	};
}

/** DNS-01 provider codes that have a guide in this file. */
export const DNS_GUIDE_CODES = [
	"gandiv5",
	"ovh",
	"porkbun",
	"namecheap",
	"gcloud",
	"azuredns",
	"desec",
	"linode",
	"vultr",
	"scaleway",
	"bunny",
	"dnsimple",
	"ionos",
	"godaddy",
	"netcup",
] as const;

export type DnsGuideCode = (typeof DNS_GUIDE_CODES)[number];

/**
 * Gandi: a Personal Access Token is created per organization, under Sharing,
 * "Create a token"; it takes a name, a validity (7, 30, 60, 90 days or 1 year),
 * the resources (the whole organization or chosen products) and permissions.
 * https://docs.gandi.net/en/managing_an_organization/organizations/personal_access_token.html
 */
function gandiGuide(): CredentialGuide {
	return {
		provider: "Gandi",
		cta: "Open Gandi to create a Personal Access Token",
		url: "https://admin.gandi.net/",
		prefilled: false,
		permissions: [
			{
				label: "Resources: the domain (or the whole organization)",
				why: "limit the token to the domain NebulaCtrl issues certificates for",
			},
			{
				label: "Permission to see and change the domain's DNS records",
				why: "publish and remove the _acme-challenge TXT record",
			},
		],
		note: "In your organization open Sharing, then Create a token. A token expires after at most one year; rotate the credential before then.",
		paste: PASTE,
		docs: "https://docs.gandi.net/en/managing_an_organization/organizations/personal_access_token.html",
	};
}

/**
 * OVHcloud: the token page prefilled with the rights lego documents
 * (POST and DELETE on /domain/zone/*; go-acme.github.io/lego/dns/ovh/). The
 * `?POST=…&DELETE=…` rights form is the one OVH's own guide uses
 * (`createToken/?GET=/me`). The three keys it issues are the application key,
 * its secret and the consumer key.
 * https://docs.ovhcloud.com/en/guides/manage-and-operate/api/first-steps
 */
function ovhGuide(): CredentialGuide {
	return {
		provider: "OVHcloud",
		cta: "Create OVHcloud API keys for DNS records",
		url: "https://api.eu.ovhcloud.com/createToken/?POST=/domain/zone/*&DELETE=/domain/zone/*",
		prefilled: true,
		permissions: [
			{
				label: "POST /domain/zone/*",
				why: "publish the _acme-challenge TXT record",
			},
			{
				label: "DELETE /domain/zone/*",
				why: "remove it again",
			},
		],
		note: "The link selects these rights for the European endpoint (ovh-eu); for Canada use the ovh-ca endpoint and its own token page. You get an application key, its secret and a consumer key: paste all three. An OAuth2 service account (client ID and secret) works instead; use one method only. See the OVHcloud guide on service accounts.",
		paste: PASTE,
		docs: "https://docs.ovhcloud.com/en/guides/manage-and-operate/api/first-steps",
	};
}

/**
 * Porkbun: Account, API Access creates the key pair; API access must also be
 * switched on for each domain (Domain Management, Details, API Access).
 * https://kb.porkbun.com/article/190-getting-started-with-the-porkbun-api
 */
function porkbunGuide(): CredentialGuide {
	return {
		provider: "Porkbun",
		cta: "Create a Porkbun API key",
		url: "https://porkbun.com/account/api",
		prefilled: false,
		permissions: [
			{
				label: "An API key and its secret key",
				why: "both go into the form",
			},
			{
				label: "API Access switched on for the domain",
				why: "Porkbun refuses the API for a domain until you enable it",
			},
		],
		note: "Enable API access per domain under Account, Domain Management, Details.",
		paste: PASTE,
		docs: "https://kb.porkbun.com/article/190-getting-started-with-the-porkbun-api",
	};
}

/**
 * Namecheap: API access is switched on under Profile, Tools, API Access, and
 * "You should whitelist at least one IP before your API access will begin to
 * work" (IPv4 only).
 * https://www.namecheap.com/support/api/intro/
 */
function namecheapGuide(): CredentialGuide {
	return {
		provider: "Namecheap",
		cta: "Open Namecheap API access",
		url: "https://ap.www.namecheap.com/settings/tools/apiaccess/",
		prefilled: false,
		permissions: [
			{ label: "API Access: on", why: "it is off by default" },
			{
				label: "The server's public IPv4 under Whitelisted IPs",
				why: "Namecheap rejects calls from any other address",
			},
		],
		note: "Namecheap only enables API access for accounts that meet its requirements (see the docs). The API user is normally your Namecheap username.",
		paste: PASTE,
		docs: "https://www.namecheap.com/support/api/intro/",
	};
}

/**
 * Google Cloud: the DNS Administrator role (roles/dns.admin) carries
 * dns.changes.create; the service account key is created on the account's
 * Keys tab.
 * https://docs.cloud.google.com/dns/docs/access-control
 * https://docs.cloud.google.com/iam/docs/keys-create-delete
 */
function gcloudGuide(): CredentialGuide {
	return {
		provider: "Google Cloud DNS",
		cta: "Open Google Cloud service accounts",
		url: "https://console.cloud.google.com/iam-admin/serviceaccounts",
		prefilled: false,
		permissions: [
			{
				label:
					"A service account with the DNS Administrator role (roles/dns.admin)",
				why: "create and remove the _acme-challenge record; grant it on the project that holds the zone",
			},
			{
				label: "A JSON key of that service account",
				why: "open the account, Keys, Add key, Create new key, JSON",
			},
		],
		note: "Paste the whole key file: NebulaCtrl stores it on one line. The key is shown once; if your organization blocks key creation, ask an admin to lift the policy for this account.",
		paste: PASTE,
		docs: "https://docs.cloud.google.com/iam/docs/keys-create-delete",
	};
}

/**
 * Azure: an app registration (service principal) with a client secret, and
 * the built-in DNS Zone Contributor role on the zone.
 * https://learn.microsoft.com/en-us/entra/identity-platform/quickstart-register-app
 * https://learn.microsoft.com/en-us/azure/role-based-access-control/built-in-roles/networking#dns-zone-contributor
 */
function azureGuide(): CredentialGuide {
	return {
		provider: "Azure DNS",
		cta: "Open Azure app registrations",
		url: "https://portal.azure.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade",
		prefilled: false,
		permissions: [
			{
				label: "An app registration with a client secret",
				why: "its application (client) ID, directory (tenant) ID and the secret value go into the form",
			},
			{
				label: "Role DNS Zone Contributor on the DNS zone",
				why: "manage record sets in the zone; assign it under the zone's Access control (IAM)",
			},
		],
		note: "Copy the secret's value, not its ID. Enter the subscription ID and resource group below to limit where NebulaCtrl looks for the zone.",
		paste: PASTE,
		docs: "https://learn.microsoft.com/en-us/entra/identity-platform/quickstart-register-app",
	};
}

/**
 * deSEC: tokens are managed on the token page; a token can be scoped with
 * policies per domain.
 * https://desec.readthedocs.io/en/latest/auth/tokens.html
 */
function desecGuide(): CredentialGuide {
	return {
		provider: "deSEC",
		cta: "Open deSEC token management",
		url: "https://desec.io/tokens",
		prefilled: false,
		permissions: [
			{
				label: "A token that can write DNS records",
				why: "publish and remove the _acme-challenge TXT record",
			},
		],
		note: "By default a token can change every domain of the account. To restrict it, add a token policy for the domain (see the docs). The token is shown once.",
		paste: PASTE,
		docs: "https://desec.readthedocs.io/en/latest/auth/tokens.html",
	};
}

/**
 * Linode: username menu, API Tokens, Create a Personal Access Token; each
 * product has an access level. The Domains scope is domains:read_write.
 * https://techdocs.akamai.com/cloud-computing/docs/manage-personal-access-tokens
 * https://techdocs.akamai.com/linode-api/reference/get-started
 */
function linodeGuide(): CredentialGuide {
	return {
		provider: "Linode",
		cta: "Create a Linode personal access token",
		url: "https://cloud.linode.com/profile/tokens",
		prefilled: false,
		permissions: [
			{
				label: "Domains: Read/Write",
				why: "publish and remove the _acme-challenge TXT record; every other product can stay on No Access",
			},
		],
		note: "Choose Create a Personal Access Token, set an expiry and the access level of Domains.",
		paste: PASTE,
		docs: "https://techdocs.akamai.com/cloud-computing/docs/manage-personal-access-tokens",
	};
}

/**
 * Vultr: the API page is under Account settings; the API reference covers the
 * DNS endpoints. Vultr can limit API calls by IP address.
 * https://www.vultr.com/api/
 */
function vultrGuide(): CredentialGuide {
	return {
		provider: "Vultr",
		cta: "Open Vultr API settings",
		url: "https://console.vultr.com/settings/#settingsapi",
		prefilled: false,
		permissions: [
			{
				label: "API enabled, with an API key",
				why: "the key goes into the form",
			},
			{
				label: "The server's public IP in the access control list",
				why: "if the account limits API calls by address, the proxy's calls need to be allowed",
			},
		],
		paste: PASTE,
		docs: "https://www.vultr.com/api/",
	};
}

/**
 * Scaleway: an IAM API key; the permission set DomainsDNSFullAccess grants
 * "create, read, list, edit and delete Domains and DNS".
 * https://www.scaleway.com/en/docs/iam/credentials/create-api-keys/
 * https://www.scaleway.com/en/docs/iam/policies-permissions/permission-sets/
 */
function scalewayGuide(): CredentialGuide {
	return {
		provider: "Scaleway",
		cta: "Open Scaleway API keys",
		url: "https://console.scaleway.com/iam/api-keys",
		prefilled: false,
		permissions: [
			{
				label: "Permission set DomainsDNSFullAccess",
				why: "edit the zone's records; attach it to the IAM user or application that owns the key through a policy",
			},
			{
				label: "A secret key (and its access key)",
				why: "the secret key is required; the access key and project ID are optional",
			},
		],
		note: "An API key has the permissions of the IAM user or application it belongs to, so create a policy for that principal first.",
		paste: PASTE,
		docs: "https://www.scaleway.com/en/docs/iam/credentials/create-api-keys/",
	};
}

/**
 * Bunny: "Find your API key in the account settings under API Keys"; the core
 * API authenticates with the AccessKey header.
 * https://bunny.net/docs/api-reference/core
 */
function bunnyGuide(): CredentialGuide {
	return {
		provider: "Bunny",
		cta: "Open bunny.net account API key",
		url: "https://dash.bunny.net/account/api-key",
		prefilled: false,
		permissions: [
			{
				label: "The account API key",
				why: "it can manage every DNS zone of the account",
			},
		],
		note: "This is an account-level key: it can change every DNS zone of the account. Keep it out of anything else you share.",
		paste: PASTE,
		docs: "https://bunny.net/docs/api-reference/core",
	};
}

/**
 * DNSimple: an account API token (a user token is rejected by lego) from the
 * API & Access page of the account, which sits under the account's own number,
 * so there is no link to open. Scoped tokens need the Solo Pro plan or higher.
 * https://support.dnsimple.com/articles/api-access-token/
 */
function dnsimpleGuide(): CredentialGuide {
	return {
		provider: "DNSimple",
		cta: "Open DNSimple",
		url: null,
		prefilled: false,
		whyNoLink:
			"The page is under your account number: choose API & Access in the account menu.",
		permissions: [
			{
				label: "An account access token, not a user token",
				why: "lego's DNSimple provider rejects user tokens",
			},
			{
				label: "Zones: full access",
				why: "only if you scope the token (a plan feature); an unscoped account token already has it",
				optional: true,
			},
		],
		note: "Log in, select the account, go to API & Access and choose Add. The token is shown once.",
		paste: PASTE,
		docs: "https://support.dnsimple.com/articles/api-access-token/",
	};
}

/**
 * IONOS: the Hosting Developer Portal issues keys written <public prefix>.<secret>;
 * "copy both the public prefix and the secret".
 * https://developer.hosting.ionos.com/docs/getstarted
 */
function ionosGuide(): CredentialGuide {
	return {
		provider: "IONOS",
		cta: "Open the IONOS developer portal keys",
		url: "https://developer.hosting.ionos.com/keys",
		prefilled: false,
		permissions: [
			{
				label: "A key of the DNS API",
				why: "enter it as <public prefix>.<secret>",
			},
		],
		note: "The secret part is shown once. Join the prefix and the secret with a dot.",
		paste: PASTE,
		docs: "https://developer.hosting.ionos.com/docs/getstarted",
	};
}

/**
 * GoDaddy: lego's provider authenticates with a classic developer key
 * (Production), which GoDaddy says is "scheduled for retirement"; a Personal
 * Access Token is not accepted by it.
 * https://developer.godaddy.com/en/docs/api-users/auth/how-to
 */
function godaddyGuide(): CredentialGuide {
	return {
		provider: "GoDaddy",
		cta: "Open GoDaddy developer keys",
		url: "https://developer.godaddy.com/keys",
		prefilled: false,
		permissions: [
			{
				label: "A new Production key",
				why: "copy both the key and the secret; the secret is shown once",
			},
		],
		note: "This is GoDaddy's classic developer key. GoDaddy has announced its retirement and limits the DNS API to accounts with enough domains; if it stops working, move the zone's DNS to another provider or delegate _acme-challenge with a CNAME.",
		paste: PASTE,
		docs: "https://developer.godaddy.com/en/docs/api-users/auth/how-to",
	};
}

/**
 * Netcup: the Customer Control Panel, Master data, API creates a (legacy) API
 * key and password; the password is shown once. Lego's provider uses the
 * legacy API with the customer number.
 * https://www.netcup.com/en/helpcenter/documentation/domain/our-api
 */
function netcupGuide(): CredentialGuide {
	return {
		provider: "Netcup",
		cta: "Open the netcup Customer Control Panel",
		url: "https://www.customercontrolpanel.de/",
		prefilled: false,
		permissions: [
			{
				label: "Master data, API: a Legacy API key and a Legacy API password",
				why: "with your customer number, all three go into the form",
			},
		],
		note: "Open Master data, then API. The password can only be regenerated, not read again.",
		paste: PASTE,
		docs: "https://www.netcup.com/en/helpcenter/documentation/domain/our-api",
	};
}

const GUIDES: Record<DnsGuideCode, () => CredentialGuide> = {
	gandiv5: gandiGuide,
	ovh: ovhGuide,
	porkbun: porkbunGuide,
	namecheap: namecheapGuide,
	gcloud: gcloudGuide,
	azuredns: azureGuide,
	desec: desecGuide,
	linode: linodeGuide,
	vultr: vultrGuide,
	scaleway: scalewayGuide,
	bunny: bunnyGuide,
	dnsimple: dnsimpleGuide,
	ionos: ionosGuide,
	godaddy: godaddyGuide,
	netcup: netcupGuide,
};

/** The guide of a curated provider defined in this file, or null for any other code. */
export function curatedDnsGuide(code: string): CredentialGuide | null {
	return (DNS_GUIDE_CODES as readonly string[]).includes(code)
		? GUIDES[code as DnsGuideCode]()
		: null;
}
