// Test-only builders for the log drains tests: API-shaped drains and the
// project → environment → service tree the scope picker walks, built with the
// schema factories.

import type {
	Environment,
	LogDrainServerStatus,
	LogDrainView,
	Project,
	ServiceView,
} from "#/api/gen/types.gen";
import { make, type Overrides } from "#/test/factories";

/** A drain; a patch key set to `undefined` removes that field (an absent kind config). */
export function drainView(patch: Overrides<LogDrainView> = {}): LogDrainView {
	return make("LogDrainView", {
		id: "drain-1",
		name: "Grafana",
		kind: "loki",
		builtin: false,
		enabled: true,
		allProjects: true,
		projectIds: [],
		serviceIds: [],
		query: "",
		bufferMb: 256,
		headerNames: [],
		secrets: {},
		servers: [],
		test: { message: "" },
		createdAt: "2026-10-01T00:00:00Z",
		updatedAt: "2026-10-01T00:00:00Z",
		loki: { url: "https://logs.grafana.net/loki/api/v1/push" },
		...patch,
	});
}

export function serverStatus(
	patch: Overrides<LogDrainServerStatus> = {},
): LogDrainServerStatus {
	return make("LogDrainServerStatus", {
		serverId: "srv-1",
		serverName: "fra-1",
		state: "ok",
		sentLines: 1204,
		droppedLines: 0,
		backlogBytes: 0,
		lastError: "",
		reportedAt: "2026-11-01T10:00:00Z",
		...patch,
	});
}

export function project(id: string, name: string): Project {
	return make("Project", {
		id,
		name,
		slug: name.toLowerCase(),
		description: "",
		buildLocation: "inherit",
		buildServerId: "",
		registryId: "",
		system: false,
		createdAt: "2026-10-01T00:00:00Z",
		updatedAt: "2026-10-01T00:00:00Z",
	});
}

export function environment(
	id: string,
	projectId: string,
	name: string,
): Environment {
	return make("Environment", {
		id,
		projectId,
		name,
		slug: name.toLowerCase(),
		isProduction: false,
		kind: "standard",
		parentEnvironmentId: null,
		prNumber: null,
		prTitle: "",
		createdAt: "2026-10-01T00:00:00Z",
	});
}

/** A service as the API sends it; the drains UI reads only its ids, name and where it lives. */
export function serviceView(
	id: string,
	name: string,
	projectId: string,
	environmentId: string,
): ServiceView {
	return make("ServiceView", {
		id,
		name,
		slug: name.toLowerCase(),
		projectId,
		environmentId,
		kind: "app",
		sourceType: "image",
	});
}

/** Two projects with one environment each: web/prod has `api` and `web`, ops/staging has `cron`. */
export const TEAM_ROUTES = {
	"GET /projects": {
		body: make("PageProject", {
			items: [project("p-web", "Web"), project("p-ops", "Ops")],
		}),
	},
	"GET /projects/p-web/environments": {
		body: make("PageEnvironment", {
			items: [environment("e-web", "p-web", "Production")],
		}),
	},
	"GET /projects/p-ops/environments": {
		body: make("PageEnvironment", {
			items: [environment("e-ops", "p-ops", "Staging")],
		}),
	},
	"GET /environments/e-web/services": {
		body: make("PageServiceView", {
			items: [
				serviceView("svc-web", "web", "p-web", "e-web"),
				serviceView("svc-api", "api", "p-web", "e-web"),
			],
		}),
	},
	"GET /environments/e-ops/services": {
		body: make("PageServiceView", {
			items: [serviceView("svc-cron", "cron", "p-ops", "e-ops")],
		}),
	},
};
