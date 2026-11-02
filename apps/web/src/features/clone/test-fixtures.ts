// Wire-shaped fixtures for the clone and move dialog tests.

import type {
	CloneEnvironmentRef,
	CloneResult,
	DomainView,
	Environment,
	MoveResult,
	Server,
	ServiceView,
} from "#/api/gen/types.gen";
import { type Service, toService } from "#/api/models";
import { make, type Overrides } from "#/test/factories";

export function serviceView(
	overrides: Overrides<ServiceView> = {},
): ServiceView {
	return make("ServiceView", {
		id: "svc-api",
		environmentId: "env-prod",
		projectId: "proj-1",
		name: "api",
		slug: "api",
		kind: "app",
		sourceType: "image",
		config: { healthcheck: { type: "none" } },
		domains: [],
		...overrides,
	});
}

export function service(overrides: Overrides<ServiceView> = {}): Service {
	return toService(serviceView(overrides));
}

export function domainView(
	hostname: string,
	kind: DomainView["kind"] = "serve",
): DomainView {
	return make("DomainView", { id: `dom-${hostname}`, hostname, kind });
}

export function environment(overrides: Partial<Environment> = {}): Environment {
	return {
		id: "env-prod",
		name: "Production",
		slug: "production",
		projectId: "proj-1",
		isProduction: true,
		kind: "standard",
		parentEnvironmentId: null,
		prNumber: null,
		prTitle: "",
		createdAt: "2026-01-01T00:00:00Z",
		...overrides,
	};
}

export function server(overrides: Overrides<Server> = {}): Server {
	return make("Server", {
		id: "srv-1",
		name: "node-1",
		status: "online",
		drainState: "none",
		acceptWorkloads: true,
		...overrides,
	});
}

export function envRef(
	overrides: Partial<CloneEnvironmentRef> = {},
): CloneEnvironmentRef {
	return {
		id: "env-staging",
		name: "Staging",
		slug: "staging",
		projectId: "proj-1",
		isProduction: false,
		...overrides,
	};
}

export function cloneResult(overrides: Partial<CloneResult> = {}): CloneResult {
	return {
		dryRun: true,
		domains: null,
		references: null,
		services: null,
		skipped: null,
		warnings: null,
		links: 0,
		sharedVariables: 0,
		...overrides,
	};
}

export function moveResult(overrides: Partial<MoveResult> = {}): MoveResult {
	return {
		applied: false,
		blocked: false,
		dryRun: true,
		from: envRef({ id: "env-prod", name: "Production", slug: "production" }),
		to: envRef(),
		incoming: null,
		links: null,
		outgoing: null,
		slug: "api",
		slugChanged: false,
		warnings: null,
		...overrides,
	};
}
