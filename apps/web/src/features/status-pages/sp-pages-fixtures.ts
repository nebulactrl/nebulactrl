// Fixtures for the tests of the status pages screens, dialogs and DNS panels:
// the typed factories (src/test/factories.ts) with the values those screens read.
import type {
	DnsStatus,
	EnvironmentService,
	MaintenanceWindowView,
	StatusIncidentView,
	StatusPageDnsCheck,
	StatusPageView,
	Team,
} from "#/api/gen/types.gen";
import { make, type Overrides } from "#/test/factories";

export function pageView(
	patch: Overrides<StatusPageView> = {},
): StatusPageView {
	return make("StatusPageView", {
		id: "page-1",
		title: "Acme status",
		slug: "acme-status",
		description: "",
		footer: "",
		hostname: "status.acme.test",
		published: true,
		showUptime: true,
		components: [],
		createdAt: "2026-10-01T00:00:00Z",
		updatedAt: "2026-10-01T00:00:00Z",
		...patch,
	});
}

export function incidentView(
	patch: Overrides<StatusIncidentView> = {},
): StatusIncidentView {
	return make("StatusIncidentView", {
		id: "inc-1",
		title: "Elevated API errors",
		impact: "minor",
		status: "investigating",
		pageIds: ["page-1"],
		serviceIds: [],
		resolvedAt: null,
		createdAt: "2026-11-01T09:00:00Z",
		updatedAt: "2026-11-01T09:30:00Z",
		updates: [],
		...patch,
	});
}

export function windowView(
	patch: Overrides<MaintenanceWindowView> = {},
): MaintenanceWindowView {
	return make("MaintenanceWindowView", {
		id: "win-1",
		title: "Database upgrade",
		description: "Read-only for 20 minutes.",
		startsAt: "2030-01-02T01:00:00Z",
		endsAt: "2030-01-02T02:30:00Z",
		phase: "upcoming",
		pageIds: ["page-1"],
		serviceIds: [],
		canceledAt: null,
		createdAt: "2026-11-01T00:00:00Z",
		updatedAt: "2026-11-01T00:00:00Z",
		...patch,
	});
}

export function team(role: Team["role"]): Team {
	return make("Team", {
		id: "team-1",
		name: "Acme",
		slug: "acme",
		role,
		current: true,
		memberCount: 2,
		createdAt: "2026-01-01T00:00:00Z",
	});
}

export function teamRoute(role: Team["role"]) {
	return {
		"GET /teams": {
			body: make("ListTeamsOutputBody", { items: [team(role)] }),
		},
	};
}

export function dnsCheck(
	patch: Overrides<StatusPageDnsCheck> = {},
): StatusPageDnsCheck {
	return make("StatusPageDNSCheck", {
		hostname: "status.acme.test",
		status: "ok",
		message: "",
		proxied: false,
		cloudflareRecord: false,
		nameChangedAt: "2020-01-01T00:00:00Z",
		records: [{ name: "status.acme.test", type: "A", value: "203.0.113.10" }],
		resolved: ["203.0.113.10"],
		...patch,
	});
}

export function cloudflareStatus(patch: Overrides<DnsStatus> = {}): DnsStatus {
	return make("DNSStatus", {
		checkedAt: null,
		configured: true,
		desired: {
			type: "A",
			content: "203.0.113.10",
			proxied: false,
			owned: false,
		},
		desiredRecords: [
			{ type: "A", content: "203.0.113.10", proxied: false, owned: false },
		],
		effectiveProxied: false,
		error: "",
		manage: true,
		message: "",
		needsAdopt: false,
		originLock: {
			aop: "off",
			aopReason: "",
			aopReasonCode: "",
			enabled: false,
			ipListSource: "builtin",
			ipLock: false,
			optIn: true,
			permissionHint: "",
			realClientIp: false,
			reason: "not_proxied",
		},
		proxied: false,
		records: [],
		sslMode: "",
		status: "missing",
		tunnel: false,
		warnings: [],
		zoneId: "zone-1",
		zoneName: "acme.test",
		...patch,
	});
}

// --- The team's services: projects → environments → services ----------------

function project(id: string, name: string) {
	return make("Project", { id, name, slug: name.toLowerCase() });
}

function environment(id: string, projectId: string, name: string) {
	return make("Environment", {
		id,
		projectId,
		name,
		slug: name.toLowerCase(),
	});
}

function serviceView(
	id: string,
	name: string,
	projectId: string,
	environmentId: string,
) {
	return make("ServiceView", {
		id,
		name,
		slug: name.toLowerCase(),
		projectId,
		environmentId,
	});
}

/** Project Web / Production has services api and web; no uptime is monitored. */
export const TEAM_SERVICES_ROUTES = {
	"GET /projects": {
		body: make("PageProject", { items: [project("p-web", "Web")] }),
	},
	"GET /projects/p-web/environments": {
		body: make("PageEnvironment", {
			items: [environment("e-web", "p-web", "Production")],
		}),
	},
	"GET /environments/e-web/services": {
		body: make("PageServiceView", {
			items: [
				serviceView("svc-api", "api", "p-web", "e-web"),
				serviceView("svc-web", "web", "p-web", "e-web"),
			],
		}),
	},
	"GET /environments/e-web/uptime": uptimeReply(),
};

/** One service's uptime row; the id doubles as its name and slug. */
export function uptimeService(
	serviceId: string,
	state: EnvironmentService["state"],
	monitored: boolean,
): EnvironmentService {
	return make("EnvironmentService", {
		serviceId,
		name: serviceId,
		slug: serviceId,
		state,
		monitored,
	});
}

/** A 200 reply listing `items` as an environment's uptime (null is how the API sends none). */
export function uptimeReply(...items: EnvironmentService[] | [null]) {
	return {
		body: make("EnvironmentUptimeOutputBody", {
			items: items[0] === null ? null : (items as EnvironmentService[]),
		}),
	};
}
