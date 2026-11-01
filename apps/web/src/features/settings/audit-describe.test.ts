import { describe, expect, test } from "bun:test";
import {
	auditActionLabel,
	auditDetails,
	auditResourceLabel,
	describeAudit,
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
