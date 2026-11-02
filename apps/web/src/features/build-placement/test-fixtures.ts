// Wire-shaped replies for the placement tests, built with the schema factories
// so each is a complete body of its schema.

import type { Project, RegistryView, Server } from "#/api/gen/types.gen";
import { make, type Overrides } from "#/test/factories";

/** An online build server "build-01" that takes no workloads. */
export function wireServer(over: Overrides<Server> = {}): Server {
	return make("Server", {
		id: "srv-1",
		name: "build-01",
		status: "online",
		buildEnabled: true,
		acceptWorkloads: false,
		createdAt: "2026-01-01T00:00:00Z",
		facts: {
			os: "linux",
			arch: "arm64",
			kernel: "6.1.0",
			cpus: 4,
			memTotal: 8_000_000_000,
			diskTotal: 80_000_000_000,
			dockerVersion: "27.0.0",
			cgroupVersion: "v2",
			port80Free: true,
			port443Free: true,
			capabilities: ["build.push"],
		},
		...over,
	});
}

/** An external registry "GHCR" on ghcr.io that nothing uses yet. */
export function wireRegistry(over: Overrides<RegistryView> = {}): RegistryView {
	return make("RegistryView", {
		id: "reg-1",
		name: "GHCR",
		kind: "external",
		host: "ghcr.io",
		namespace: "acme",
		hasPushAuth: true,
		hasPullAuth: false,
		manageRetention: false,
		lastCheck: {},
		imageCount: 0,
		sizeBytes: 0,
		inUse: false,
		insecure: false,
		references: { projects: 0, services: 0, teams: 0 },
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		...over,
	});
}

/** The project "Shop" that inherits its build location, with no pin and no registry. */
export function wireProject(over: Overrides<Project> = {}): Project {
	return make("Project", {
		id: "proj-1",
		name: "Shop",
		slug: "shop",
		description: "",
		system: false,
		buildLocation: "inherit",
		buildServerId: "",
		registryId: "",
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		...over,
	});
}

/** The signed-in principal; the registry list is only fetched for an admin. */
export function wireMe(scopes: string[]) {
	return make("MeBody", {
		id: "user-1",
		instanceAdmin: scopes.includes("admin"),
		workspaceId: "ws-1",
		principal: { scopes },
	});
}
