// API token presets and the scope vocabulary. Pure, so the mapping between a
// preset and the scopes it sends (and back, for the token list) is tested.

export const ALL_SCOPES = [
	"projects:read",
	"logs:read",
	"projects:write",
	"deployments:write",
	"deployments:rollback",
	"servers:read",
	"servers:write",
	"proxy:write",
	"admin",
] as const;
export type Scope = (typeof ALL_SCOPES)[number];

export const SCOPE_LABEL: Record<Scope, string> = {
	"projects:read": "Read projects, services, environments and deployments",
	"logs:read": "Read runtime, build and command logs",
	"projects:write":
		"Create and change projects, services, variables and domains",
	"deployments:write": "Deploy, restart and stop services",
	"deployments:rollback": "Roll back deployments",
	"servers:read": "Read servers and their metrics",
	"servers:write": "Enrol, change and revoke servers",
	"proxy:write":
		"Publish traffic: routes, TCP/UDP ports, certificates, tailnet",
	admin: "Everything, including tokens and the audit log",
};

export type PresetId =
	| "full"
	| "read-only"
	| "mcp-read"
	| "mcp-deploy"
	| "mcp-configure"
	| "custom";

/**
 * mcp: an AI agent; works only on /mcp. rest: the REST API, the CLI and CI;
 * works only on /api/v1. Each is refused on the other.
 */
export type TokenKind = "mcp" | "rest";

/** The kind a new token gets: the preset's, or the one chosen for Custom. */
export function tokenKindFor(
	preset: PresetId,
	customKind: TokenKind,
): TokenKind {
	const kind = TOKEN_PRESETS.find((p) => p.id === preset)?.kind;
	return kind ?? customKind;
}

/**
 * Only the instance admin or a team Owner/Admin can create a token that skips
 * production approvals; the server enforces it too.
 */
export function canSkipProductionApproval(
	role: string | null | undefined,
	instanceAdmin: boolean,
): boolean {
	return instanceAdmin || role === "owner" || role === "admin";
}

export interface TokenPreset {
	id: PresetId;
	label: string;
	/** A few words under the label on its option card. */
	summary: string;
	hint: string;
	/** Suggested token name. */
	name: string;
	scopes: readonly Scope[];
	/** What the token is for; null (Custom) lets the person choose. */
	kind: TokenKind | null;
}

export const TOKEN_PRESETS: readonly TokenPreset[] = [
	{
		id: "full",
		kind: "rest",
		label: "Full access",
		summary: "Everything you can do",
		hint: "Everything a signed-in user can do. For your own scripts.",
		name: "full-access",
		scopes: ALL_SCOPES,
	},
	{
		id: "read-only",
		kind: "rest",
		label: "Read-only",
		summary: "Look, never change",
		hint: "Look at projects, services, deployments, logs and servers. Cannot change anything.",
		name: "read-only",
		scopes: ["projects:read", "logs:read", "servers:read"],
	},
	{
		id: "mcp-read",
		kind: "mcp",
		label: "MCP read-only",
		summary: "AI client, inspect only",
		hint: "For an AI client that may look and diagnose (services, deployments, servers and logs) but never change anything. Start here.",
		name: "mcp-read-only",
		scopes: ["projects:read", "logs:read", "servers:read"],
	},
	{
		id: "mcp-deploy",
		kind: "mcp",
		label: "MCP deploy",
		summary: "AI client, can deploy",
		hint: "Read-only plus applying staged changes, restarts and one-off commands. It cannot stage edits itself, so changes you made in the dashboard can be shipped by the client. Changes to production still need your approval. Rollback stays off; add it under Custom.",
		name: "mcp-deploy",
		scopes: ["projects:read", "logs:read", "servers:read", "deployments:write"],
	},
	{
		id: "mcp-configure",
		kind: "mcp",
		label: "MCP configure",
		summary: "AI client, can edit and deploy",
		hint: "MCP deploy plus staging changes to services, variables, domains and volumes. Nothing runs until applied, and changes to production still need your approval. Rollback and proxy publishing stay off.",
		name: "mcp-configure",
		scopes: [
			"projects:read",
			"logs:read",
			"servers:read",
			"projects:write",
			"deployments:write",
		],
	},
	{
		id: "custom",
		kind: null,
		label: "Custom",
		summary: "Pick the scopes",
		hint: "Pick the scopes yourself.",
		name: "",
		scopes: [],
	},
];

export function presetById(id: PresetId): TokenPreset {
	return (
		TOKEN_PRESETS.find((p) => p.id === id) ?? TOKEN_PRESETS[0] ?? fallbackPreset
	);
}

const fallbackPreset: TokenPreset = {
	id: "custom",
	kind: null,
	label: "Custom",
	summary: "",
	hint: "",
	name: "",
	scopes: [],
};

function sameSet(a: readonly string[], b: readonly string[]): boolean {
	if (a.length !== b.length) return false;
	const set = new Set(a);
	return b.every((item) => set.has(item));
}

/**
 * A short name for a token's scopes in the list: "Full access" when it has
 * every scope, "Read-only" for the two read scopes, "Read + deploy" for the
 * MCP deploy set, otherwise the scopes themselves joined.
 */
export function describeScopes(
	scopes: readonly string[] | null | undefined,
): string {
	const list = scopes ?? [];
	if (list.length === 0) return "No scopes";
	if (list.includes("admin") || sameSet(list, ALL_SCOPES)) return "Full access";
	// Tokens created before logs:read existed carry projects:read without it
	// (a migration adds it, but the list still names both shapes).
	const readSet = list.filter((s) => s !== "logs:read");
	if (sameSet(readSet, ["projects:read", "servers:read"])) return "Read-only";
	if (
		sameSet(readSet, ["projects:read", "servers:read", "deployments:write"])
	) {
		return "Read + deploy";
	}
	return list.join(", ");
}

export interface Expiry {
	id: "never" | "30d" | "90d" | "365d";
	label: string;
	days: number | null;
}

export const EXPIRIES: readonly Expiry[] = [
	{ id: "never", label: "No expiry", days: null },
	{ id: "30d", label: "30 days", days: 30 },
	{ id: "90d", label: "90 days", days: 90 },
	{ id: "365d", label: "1 year", days: 365 },
];

/** ISO timestamp `days` from `now`, or undefined for "No expiry". */
export function expiresAtFor(
	days: number | null,
	now = new Date(),
): string | undefined {
	if (days === null) return undefined;
	return new Date(now.getTime() + days * 86_400_000).toISOString();
}

/** A token is usable until it is revoked or its expiry passes. */
export function tokenState(
	token: { revokedAt?: string | undefined; expiresAt?: string | undefined },
	now = Date.now(),
): "active" | "revoked" | "expired" {
	if (token.revokedAt) return "revoked";
	if (token.expiresAt && Date.parse(token.expiresAt) <= now) return "expired";
	return "active";
}

/** The client config an MCP client reads: `mcpServers` with a bearer header. */
export function mcpClientConfig(
	mcpUrl: string,
	token = "<your-token>",
): string {
	return JSON.stringify(
		{
			mcpServers: {
				nebulactrl: {
					url: mcpUrl,
					headers: { Authorization: `Bearer ${token}` },
				},
			},
		},
		null,
		2,
	);
}
