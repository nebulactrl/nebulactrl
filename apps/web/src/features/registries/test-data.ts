// Wire-shape builders for the registries tests: a RegistryView as the
// control plane answers it, and the servers the "Test registry" select offers.

import type {
	RegistryImageView,
	RegistryView,
	Server,
	RegistryCheck as WireCheck,
} from "#/api/gen/types.gen";
import { make, type Overrides } from "#/test/factories";
import type { Registry } from "./model";

export function wireRegistry(
	patch: Overrides<RegistryView> = {},
): RegistryView {
	return make("RegistryView", {
		id: "reg-1",
		name: "GitHub registry",
		kind: "external",
		host: "ghcr.io",
		namespace: "acme",
		hasPushAuth: true,
		pushUsername: "deploy-bot",
		hasPullAuth: false,
		manageRetention: false,
		lastCheck: {},
		imageCount: 0,
		sizeBytes: 0,
		inUse: false,
		insecure: false,
		references: { projects: 0, services: 0, teams: 0 },
		createdAt: "2026-10-01T10:00:00Z",
		updatedAt: "2026-10-01T10:00:00Z",
		...patch,
	});
}

export function wireBuiltin(patch: Overrides<RegistryView> = {}): RegistryView {
	return wireRegistry({
		id: "reg-builtin",
		name: "Built-in registry",
		kind: "builtin",
		host: "",
		namespace: "",
		hasPushAuth: false,
		builtin: {
			status: "ready",
			statusMessage: "",
			gcState: "idle",
			lastSweepDeleted: 0,
		},
		...patch,
	});
}

export function wireCheck(patch: Overrides<WireCheck> = {}): WireCheck {
	return make("RegistryCheck", {
		ok: true,
		push: true,
		pull: true,
		at: "2026-10-30T10:00:00Z",
		...patch,
	});
}

export function wireImage(
	patch: Overrides<RegistryImageView> = {},
): RegistryImageView {
	return make("RegistryImageView", {
		id: "img-1",
		repository: "ghcr.io/acme/shop-api",
		digest: `sha256:${"0a1b2c3d4e5f".padEnd(64, "6")}`,
		platform: "linux/amd64",
		pushedAt: "2026-10-30T09:00:00Z",
		reference: "ghcr.io/acme/shop-api@sha256:0a1b2c3d4e5f",
		state: "live",
		...patch,
	});
}

/** A server row as GET /servers answers: online, no build role, unless the patch says so. */
export function wireServer(
	patch: { id: string; name: string } & Overrides<Server>,
): Server {
	return make("Server", {
		status: "online",
		buildEnabled: false,
		createdAt: "2026-09-01T00:00:00Z",
		...patch,
	});
}

/** A registry in the shape the components read. */
export function registry(patch: Partial<Registry> = {}): Registry {
	return {
		id: "reg-1",
		name: "GitHub registry",
		kind: "external",
		host: "ghcr.io",
		namespace: "acme",
		hasPushAuth: true,
		pushUsername: "deploy-bot",
		hasPullAuth: false,
		pullUsername: "",
		manageRetention: false,
		lastCheck: {},
		imageCount: 0,
		sizeBytes: 0,
		builtin: null,
		inUse: false,
		createdAt: "2026-10-01T10:00:00Z",
		...patch,
	};
}
