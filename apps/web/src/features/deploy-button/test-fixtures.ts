// Fixtures for the deploy button tests. Wire-shaped views carry only what the
// mappers and the UI read; the casts document that.

import { mock } from "bun:test";
import type { ServiceView } from "#/api/gen/types.gen";
import { type Service, toService } from "#/api/models";
import type { ReviewGroup } from "#/features/staged/staged";
import type { EnvironmentDeploy } from "./use-environment-deploy";

/** A service as the API returns it; `staged` is the number of settings changes waiting. */
export function serviceView(
	overrides: Partial<ServiceView> & { staged?: number } = {},
): ServiceView {
	const { staged = 0, ...rest } = overrides;
	return {
		id: "svc-api",
		environmentId: "env-1",
		projectId: "proj-1",
		name: "api",
		slug: "api",
		kind: "app",
		sourceType: "image",
		config: { healthcheck: { type: "none" } },
		scaling: {},
		domains: [],
		hasStagedChanges: staged > 0,
		pendingChangeCounts: { variables: 0, settings: staged },
		currentDeploymentId: "dep-1",
		...rest,
	} as unknown as ServiceView;
}

export function service(
	overrides: Partial<ServiceView> & { staged?: number } = {},
): Service {
	return toService(serviceView(overrides));
}

export function group(
	serviceId: string,
	name: string,
	lines: ReviewGroup["lines"],
): ReviewGroup {
	return { serviceId, name, lines };
}

/** An `EnvironmentDeploy` whose actions are recorded and succeed; override what the case cares about. */
export function fakeDeploy(
	overrides: Partial<EnvironmentDeploy> = {},
): EnvironmentDeploy {
	return {
		services: [],
		stagedCountOf: () => 0,
		groups: [],
		count: 0,
		emptyStaged: [],
		inFlight: false,
		sent: false,
		busy: false,
		discarding: undefined,
		deployStaged: mock(async () => true),
		redeployService: mock(async () => true),
		discardChange: mock(() => {}),
		discardAll: mock(async () => {}),
		redeployCurrent: mock(async () => {}),
		...overrides,
	};
}
