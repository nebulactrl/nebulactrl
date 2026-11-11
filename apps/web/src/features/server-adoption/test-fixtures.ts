import type { ApiSchemas } from "#/test/api-schemas.gen";
import { make, type Overrides } from "#/test/factories";

/** The ids of the adoption scene the fixtures describe. */
export const SVC = "11111111-1111-4111-8111-111111111111";
export const DB = "33333333-3333-4333-8333-333333333333";
export const ENV = "22222222-2222-4222-8222-222222222222";

/** A service as the plan returns it: a running image app with two containers. */
export function service(over: Overrides<ApiSchemas["AdoptionService"]> = {}) {
	return make("AdoptionService", {
		id: SVC,
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
		...over,
	});
}

/** An environment as the plan returns it, to be created in a new project. */
export function environment(
	over: Overrides<ApiSchemas["AdoptionEnvironment"]> = {},
	services: ApiSchemas["AdoptionService"][] = [service()],
) {
	return make("AdoptionEnvironment", {
		id: ENV,
		action: "create",
		projectAction: "create",
		projectName: "Adopted from alpha",
		projectSlug: "adopted-from-alpha",
		name: "production",
		slug: "production",
		production: true,
		services,
		...over,
	});
}

/** The plan of server srv-1: one service to adopt in one new environment. */
export function plan(over: Overrides<ApiSchemas["AdoptionPlan"]> = {}) {
	return make("AdoptionPlan", {
		serverId: "srv-1",
		held: true,
		environments: [environment()],
		totals: { adopt: 1, leave: 0, remove: 0, containers: 2 },
		...over,
	});
}

/** A service in the result: the adopted one, or a reattached one with `over`. */
export function adoptedService(
	over: Overrides<ApiSchemas["AdoptedService"]> = {},
) {
	return make("AdoptedService", {
		id: SVC,
		name: "web",
		slug: "web",
		environmentId: ENV,
		envSlug: "production",
		projectId: "p-1",
		projectSlug: "adopted-from-alpha",
		...over,
	});
}

/** The result of applying that plan: the one service adopted into a new project. */
export function applied(over: Overrides<ApiSchemas["AdoptionResult"]> = {}) {
	return make("AdoptionResult", {
		projects: [
			{ id: "p-1", name: "Adopted from alpha", slug: "adopted-from-alpha" },
		],
		services: [adoptedService()],
		...over,
	});
}
