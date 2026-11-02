// Wire-shaped fixtures for the link flow's component tests. Only the fields
// the components and `toService` read are filled in.

import type {
	LinkPreview,
	LinkView,
	ProposedVariable,
	ServiceView,
} from "#/api/gen/types.gen";
import { type Service, toService } from "#/api/models";

export function serviceView(
	id: string,
	overrides: Partial<ServiceView> = {},
): ServiceView {
	return {
		id,
		environmentId: "env-1",
		projectId: "proj-1",
		name: id,
		slug: id,
		kind: "app",
		sourceType: "image",
		config: { healthcheck: { type: "none" } },
		scaling: {},
		domains: [],
		...overrides,
	} as unknown as ServiceView;
}

export function service(
	id: string,
	overrides: Partial<ServiceView> = {},
): Service {
	return toService(serviceView(id, overrides));
}

export function proposal(
	key: string,
	overrides: Partial<ProposedVariable> = {},
): ProposedVariable {
	return {
		key,
		value: `\${{postgres.${key}}}`,
		recommended: true,
		secret: false,
		status: "new",
		...overrides,
	};
}

export function preview(overrides: Partial<LinkPreview> = {}): LinkPreview {
	return {
		existingKeys: null,
		linked: false,
		mesh: { blockers: null, crossServer: false, devicesAdded: 0, links: null },
		proposedVariables: [],
		warnings: null,
		...overrides,
	};
}

export function linkView(
	id: string,
	from: string,
	to: string,
	overrides: Partial<LinkView> = {},
): LinkView {
	return {
		id,
		environmentId: "env-1",
		fromServiceId: from,
		toServiceId: to,
		references: null,
		source: "manual",
		staged: false,
		createdAt: "2026-01-01T00:00:00Z",
		...overrides,
	};
}
