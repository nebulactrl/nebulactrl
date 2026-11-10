import type {
	AdoptionEnvironment,
	AdoptionPlan,
	AdoptionResult,
	AdoptionService,
} from "#/api/gen/types.gen";

/** A service as the plan returns it; `over` replaces fields. */
export function service(over: Partial<AdoptionService> = {}): AdoptionService {
	return {
		id: "11111111-1111-4111-8111-111111111111",
		slug: "web",
		name: "web",
		kind: "app",
		sourceType: "image",
		image: "nginx:1.27",
		action: "adopt",
		adoptable: true,
		replicas: 2,
		containers: [
			{ name: "nc-web-aaaaaaaa-1", state: "running" },
			{ name: "nc-web-aaaaaaaa-2", state: "running" },
		],
		domains: null,
		env: null,
		notes: null,
		processes: null,
		volumes: null,
		...over,
	};
}

/** An environment as the plan returns it; `over` replaces fields. */
export function environment(
	over: Partial<AdoptionEnvironment> = {},
	services: AdoptionService[] = [service()],
): AdoptionEnvironment {
	return {
		id: "22222222-2222-4222-8222-222222222222",
		action: "create",
		projectAction: "create",
		projectName: "Adopted from alpha",
		projectSlug: "adopted-from-alpha",
		name: "production",
		slug: "production",
		production: true,
		services,
		...over,
	};
}

export function plan(over: Partial<AdoptionPlan> = {}): AdoptionPlan {
	return {
		serverId: "srv-1",
		held: true,
		environments: [environment()],
		warnings: null,
		blockers: null,
		totals: { adopt: 1, leave: 0, remove: 0, containers: 2 },
		...over,
	};
}

export function applied(over: Partial<AdoptionResult> = {}): AdoptionResult {
	return {
		left: 0,
		removed: 0,
		reattached: 0,
		warnings: null,
		projects: [
			{ id: "p-1", name: "Adopted from alpha", slug: "adopted-from-alpha" },
		],
		services: [
			{
				id: "11111111-1111-4111-8111-111111111111",
				name: "web",
				slug: "web",
				environmentId: "22222222-2222-4222-8222-222222222222",
				envSlug: "production",
				projectId: "p-1",
				projectSlug: "adopted-from-alpha",
			},
		],
		...over,
	};
}
