import type {
	CoolifyApplyResult,
	CoolifyPlan,
	CoolifyPlanEnv,
	CoolifyPlanProject,
	CoolifyPlanResource,
	Server,
} from "#/api/gen/types.gen";
import { make, type Overrides } from "#/test/factories";

/** A resource as the plan returns it; `over` replaces fields. */
export function resource(
	over: Partial<CoolifyPlanResource> = {},
): CoolifyPlanResource {
	return {
		uuid: "res-web",
		name: "web",
		slug: "web",
		kind: "app",
		include: true,
		dependsOn: null,
		domains: null,
		env: null,
		needsUnrestricted: null,
		notes: null,
		unsupported: null,
		volumes: null,
		source: { type: "image", image: "ghcr.io/acme/web:1.2" },
		...over,
	};
}

export function envRow(over: Partial<CoolifyPlanEnv> = {}): CoolifyPlanEnv {
	return { key: "API_URL", scope: "runtime", secret: false, ...over };
}

export function project(
	over: Partial<CoolifyPlanProject> = {},
	resources: CoolifyPlanResource[] = [resource()],
): CoolifyPlanProject {
	return {
		uuid: "proj-1",
		name: "Shop",
		slug: "shop",
		action: "create",
		collision: false,
		environments: [
			{
				name: "production",
				slug: "production",
				action: "create",
				production: true,
				resources,
			},
		],
		...over,
	};
}

export function plan(over: Partial<CoolifyPlan> = {}): CoolifyPlan {
	return {
		blockers: null,
		checks: null,
		followups: null,
		installable: true,
		projects: [project()],
		registries: null,
		rewrites: null,
		totals: {
			projects: 1,
			services: 1,
			databases: 0,
			variables: 0,
			secrets: 0,
			domains: 0,
		},
		unsupported: null,
		warnings: null,
		...over,
	};
}

export function applied(
	over: Partial<CoolifyApplyResult> = {},
): CoolifyApplyResult {
	return {
		deployments: null,
		followups: null,
		rewrites: null,
		warnings: null,
		projects: [
			{
				id: "p-1",
				name: "Shop",
				slug: "shop",
				created: true,
				environments: [
					{ id: "e-1", name: "production", slug: "production", created: true },
				],
			},
		],
		services: [
			{
				id: "s-1",
				name: "web",
				slug: "web",
				kind: "app",
				coolifyUuid: "res-web",
				envSlug: "production",
				environmentId: "e-1",
				projectId: "p-1",
				projectSlug: "shop",
			},
		],
		...over,
	};
}

/** A server row as the API returns it, with the fields the wizard reads. */
export function serverWire(over: Overrides<Server> = {}) {
	return make("Server", {
		id: "srv-1",
		name: "alpha",
		status: "online",
		unrestricted: false,
		drainState: "none",
		acceptWorkloads: true,
		serviceCount: 0,
		createdAt: "2026-01-01T00:00:00Z",
		proxy: { status: "", v2: false, generation: 0, appliedGeneration: 0 },
		...over,
	});
}
