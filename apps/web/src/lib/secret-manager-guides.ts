// Credential setup guides for the external secret managers (Team settings →
// Integrations → Secret managers): what to create in Vault, OpenBao or
// Infisical so NebulaCtrl can read secrets, and the exact commands. Same
// shape and rendering as `lib/credential-links.ts` (`CredentialHelp`).
//
// Pure: no DOM, no network. Every URL names the doc it was checked against
// (verified 2026-10-27).

import { type CredentialGuide, normalizeBaseUrl } from "#/lib/credential-links";

export type SecretManagerKind = "vault" | "infisical" | "doppler";
export type VaultAuth = "token" | "approle";

/** https://developer.hashicorp.com/vault/docs/auth/approle */
export const VAULT_APPROLE_DOCS =
	"https://developer.hashicorp.com/vault/docs/auth/approle";

/** https://developer.hashicorp.com/vault/docs/auth/token */
export const VAULT_TOKEN_DOCS =
	"https://developer.hashicorp.com/vault/docs/auth/token";

/** https://infisical.com/docs/documentation/platform/identities/universal-auth */
export const INFISICAL_UNIVERSAL_AUTH_DOCS =
	"https://infisical.com/docs/documentation/platform/identities/universal-auth";

/** https://docs.doppler.com/docs/service-tokens */
export const DOPPLER_SERVICE_TOKENS_DOCS =
	"https://docs.doppler.com/docs/service-tokens";

/** The Doppler API a new integration talks to. */
export const DOPPLER_DEFAULT_URL = "https://api.doppler.com";

/** The Infisical Cloud URL a new integration starts with. */
export const INFISICAL_DEFAULT_URL = "https://us.infisical.com";

const MOUNT = /^[A-Za-z0-9._-]+(\/[A-Za-z0-9._-]+)*$/;

/** A KV v2 mount path as the policy needs it: trimmed, `secret` when blank or invalid. */
export function vaultMount(raw: string): string {
	const mount = raw.trim().replace(/^\/+|\/+$/g, "");
	return MOUNT.test(mount) && !mount.includes("..") ? mount : "secret";
}

/**
 * The read-only policy NebulaCtrl needs: `read` on the KV v2 data of the
 * mount. In KV v2 the data lives under `<mount>/data/`, not `<mount>/`.
 * https://developer.hashicorp.com/vault/docs/secrets/kv/kv-v2#acl-rules
 */
export function vaultPolicy(mount: string): string {
	return `path "${vaultMount(mount)}/data/*" {\n  capabilities = ["read"]\n}\n`;
}

/** Vault and OpenBao share the API, so one guide covers both. */
export function vaultGuide({
	auth,
	mount,
}: {
	auth: VaultAuth;
	mount: string;
}): CredentialGuide {
	const policy = vaultPolicy(mount);
	const base = {
		provider: "Vault / OpenBao",
		cta: null,
		url: null,
		prefilled: false,
		whyNoLink:
			"Vault and OpenBao run on your own server, so there is no page to open.",
	} as const;
	if (auth === "approle") {
		return {
			...base,
			permissions: [
				{
					label: `read on ${vaultMount(mount)}/data/*`,
					why: "NebulaCtrl reads the secrets your variables reference, when a deploy starts",
				},
				{
					label: "An AppRole with that policy",
					why: "it signs in with a role id and a secret id, so no long-lived token is stored",
				},
			],
			policy: {
				title: "Policy, then run on your Vault or OpenBao server",
				text: `${policy}\n# save it as nebulactrl.hcl, then:\nvault policy write nebulactrl nebulactrl.hcl\nvault auth enable approle\nvault write auth/approle/role/nebulactrl token_policies=nebulactrl token_type=batch token_ttl=10m secret_id_ttl=0\nvault read auth/approle/role/nebulactrl/role-id\nvault write -f auth/approle/role/nebulactrl/secret-id`,
			},
			note: "Batch tokens expire by themselves, which suits NebulaCtrl: it signs in once per deploy and never renews. Narrow the policy path to the secrets this team needs.",
			paste: "Then enter the role id and paste the secret id here.",
			docs: VAULT_APPROLE_DOCS,
		};
	}
	return {
		...base,
		permissions: [
			{
				label: `read on ${vaultMount(mount)}/data/*`,
				why: "NebulaCtrl reads the secrets your variables reference, when a deploy starts",
			},
			{
				label: "The default policy",
				why: "it lets the token look itself up, which the connection test uses",
			},
		],
		policy: {
			title: "Policy, then run on your Vault or OpenBao server",
			text: `${policy}\n# save it as nebulactrl.hcl, then:\nvault policy write nebulactrl nebulactrl.hcl\nvault token create -policy=nebulactrl -period=720h`,
		},
		note: "A token stops working when its period ends and NebulaCtrl does not renew it. For production, prefer AppRole.",
		paste: "Then paste the token here.",
		docs: VAULT_TOKEN_DOCS,
	};
}

/** An Infisical machine identity with universal auth, added to the project. */
export function infisicalGuide(siteUrl: string): CredentialGuide {
	const site = normalizeBaseUrl(siteUrl) ?? INFISICAL_DEFAULT_URL;
	return {
		provider: "Infisical",
		cta: "Open the machine identity guide",
		url: INFISICAL_UNIVERSAL_AUTH_DOCS,
		prefilled: false,
		permissions: [
			{
				label: "Access Control → Machine Identities → Create",
				why: "the identity NebulaCtrl signs in as, with universal auth",
			},
			{
				label: "Add Client Secret",
				why: "the client id and secret it signs in with",
			},
			{
				label: "Project → Access Control → Add Machine Identity",
				why: "give it a project role that can read secrets (Viewer is enough)",
			},
		],
		note: `The client secret is shown once. The project id is in the project's settings. Signing in goes to ${site}.`,
		paste:
			"Then enter the client id and project id and paste the client secret here.",
		docs: INFISICAL_UNIVERSAL_AUTH_DOCS,
	};
}

/**
 * A Doppler token. A service token is bound to one config (the best fit: it is
 * read-only and reaches nothing else); a service account or personal token
 * needs the project and config named in each reference or set as the
 * integration's default.
 * https://docs.doppler.com/docs/service-tokens
 * https://docs.doppler.com/docs/service-accounts
 * https://docs.doppler.com/reference/api (token types, `Authorization: Bearer`)
 */
export function dopplerGuide(siteUrl: string): CredentialGuide {
	const site = normalizeBaseUrl(siteUrl) ?? DOPPLER_DEFAULT_URL;
	return {
		provider: "Doppler",
		cta: "Open the service token guide",
		url: DOPPLER_SERVICE_TOKENS_DOCS,
		prefilled: false,
		permissions: [
			{
				label: "Project → Config → Access → Generate",
				why: "a service token is read-only and bound to that one config, so the reference needs no project or config",
			},
			{
				label: "Or a service account token with read access",
				why: "one token for several projects (Team and Enterprise plans); then name the project and config in each reference or set a default here",
				optional: true,
			},
		],
		note: `The token is shown once. Do not use a personal or CLI token in a live environment: it has the write access of the person who made it. NebulaCtrl downloads the config's computed secrets once per deploy from ${site} and never asks for dynamic secrets.`,
		paste: "Then paste the token here.",
		docs: DOPPLER_SERVICE_TOKENS_DOCS,
	};
}

/** What a reference looks like for each kind, with the part that varies named. */
export function referenceExample(
	kind: SecretManagerKind,
	name: string,
	defaultsSet = false,
): string {
	const n = name.trim() === "" ? "name" : name.trim();
	switch (kind) {
		case "infisical":
			return `\${{vault:${n}/prod/backend#DATABASE_URL}}`;
		case "doppler":
			return defaultsSet
				? `\${{doppler:${n}#DATABASE_URL}}`
				: `\${{doppler:${n}/backend/prd#DATABASE_URL}}`;
		default:
			return `\${{vault:${n}/apps/shop/db#password}}`;
	}
}

/** How the path of a reference is read, in one line, for the form. */
export function referenceHelp(kind: SecretManagerKind): string {
	switch (kind) {
		case "infisical":
			return "The path is the environment slug, then the folder: prod/backend. The part after # is the secret's name.";
		case "doppler":
			return "The path is the Doppler project and config: backend/prd. Leave it out when a default (or a service token) fixes them. The part after # is the secret's name.";
		default:
			return "The path is the secret's path under the mount: apps/shop/db. The part after # is the field to read.";
	}
}
