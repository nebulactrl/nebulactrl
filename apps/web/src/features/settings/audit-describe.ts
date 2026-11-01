// What an audit row's Details cell says. The server stores each event's
// metadata as a loose map (secrets never in it); this turns the actions that
// carry a recognisable shape into one sentence and leaves the rest as a short
// "key value" line. Pure, so the sentences are tested.

type Metadata = Record<string, unknown> | null | undefined;

const text = (value: unknown): string | undefined =>
	typeof value === "string" && value !== "" ? value : undefined;

const list = (value: unknown): string[] =>
	Array.isArray(value)
		? value.filter((v): v is string => typeof v === "string")
		: [];

function joinNames(names: string[], max = 3): string {
	if (names.length <= max) return names.join(", ");
	return `${names.slice(0, max).join(", ")} and ${names.length - max} more`;
}

const plural = (n: number, one: string, many = `${one}s`): string =>
	`${n} ${n === 1 ? one : many}`;

/** "2026-10-29T14:00:00Z" as "2026-10-29 14:00 UTC"; anything else as it came. */
function utcMinute(value: unknown): string | undefined {
	const raw = text(value);
	if (!raw) return undefined;
	const at = new Date(raw);
	if (Number.isNaN(at.getTime())) return raw;
	return `${at.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** The sentences for status pages, incidents and maintenance windows. */
function describeStatus(
	action: string,
	meta: NonNullable<Metadata>,
): string | undefined {
	const title = text(meta.title);
	if (!title) return undefined;
	const changed = list(meta.changed);
	const changes = changed.length > 0 ? joinNames(changed, 4) : undefined;
	switch (action) {
		case "status_page.create": {
			const host = text(meta.hostname);
			return `Created status page ${title}${host ? ` on ${host}` : ""}`;
		}
		case "status_page.update": {
			const host = text(meta.hostname);
			if (changed.includes("published")) {
				return meta.published === true
					? `Published status page ${title}${host ? ` on ${host}` : ""}`
					: `Unpublished status page ${title}`;
			}
			return changes
				? `Changed ${changes} of status page ${title}`
				: `Saved status page ${title}`;
		}
		case "status_page.delete":
			return `Deleted status page ${title}`;
		case "status_page.components": {
			const count = typeof meta.components === "number" ? meta.components : 0;
			return `Set ${plural(count, "component")} of status page ${title}`;
		}
		case "status_incident.create": {
			const impact = text(meta.impact);
			const status = text(meta.status);
			const detail = [impact ? `${impact} impact` : undefined, status]
				.filter(Boolean)
				.join(", ");
			return `Opened incident ${title}${detail ? ` (${detail})` : ""}`;
		}
		case "status_incident.update":
			return changes
				? `Changed ${changes} of incident ${title}`
				: `Saved incident ${title}`;
		case "status_incident.post": {
			const status = text(meta.status);
			const before = text(meta.previousStatus);
			if (status === "resolved") return `Resolved incident ${title}`;
			if (status && before && status !== before) {
				return `Posted an update to incident ${title}: ${before} to ${status}`;
			}
			return `Posted an update to incident ${title}${status ? ` (${status})` : ""}`;
		}
		case "status_incident.delete":
			return `Deleted incident ${title}`;
		case "maintenance_window.create": {
			const from = utcMinute(meta.startsAt);
			const to = utcMinute(meta.endsAt);
			return from && to
				? `Scheduled maintenance ${title}, ${from} to ${to}`
				: `Scheduled maintenance ${title}`;
		}
		case "maintenance_window.update":
			return changes
				? `Changed ${changes} of maintenance ${title}`
				: `Saved maintenance ${title}`;
		case "maintenance_window.cancel":
			return `Canceled maintenance ${title}`;
		case "maintenance_window.delete":
			return `Deleted maintenance ${title}`;
		default:
			return undefined;
	}
}

/** How a service health incident reads in the audit log. */
const INCIDENT_PHRASE: Record<string, string> = {
	crashed: "crashed",
	unhealthy: "became unhealthy",
	restart_loop: "is restart-looping",
};

/** The reason an operation read external secrets, as the access log words it. */
const PURPOSE: Record<string, string> = {
	deploy: "deploy",
	task: "task",
	"log-mask": "log masking",
	database: "database access",
	backup: "backup",
};

/** "web.API_KEY" or just "API_KEY" when no service is named. */
function variableNames(meta: NonNullable<Metadata>): string {
	const service = text(meta.service);
	const keys = list(meta.keys).map((key) =>
		service ? `${service}.${key}` : key,
	);
	return joinNames(keys);
}

/** Compact value for the fallback line: scalars as-is, collections as a count. */
function brief(value: unknown): string {
	if (Array.isArray(value)) return `${value.length}`;
	if (typeof value === "object" && value !== null) {
		return `${Object.keys(value).length} fields`;
	}
	return String(value);
}

/** The fallback: "key value" pairs, collections collapsed to a count. */
function metadataFallback(metadata: Metadata): string {
	return Object.entries(metadata ?? {})
		.map(([key, value]) => `${key} ${brief(value)}`)
		.join(" · ");
}

/** The complete metadata for the title and the copy action. */
export function metadataJson(metadata: Metadata): string {
	return JSON.stringify(metadata ?? {}, null, 2);
}

/** One sentence for the actions with a known shape, or `undefined`. */
export function describeAudit(
	action: string,
	metadata: Metadata,
): string | undefined {
	const meta = metadata ?? {};
	const name = text(meta.name);
	const slug = text(meta.slug);
	switch (action) {
		case "variable.set":
			return variableNames(meta) ? `Set ${variableNames(meta)}` : undefined;
		case "variable.delete":
			return variableNames(meta) ? `Deleted ${variableNames(meta)}` : undefined;
		case "variable.restore":
			return variableNames(meta)
				? `Restored ${variableNames(meta)}`
				: undefined;
		case "variable.share":
			return variableNames(meta) ? `Shared ${variableNames(meta)}` : undefined;
		case "domain.add":
		case "domain.remove":
		case "domain.update": {
			const host = text(meta.hostname);
			if (!host) return undefined;
			const verb =
				action === "domain.add"
					? "Added"
					: action === "domain.remove"
						? "Removed"
						: "Updated";
			return `${verb} domain ${host}`;
		}
		case "project.create":
		case "environment.create":
		case "service.create":
		case "route.create":
		case "database.create": {
			const subject = name ?? slug;
			if (!subject) return undefined;
			const kind = action.split(".")[0] ?? "";
			return `Created ${kind === "database" ? "database" : kind} ${subject}`;
		}
		case "project.update":
		case "environment.update":
		case "project.delete":
		case "environment.delete": {
			const subject = name ?? slug;
			if (!subject) return undefined;
			const [kind, verb] = action.split(".");
			return `${verb === "delete" ? "Deleted" : "Updated"} ${kind} ${subject}`;
		}
		case "project.deleted": {
			const subject = name ?? slug;
			if (!subject) return undefined;
			return `Deleted project ${subject} and everything in it`;
		}
		case "service.update": {
			const fields = list(meta.fields);
			return fields.length > 0 ? `Changed ${joinNames(fields, 4)}` : undefined;
		}
		case "server.update":
		case "server.proxy.update": {
			const changed = list(meta.changed);
			return changed.length > 0
				? `Changed ${joinNames(changed, 4)}`
				: undefined;
		}
		case "server.enroll": {
			const host = text(meta.hostname);
			return host ? `Enrolled ${host}` : undefined;
		}
		case "token.create": {
			if (!name) return undefined;
			const scopes = list(meta.scopes);
			return scopes.length > 0
				? `Created token ${name} (${joinNames(scopes, 2)})`
				: `Created token ${name}`;
		}
		case "token.revoke":
			return name ? `Revoked token ${name}` : undefined;
		case "token.production_guard.disabled":
			return name
				? `Let token ${name} change production without approval`
				: undefined;
		case "approval.requested":
		case "approval.approved":
		case "approval.declined":
		case "approval.expired":
		case "approval.refused":
		case "approval.used": {
			const summary = text(meta.summary);
			if (!summary) return undefined;
			const verb: Record<string, string> = {
				"approval.requested": "Approval requested",
				"approval.approved": "Approved",
				"approval.declined": "Declined",
				"approval.expired": "Approval expired",
				"approval.refused": "Refused",
				"approval.used": "Ran with approval",
			};
			return `${verb[action]}: ${summary}`;
		}
		case "approval.error":
			return "Production approval could not be checked; the change was refused";
		case "link.create":
		case "link.delete": {
			const from = text(meta.from);
			const to = text(meta.to);
			if (!from || !to) return undefined;
			return `${action === "link.create" ? "Linked" : "Unlinked"} ${from} and ${to}`;
		}
		case "invite.create": {
			const email = text(meta.email);
			const role = text(meta.role);
			if (!role) return undefined;
			return email
				? `Invited ${email} as ${role}`
				: `Created an invite link for ${role}`;
		}
		case "member.update": {
			const email = text(meta.email);
			const role = text(meta.role);
			return email && role ? `Set ${email} to ${role}` : undefined;
		}
		case "service.incident_opened":
		case "service.incident_escalated":
		case "service.incident_resolved": {
			const service = text(meta.service) ?? "A service";
			const phrase = INCIDENT_PHRASE[text(meta.kind) ?? ""] ?? "had a problem";
			const detail = text(meta.detail);
			if (action === "service.incident_resolved") {
				return text(meta.resolution) === "stopped"
					? `${service} was stopped; incident closed`
					: `${service} recovered (${phrase})`;
			}
			const lead =
				action === "service.incident_escalated"
					? `${service} got worse: ${phrase}`
					: `${service} ${phrase}`;
			return detail ? `${lead} — ${detail}` : lead;
		}
		case "notifications.alert_settings_update":
			return "Changed the service alert thresholds";
		case "uptime_monitor.update": {
			const service = text(meta.service) ?? "a service";
			if (meta.enabled === false) {
				return `Turned off uptime monitoring of ${service}`;
			}
			const after = meta.failureThreshold;
			const outage =
				typeof after === "number"
					? `; an outage opens after ${after} failed ${after === 1 ? "check" : "checks"}`
					: "";
			const alerts = meta.alerts === false ? ", alerts off" : "";
			return `Changed the uptime monitor of ${service}${outage}${alerts}`;
		}
		case "status_page.create":
		case "status_page.update":
		case "status_page.delete":
		case "status_page.components":
		case "status_incident.create":
		case "status_incident.update":
		case "status_incident.post":
		case "status_incident.delete":
		case "maintenance_window.create":
		case "maintenance_window.update":
		case "maintenance_window.cancel":
		case "maintenance_window.delete":
			return describeStatus(action, meta);
		case "reconcile.limit_reached": {
			const service = text(meta.service);
			return service
				? `Stopped redeploying ${service} after repeated failures`
				: undefined;
		}
		case "secret.fetch":
		case "secret.fetch.denied": {
			const service = text(meta.service) ?? "a service";
			const count = list(meta.references).length;
			const denied = list(meta.denied).length;
			const how = [
				PURPOSE[text(meta.purpose) ?? ""] ?? text(meta.purpose),
				text(meta.startedBy) ? `started by ${text(meta.startedBy)}` : undefined,
			]
				.filter(Boolean)
				.join(", ");
			const tail = how ? ` (${how})` : "";
			if (action === "secret.fetch.denied") {
				return `Refused ${denied} external secret reference${denied === 1 ? "" : "s"} for ${service}: the integration is not available to this project or environment${tail}`;
			}
			return count > 0
				? `Read ${count} external secret${count === 1 ? "" : "s"} for ${service}${tail}`
				: `Could not read external secrets for ${service}${tail}`;
		}
		case "secret.reveal": {
			const key = text(meta.key);
			const service = text(meta.service);
			if (!key) return undefined;
			return service ? `Revealed ${key} of ${service}` : `Revealed ${key}`;
		}
		case "secret_manager.create":
			return name ? `Added secret manager ${name}` : undefined;
		case "secret_manager.update": {
			if (!name) return undefined;
			const changed = list(meta.changed);
			return changed.length > 0
				? `Changed ${joinNames(changed, 4)} of secret manager ${name}`
				: `Saved secret manager ${name}`;
		}
		case "secret_manager.delete":
			return name ? `Removed secret manager ${name}` : undefined;
		case "secret_manager.test": {
			if (!name) return undefined;
			const reference = text(meta.reference);
			return reference
				? `Tested secret manager ${name} by reading ${reference}`
				: `Tested secret manager ${name}`;
		}
		default:
			return undefined;
	}
}

/** The cell's sentence: the known one, else the compact fallback, else a dash. */
export function auditDetails(action: string, metadata: Metadata): string {
	return describeAudit(action, metadata) ?? (metadataFallback(metadata) || "–");
}

/** The past-tense word for the last part of an action id; others read as they are. */
const VERB_WORD: Record<string, string> = {
	create: "created",
	delete: "deleted",
	update: "updated",
	add: "added",
	remove: "removed",
	revoke: "revoked",
	enroll: "enrolled",
	restore: "restored",
	share: "shared",
	post: "posted",
	cancel: "canceled",
	fetch: "read",
	reveal: "revealed",
	test: "tested",
	set: "set",
};

const sentence = (words: string): string =>
	words === "" ? words : words.charAt(0).toUpperCase() + words.slice(1);

/** "status_page.delete" as "Status page deleted": the action id in words. */
export function auditActionLabel(action: string): string {
	const segments = action.split(".").filter(Boolean);
	const last = segments.at(-1);
	if (last === undefined) return action;
	const verb = VERB_WORD[last];
	if (verb !== undefined) segments[segments.length - 1] = verb;
	return sentence(segments.join(" ").replaceAll("_", " "));
}

/** "status_page" as "Status page". */
export function auditResourceLabel(resourceType: string): string {
	return sentence(resourceType.replaceAll("_", " "));
}
