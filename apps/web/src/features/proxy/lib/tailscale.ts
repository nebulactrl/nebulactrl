// Settings → Integrations → Tailscale: the policy snippet and tag handling.

import type { TailscaleIntegration } from "#/api/gen/types.gen";

/** OAuth scopes the client needs: minting auth keys and listing/deleting devices. */
export const REQUIRED_OAUTH_SCOPES = ["auth_keys", "devices:core"] as const;

export const DEFAULT_TAG = "tag:nebula-svc";

/** Split tags typed as comma/space/newline separated text, de-duplicated. */
export function parseTags(text: string): string[] {
	const seen: string[] = [];
	for (const part of text.split(/[\s,]+/)) {
		const tag = part.trim();
		if (tag !== "" && !seen.includes(tag)) seen.push(tag);
	}
	return seen;
}

/** A Tailscale tag: `tag:` and a name of letters, digits and dashes. */
export function validateTag(tag: string): string | null {
	return /^tag:[A-Za-z][A-Za-z0-9-]*$/.test(tag)
		? null
		: `“${tag}” is not a tag. Use tag:name, starting with a letter.`;
}

export interface TagErrors {
	defaultTags?: string;
	allowedTags?: string;
}

/** The two tag lists, checked the way the control plane does (default ⊆ allowed). */
export function validateTags(
	defaultTags: readonly string[],
	allowedTags: readonly string[],
): TagErrors {
	const errors: TagErrors = {};
	const badDefault = defaultTags.map(validateTag).find((p) => p !== null);
	if (badDefault) errors.defaultTags = badDefault;
	else if (defaultTags.length === 0) {
		errors.defaultTags = "Give at least one default tag.";
	}
	const badAllowed = allowedTags.map(validateTag).find((p) => p !== null);
	if (badAllowed) errors.allowedTags = badAllowed;
	else if (allowedTags.length > 0) {
		const missing = defaultTags.find((tag) => !allowedTags.includes(tag));
		if (missing) {
			errors.allowedTags = `Allowed tags must include every default tag (${missing}).`;
		}
	}
	return errors;
}

/** The owner tag held by the OAuth client; it owns the tags minted keys carry. */
export const OWNER_TAG = "tag:nebula";

/**
 * The tailnet policy to paste into the Tailscale admin console (HuJSON): the
 * OAuth client's owner tag owns the tags NebulaCtrl mints keys for, and the
 * tagged proxy nodes may reach the devices a route should forward to. NebulaCtrl
 * never edits the policy itself.
 */
export function policySnippet(tags: readonly string[]): string {
	const list = (tags.length > 0 ? tags : [DEFAULT_TAG]).filter(
		(tag) => tag !== OWNER_TAG,
	);
	const owners = list
		.map((tag) => `    "${tag}": ["${OWNER_TAG}"]`)
		.join(",\n");
	const dst = list.map((tag) => `"${tag}:*"`).join(", ");
	const src = list.map((tag) => `"${tag}"`).join(", ");
	return [
		"{",
		'  "tagOwners": {',
		`    "${OWNER_TAG}": ["autogroup:admin"], // held by the OAuth client`,
		`${owners}`,
		"  },",
		'  "acls": [',
		"    // members reach the NebulaCtrl proxy nodes (their tailnet name)",
		`    {"action": "accept", "src": ["autogroup:member"], "dst": [${dst}]},`,
		"    // the proxy nodes reach the tailnet devices your routes forward to;",
		"    // narrow dst to those devices and ports",
		`    {"action": "accept", "src": [${src}], "dst": ["*:*"]}`,
		"  ]",
		"}",
	].join("\n");
}

/** A device row's address: the first IPv4 address, else the first of any family. */
export function primaryAddress(addresses: readonly string[] | null): string {
	const list = addresses ?? [];
	return list.find((a) => a.includes(".")) ?? list[0] ?? "";
}

/** A picked device's address in front of whatever port the field already holds: `100.64.0.2:8123`. */
export function withHost(current: string, host: string): string {
	const port = /:(\d+)$/.exec(current)?.[1];
	return port ? `${host}:${port}` : `${host}:`;
}

/** Which control plane the workspace's tailnet integration talks to. */
export type IntegrationProvider = TailscaleIntegration["provider"];

/** How Headscale is reached: an API key mints keys, or one stored pre-auth key. */
export type HeadscaleCredential = TailscaleIntegration["headscaleCredential"];

export const HEADSCALE_CREDENTIALS: readonly {
	id: HeadscaleCredential;
	label: string;
	hint: string;
}[] = [
	{
		id: "apikey",
		label: "API key",
		hint: "NebulaCtrl mints single-use pre-auth keys and can delete nodes.",
	},
	{
		id: "preauth",
		label: "Pre-auth key",
		hint: "One reusable tagged key for every service; nodes cannot be removed through the API.",
	},
];

/** The Headscale URL as the control plane checks it, or what is wrong with it. */
export function validateLoginServer(raw: string): string | null {
	const text = raw.trim();
	if (text === "") return "The Headscale server URL is required.";
	let url: URL;
	try {
		url = new URL(text);
	} catch {
		return "Use a full URL such as https://headscale.example.com.";
	}
	if (url.protocol !== "https:" && url.protocol !== "http:") {
		return "Use an http:// or https:// URL.";
	}
	if (
		url.username !== "" ||
		url.password !== "" ||
		url.search !== "" ||
		url.hash !== ""
	) {
		return "Leave out credentials, a query and a fragment.";
	}
	return null;
}

/**
 * A Headscale policy (HuJSON) that lets the user own the service tags and lets
 * members reach tagged nodes. NebulaCtrl never edits the policy itself.
 */
export function headscalePolicySnippet(
	tags: readonly string[],
	user: string,
): string {
	const list = tags.length > 0 ? tags : [DEFAULT_TAG];
	const owner = user.trim() === "" ? "your-user@" : `${user.trim()}@`;
	const owners = list.map((tag) => `    "${tag}": ["${owner}"]`).join(",\n");
	const dst = list.map((tag) => `"${tag}:*"`).join(", ");
	return [
		"{",
		'  "tagOwners": {',
		owners,
		"  },",
		'  "acls": [',
		"    // members reach the service nodes; narrow dst to the ports you expose",
		`    {"action": "accept", "src": ["autogroup:member"], "dst": [${dst}]}`,
		"  ]",
		"}",
	].join("\n");
}

/**
 * Why a server cannot use the managed egress node, or null. The per-server
 * "Join tailnet" node registers with an OAuth client, so a Headscale
 * integration cannot offer it.
 */
export function managedEgressBlocker(
	provider: IntegrationProvider | undefined,
): string | null {
	return provider === "headscale"
		? "Joining a server's proxy to the tailnet needs a Tailscale OAuth client; your integration is Headscale, which is for per-service nodes only."
		: null;
}

/** The connection as typed, for comparing with what is stored. */
export interface ConnectionDraft {
	provider: IntegrationProvider;
	clientId: string;
	loginServer: string;
	credential: HeadscaleCredential;
}

const trimSlashes = (url: string): string => url.trim().replace(/\/+$/, "");

/**
 * Whether the stored secret still belongs to this connection. The control
 * plane demands a new secret when the provider, the OAuth client, the
 * Headscale server or the Headscale credential kind changes.
 */
export function keepsStoredSecret(
	draft: ConnectionDraft,
	stored: Pick<
		TailscaleIntegration,
		| "secretSet"
		| "provider"
		| "clientId"
		| "loginServer"
		| "headscaleCredential"
	>,
): boolean {
	if (!stored.secretSet || draft.provider !== stored.provider) return false;
	if (draft.provider === "tailscale_oauth") {
		return draft.clientId.trim() === stored.clientId;
	}
	return (
		draft.credential === stored.headscaleCredential &&
		trimSlashes(draft.loginServer) === trimSlashes(stored.loginServer)
	);
}
