import type {
	ConfigPlan,
	ConfigPlanChange,
	ConfigPlanService,
	ConnectionView,
	Environment,
	EnvironmentConfigSource,
} from "#/api/gen/types.gen";
import { make, type Overrides } from "#/test/factories";

/** A staging environment of project p1; override what a test is about. */
export function environment(
	overrides: Overrides<Environment> = {},
): Environment {
	return make("Environment", {
		createdAt: "2026-01-01T00:00:00Z",
		id: "env_1",
		isProduction: false,
		kind: "standard",
		name: "Staging",
		parentEnvironmentId: null,
		prNumber: null,
		prTitle: "",
		projectId: "p1",
		slug: "staging",
		...overrides,
	});
}

export function change(
	overrides: Overrides<ConfigPlanChange> = {},
): ConfigPlanChange {
	return make("ConfigPlanChange", {
		path: "services.web.port",
		line: 14,
		field: "config.port",
		op: "set",
		applies: "staged",
		reason: "file_changed",
		from: "3000",
		to: "8080",
		masked: false,
		...overrides,
	});
}

/** What `plan` takes: a plan's members, its services as the members they differ in. */
type PlanOverrides = Omit<Overrides<ConfigPlan>, "services"> & {
	services?: Overrides<ConfigPlanService>[];
};

/** A valid plan with one updated service; override pieces per test. */
export function plan({
	services = [
		{
			slug: "web",
			kind: "git",
			action: "update",
			managed: true,
			changes: [change()],
		},
	],
	...overrides
}: PlanOverrides = {}): ConfigPlan {
	return make("ConfigPlan", {
		planId: "pl_7d3c1f0a9b2e4c55e6a1d8f0b3c47a92",
		valid: true,
		source: {
			kind: "git",
			repo: "acme/shop",
			branch: "main",
			path: "nebula.toml",
			commit: "3f9c2a1deadbeef",
			bytes: 2048,
		},
		summary: {
			create: 0,
			update: 1,
			unchanged: 2,
			notInFile: 0,
			staged: 1,
			immediate: 0,
			deferred: 0,
			errors: 0,
			warnings: 0,
		},
		services: services.map((service) => make("ConfigPlanService", service)),
		guard: { productionApproval: "not_required" },
		...overrides,
	});
}

/** The environment's config-source answer for a bound, in-sync file. */
export function envSource(
	overrides: Overrides<EnvironmentConfigSource> = {},
): EnvironmentConfigSource {
	return make("EnvironmentConfigSource", {
		autoApply: false,
		branch: "main",
		checkedAt: "2026-01-02T00:00:00Z",
		commit: "3f9c2a1deadbeef",
		connectionId: "conn_1",
		fileSha256: "",
		mode: "explicit",
		path: "",
		repo: "acme/shop",
		resolvedPath: "nebula.toml",
		services: [],
		status: "in_sync",
		statusDetail: "",
		...overrides,
	});
}

/** One git connection as the connections list returns it. */
export function connection(
	overrides: Overrides<ConnectionView> = {},
): ConnectionView {
	return make("ConnectionView", {
		id: "conn_1",
		name: "Acme GitHub",
		provider: "github",
		baseUrl: "",
		...overrides,
	});
}

/** The connections list reply; `[connection()]` unless the test names its own. */
export function connectionsReply(items: ConnectionView[] = [connection()]) {
	return { body: make("ListConnectionsOutputBody", { items }) };
}
