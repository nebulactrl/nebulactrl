import { describe, expect, test } from "bun:test";
import {
	auditActionLabel,
	auditDetails,
	auditResourceLabel,
	describeAudit,
	metadataJson,
} from "./audit-describe";

describe("describeAudit", () => {
	test("deleting a project with everything in it names the project", () => {
		expect(
			describeAudit("project.deleted", { name: "Acme Shop", services: 5 }),
		).toBe("Deleted project Acme Shop and everything in it");
	});

	test("a variable change names service.key", () => {
		expect(
			describeAudit("variable.set", {
				service: "web",
				keys: ["API_KEY"],
				changes: [{ key: "API_KEY", fingerprintTo: "abc" }],
				affected: [],
			}),
		).toBe("Set web.API_KEY");
	});

	test("many keys collapse to a count", () => {
		expect(
			describeAudit("variable.delete", {
				service: "web",
				keys: ["A", "B", "C", "D", "E"],
			}),
		).toBe("Deleted web.A, web.B, web.C and 2 more");
	});

	test("a domain sentence", () => {
		expect(describeAudit("domain.add", { hostname: "shop.localhost" })).toBe(
			"Added domain shop.localhost",
		);
	});

	test("an invite with and without an email", () => {
		expect(
			describeAudit("invite.create", { email: "a@b.c", role: "viewer" }),
		).toBe("Invited a@b.c as viewer");
		expect(describeAudit("invite.create", { role: "admin" })).toBe(
			"Created an invite link for admin",
		);
	});
});

describe("auditDetails", () => {
	test("an unknown action falls back to short key/value pairs", () => {
		expect(
			auditDetails("custom.thing", { serverId: "11880bdb", changes: [1, 2] }),
		).toBe("serverId 11880bdb · changes 2");
	});

	test("no metadata is a dash", () => {
		expect(auditDetails("custom.thing", {})).toBe("–");
	});
});

describe("secret access", () => {
	test("a deploy fetch names the service, the count and who started it", () => {
		expect(
			describeAudit("secret.fetch", {
				service: "api",
				purpose: "deploy",
				startedBy: "alice@example.com",
				references: ["doppler:dp#A", "doppler:dp#B"],
				count: 2,
			}),
		).toBe(
			"Read 2 external secrets for api (deploy, started by alice@example.com)",
		);
		expect(
			describeAudit("secret.fetch", {
				service: "api",
				purpose: "log-mask",
				references: ["vault:bao/a#k"],
			}),
		).toBe("Read 1 external secret for api (log masking)");
		expect(
			describeAudit("secret.fetch", {
				service: "api",
				purpose: "deploy",
				failed: [{ reference: "vault:bao/a#k", error: "permission denied" }],
			}),
		).toBe("Could not read external secrets for api (deploy)");
	});

	test("a refused fetch says why", () => {
		expect(
			describeAudit("secret.fetch.denied", {
				service: "api",
				denied: ["doppler:dp#A"],
			}),
		).toBe(
			"Refused 1 external secret reference for api: the integration is not available to this project or environment",
		);
	});

	test("reveals and secret manager changes", () => {
		expect(
			describeAudit("secret.reveal", { service: "pg", key: "DATABASE_URL" }),
		).toBe("Revealed DATABASE_URL of pg");
		expect(describeAudit("secret_manager.create", { name: "dp" })).toBe(
			"Added secret manager dp",
		);
		expect(
			describeAudit("secret_manager.update", {
				name: "dp",
				changed: ["scope"],
			}),
		).toBe("Changed scope of secret manager dp");
		expect(describeAudit("secret_manager.delete", { name: "dp" })).toBe(
			"Removed secret manager dp",
		);
		expect(
			describeAudit("secret_manager.test", {
				name: "dp",
				reference: "doppler:dp#A",
			}),
		).toBe("Tested secret manager dp by reading doppler:dp#A");
	});

	test("service health incidents read as sentences", () => {
		expect(
			describeAudit("service.incident_opened", {
				service: "api",
				kind: "crashed",
				detail: "The container exited and has been down for 30s.",
			}),
		).toBe("api crashed — The container exited and has been down for 30s.");
		expect(
			describeAudit("service.incident_escalated", {
				service: "api",
				kind: "restart_loop",
			}),
		).toBe("api got worse: is restart-looping");
		expect(
			describeAudit("service.incident_resolved", {
				service: "api",
				kind: "crashed",
				resolution: "recovered",
			}),
		).toBe("api recovered (crashed)");
		expect(
			describeAudit("service.incident_resolved", {
				service: "api",
				kind: "unhealthy",
				resolution: "stopped",
			}),
		).toBe("api was stopped; incident closed");
	});

	test("uptime monitor changes read as sentences", () => {
		expect(
			describeAudit("uptime_monitor.update", {
				service: "api",
				enabled: true,
				alerts: true,
				failureThreshold: 3,
				recoveryThreshold: 2,
				reminderMinutes: 0,
			}),
		).toBe(
			"Changed the uptime monitor of api; an outage opens after 3 failed checks",
		);
		expect(
			describeAudit("uptime_monitor.update", {
				service: "api",
				enabled: true,
				alerts: false,
				failureThreshold: 1,
			}),
		).toBe(
			"Changed the uptime monitor of api; an outage opens after 1 failed check, alerts off",
		);
		expect(
			describeAudit("uptime_monitor.update", {
				service: "api",
				enabled: false,
			}),
		).toBe("Turned off uptime monitoring of api");
	});

	test("status pages read as sentences", () => {
		expect(
			describeAudit("status_page.create", {
				title: "Acme status",
				slug: "acme",
				hostname: "status.acme.dev",
			}),
		).toBe("Created status page Acme status on status.acme.dev");
		expect(
			describeAudit("status_page.create", {
				title: "Acme status",
				hostname: null,
			}),
		).toBe("Created status page Acme status");
		expect(
			describeAudit("status_page.update", {
				title: "Acme status",
				hostname: "status.acme.dev",
				published: true,
				changed: ["published"],
			}),
		).toBe("Published status page Acme status on status.acme.dev");
		expect(
			describeAudit("status_page.update", {
				title: "Acme status",
				published: false,
				changed: ["published"],
			}),
		).toBe("Unpublished status page Acme status");
		expect(
			describeAudit("status_page.update", {
				title: "Acme status",
				changed: ["title", "footer"],
			}),
		).toBe("Changed title, footer of status page Acme status");
		expect(
			describeAudit("status_page.components", {
				title: "Acme status",
				components: 1,
			}),
		).toBe("Set 1 component of status page Acme status");
		expect(
			describeAudit("status_page.components", {
				title: "Acme status",
				components: 4,
			}),
		).toBe("Set 4 components of status page Acme status");
		expect(describeAudit("status_page.delete", { title: "Acme status" })).toBe(
			"Deleted status page Acme status",
		);
		expect(describeAudit("status_page.delete", {})).toBeUndefined();
	});

	test("status incidents read as sentences", () => {
		expect(
			describeAudit("status_incident.create", {
				title: "API errors",
				impact: "major",
				status: "investigating",
			}),
		).toBe("Opened incident API errors (major impact, investigating)");
		expect(
			describeAudit("status_incident.update", {
				title: "API errors",
				changed: ["impact", "pages"],
			}),
		).toBe("Changed impact, pages of incident API errors");
		expect(
			describeAudit("status_incident.post", {
				title: "API errors",
				status: "monitoring",
				previousStatus: "identified",
			}),
		).toBe("Posted an update to incident API errors: identified to monitoring");
		expect(
			describeAudit("status_incident.post", {
				title: "API errors",
				status: "resolved",
				previousStatus: "monitoring",
			}),
		).toBe("Resolved incident API errors");
		expect(
			describeAudit("status_incident.post", {
				title: "API errors",
				status: "identified",
				previousStatus: "identified",
			}),
		).toBe("Posted an update to incident API errors (identified)");
		expect(
			describeAudit("status_incident.delete", { title: "API errors" }),
		).toBe("Deleted incident API errors");
	});

	test("maintenance windows read as sentences", () => {
		expect(
			describeAudit("maintenance_window.create", {
				title: "Database upgrade",
				startsAt: "2026-11-02T01:00:00Z",
				endsAt: "2026-11-02T02:30:00Z",
			}),
		).toBe(
			"Scheduled maintenance Database upgrade, 2026-11-02 01:00 UTC to 2026-11-02 02:30 UTC",
		);
		expect(
			describeAudit("maintenance_window.update", {
				title: "Database upgrade",
				changed: ["startsAt", "endsAt"],
			}),
		).toBe("Changed startsAt, endsAt of maintenance Database upgrade");
		expect(
			describeAudit("maintenance_window.cancel", { title: "Database upgrade" }),
		).toBe("Canceled maintenance Database upgrade");
		expect(
			describeAudit("maintenance_window.delete", { title: "Database upgrade" }),
		).toBe("Deleted maintenance Database upgrade");
	});
});

describe("auditActionLabel", () => {
	test("reads an action id as words", () => {
		expect(auditActionLabel("status_page.delete")).toBe("Status page deleted");
		expect(auditActionLabel("token.create")).toBe("Token created");
		expect(auditActionLabel("server.proxy.update")).toBe(
			"Server proxy updated",
		);
		expect(auditActionLabel("secret.fetch.denied")).toBe("Secret fetch denied");
	});

	test("leaves no dot or underscore for a verb it does not know", () => {
		expect(auditActionLabel("notifications.alert_settings_update")).toBe(
			"Notifications alert settings update",
		);
	});
});

describe("auditResourceLabel", () => {
	test("sentence case, spaces for underscores", () => {
		expect(auditResourceLabel("status_page")).toBe("Status page");
		expect(auditResourceLabel("token")).toBe("Token");
	});
});

describe("resource and access sentences", () => {
	test("variable verbs name service.key, and say nothing without keys", () => {
		const meta = { service: "web", keys: ["A"] };
		expect(describeAudit("variable.restore", meta)).toBe("Restored web.A");
		expect(describeAudit("variable.share", meta)).toBe("Shared web.A");
		expect(describeAudit("variable.delete", { keys: ["A", "B"] })).toBe(
			"Deleted A, B",
		);
		for (const action of [
			"variable.set",
			"variable.delete",
			"variable.restore",
			"variable.share",
		]) {
			expect(
				describeAudit(action, { service: "web", keys: [] }),
			).toBeUndefined();
		}
	});

	test("domain verbs differ, and a missing hostname says nothing", () => {
		expect(describeAudit("domain.remove", { hostname: "a.dev" })).toBe(
			"Removed domain a.dev",
		);
		expect(describeAudit("domain.update", { hostname: "a.dev" })).toBe(
			"Updated domain a.dev",
		);
		expect(describeAudit("domain.add", {})).toBeUndefined();
	});

	test("creating a thing names it by name, else slug", () => {
		expect(describeAudit("project.create", { name: "Shop" })).toBe(
			"Created project Shop",
		);
		expect(describeAudit("environment.create", { slug: "staging" })).toBe(
			"Created environment staging",
		);
		expect(describeAudit("service.create", { name: "web" })).toBe(
			"Created service web",
		);
		expect(describeAudit("route.create", { name: "api", slug: "x" })).toBe(
			"Created route api",
		);
		expect(describeAudit("database.create", { name: "pg" })).toBe(
			"Created database pg",
		);
		expect(describeAudit("service.create", {})).toBeUndefined();
	});

	test("updating or deleting a project or environment", () => {
		expect(describeAudit("project.update", { name: "Shop" })).toBe(
			"Updated project Shop",
		);
		expect(describeAudit("environment.update", { slug: "prod" })).toBe(
			"Updated environment prod",
		);
		expect(describeAudit("project.delete", { name: "Shop" })).toBe(
			"Deleted project Shop",
		);
		expect(describeAudit("environment.delete", { name: "qa" })).toBe(
			"Deleted environment qa",
		);
		expect(describeAudit("project.update", {})).toBeUndefined();
		expect(describeAudit("project.deleted", {})).toBeUndefined();
	});

	test("service and server changes list the fields, at most four", () => {
		expect(describeAudit("service.update", { fields: ["image", "port"] })).toBe(
			"Changed image, port",
		);
		expect(
			describeAudit("service.update", {
				fields: ["a", "b", "c", "d", "e", "f"],
			}),
		).toBe("Changed a, b, c, d and 2 more");
		expect(describeAudit("service.update", { fields: [] })).toBeUndefined();
		expect(describeAudit("server.update", { changed: ["name"] })).toBe(
			"Changed name",
		);
		expect(describeAudit("server.proxy.update", { changed: ["acme"] })).toBe(
			"Changed acme",
		);
		expect(describeAudit("server.update", {})).toBeUndefined();
		expect(describeAudit("server.enroll", { hostname: "h1" })).toBe(
			"Enrolled h1",
		);
		expect(describeAudit("server.enroll", {})).toBeUndefined();
	});

	test("tokens: created with scopes, without, revoked, production guard off", () => {
		expect(
			describeAudit("token.create", {
				name: "ci",
				scopes: ["projects:read", "logs:read", "admin"],
			}),
		).toBe("Created token ci (projects:read, logs:read and 1 more)");
		expect(describeAudit("token.create", { name: "ci", scopes: [] })).toBe(
			"Created token ci",
		);
		expect(describeAudit("token.create", {})).toBeUndefined();
		expect(describeAudit("token.revoke", { name: "ci" })).toBe(
			"Revoked token ci",
		);
		expect(describeAudit("token.revoke", {})).toBeUndefined();
		expect(
			describeAudit("token.production_guard.disabled", { name: "ci" }),
		).toBe("Let token ci change production without approval");
		expect(
			describeAudit("token.production_guard.disabled", {}),
		).toBeUndefined();
	});

	test("approvals read as 'verb: summary'", () => {
		const summary = { summary: "restart web in production" };
		const expected: Record<string, string> = {
			"approval.requested": "Approval requested",
			"approval.approved": "Approved",
			"approval.declined": "Declined",
			"approval.expired": "Approval expired",
			"approval.refused": "Refused",
			"approval.used": "Ran with approval",
		};
		for (const [action, lead] of Object.entries(expected)) {
			expect(describeAudit(action, summary)).toBe(
				`${lead}: restart web in production`,
			);
			expect(describeAudit(action, {})).toBeUndefined();
		}
		expect(describeAudit("approval.error", {})).toBe(
			"Production approval could not be checked; the change was refused",
		);
	});

	test("links, member roles and invites without a role", () => {
		expect(describeAudit("link.create", { from: "web", to: "db" })).toBe(
			"Linked web and db",
		);
		expect(describeAudit("link.delete", { from: "web", to: "db" })).toBe(
			"Unlinked web and db",
		);
		expect(describeAudit("link.create", { from: "web" })).toBeUndefined();
		expect(
			describeAudit("member.update", { email: "a@b.c", role: "admin" }),
		).toBe("Set a@b.c to admin");
		expect(describeAudit("member.update", { email: "a@b.c" })).toBeUndefined();
		expect(describeAudit("invite.create", { email: "a@b.c" })).toBeUndefined();
	});

	test("alert thresholds and the reconcile limit", () => {
		expect(describeAudit("notifications.alert_settings_update", {})).toBe(
			"Changed the service alert thresholds",
		);
		expect(describeAudit("reconcile.limit_reached", { service: "web" })).toBe(
			"Stopped redeploying web after repeated failures",
		);
		expect(describeAudit("reconcile.limit_reached", {})).toBeUndefined();
	});

	test("secret manager actions without a name say nothing", () => {
		expect(describeAudit("secret_manager.create", {})).toBeUndefined();
		expect(describeAudit("secret_manager.update", {})).toBeUndefined();
		expect(describeAudit("secret_manager.delete", {})).toBeUndefined();
		expect(describeAudit("secret_manager.test", {})).toBeUndefined();
		expect(describeAudit("secret.reveal", { service: "web" })).toBeUndefined();
	});

	test("status sentences need a title, else they fall back to key/value", () => {
		expect(describeAudit("status_page.create", {})).toBeUndefined();
		expect(auditDetails("status_page.delete", { slug: "x" })).toBe("slug x");
	});

	test("unknown actions with null metadata are a dash", () => {
		expect(describeAudit("nothing.here", null)).toBeUndefined();
		expect(auditDetails("nothing.here", undefined)).toBe("–");
	});

	test("a fallback line collapses objects to a field count", () => {
		expect(
			auditDetails("nothing.here", { config: { a: 1, b: 2 }, ok: true }),
		).toBe("config 2 fields · ok true");
	});

	test("metadataJson prints the whole map, and {} for none", () => {
		expect(metadataJson({ a: 1 })).toBe('{\n  "a": 1\n}');
		expect(metadataJson(null)).toBe("{}");
		expect(metadataJson(undefined)).toBe("{}");
	});
});
