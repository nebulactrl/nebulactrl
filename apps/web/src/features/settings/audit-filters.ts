// The audit log's server-side filter: one chip per action family, sent as the
// `type` query of GET /audit-events (an action prefix such as `deployment.`).
// Pure, so the chip -> query mapping and the per-filter copy are tested.

export type AuditFilterId =
	| "all"
	| "deployments"
	| "services"
	| "variables"
	| "secrets"
	| "servers"
	| "tokens"
	| "projects";

export interface AuditFilter {
	id: AuditFilterId;
	label: string;
	/** The `type` query value; undefined for "All". */
	prefix: string | undefined;
	/** What an empty result says: a title and one sentence of what lands here. */
	empty: { title: string; body: string };
}

export const AUDIT_FILTERS: readonly AuditFilter[] = [
	{
		id: "all",
		label: "All",
		prefix: undefined,
		empty: {
			title: "Nothing recorded yet",
			body: "Every change — projects, services, variables, domains, deploys, tokens, servers — is written here with who made it.",
		},
	},
	{
		id: "deployments",
		label: "Deployments",
		prefix: "deployment.",
		empty: {
			title: "No deployment events yet",
			body: "Deploys, rollbacks and cancellations show up here once someone runs one.",
		},
	},
	{
		id: "services",
		label: "Services",
		prefix: "service.",
		empty: {
			title: "No service events yet",
			body: "Creating, changing, scaling, restarting and deleting a service is recorded here.",
		},
	},
	{
		id: "variables",
		label: "Variables",
		prefix: "variable.",
		empty: {
			title: "No variable events yet",
			body: "Setting or deleting a variable is recorded here. Only the key is kept, never the value.",
		},
	},
	{
		id: "secrets",
		label: "Secrets",
		prefix: "secret",
		empty: {
			title: "No secret access recorded yet",
			body: "Every external secret a deploy reads, every revealed value and every secret manager change is recorded here: the reference and the service, never the value.",
		},
	},
	{
		id: "servers",
		label: "Servers",
		prefix: "server.",
		empty: {
			title: "No server events yet",
			body: "Enrolling, changing, draining and revoking a server is recorded here.",
		},
	},
	{
		id: "tokens",
		label: "Tokens",
		prefix: "token.",
		empty: {
			title: "No token events yet",
			body: "Creating and revoking an API token is recorded here.",
		},
	},
	{
		id: "projects",
		label: "Projects",
		prefix: "project.",
		empty: {
			title: "No project events yet",
			body: "Creating, renaming and deleting a project is recorded here.",
		},
	},
];

const FALLBACK = AUDIT_FILTERS[0] as AuditFilter;

export function auditFilterById(id: AuditFilterId): AuditFilter {
	return AUDIT_FILTERS.find((filter) => filter.id === id) ?? FALLBACK;
}

/**
 * The query of GET /audit-events for a filter. `type` is left out (not sent
 * empty) for "All", so the server applies no prefix.
 */
export function auditQuery(
	id: AuditFilterId,
	limit: number,
): { limit: number; type?: string } {
	const prefix = auditFilterById(id).prefix;
	return prefix === undefined ? { limit } : { limit, type: prefix };
}

export type AuditActionKind = "create" | "remove" | "change";

const REMOVE =
	/(?:^|[._])(?:delete|deleted|revoke|revoked|remove|removed|cancel|canceled|discard_change|fail|denied)(?:$|[._])/;
const CREATE = /(?:^|[._])(?:create|created|add|added|enroll)(?:$|[._])/;

/**
 * What an action does to its resource, read from the verb after the family:
 * `token.create` makes something, `deployment.cancel` ends or removes one,
 * everything else (`service.update`, `variable.set`) changes it.
 */
export function auditActionKind(action: string): AuditActionKind {
	const verb = action.slice(action.indexOf(".") + 1);
	if (REMOVE.test(verb)) return "remove";
	if (CREATE.test(verb)) return "create";
	return "change";
}
