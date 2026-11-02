// Wire-shaped fixtures for the platform tests: complete bodies built with the
// schema factories, so a scenario's own values are the only thing a test states.

import type {
	PlatformKindView,
	PlatformListing,
	PlatformServiceView,
	Server,
} from "#/api/gen/types.gen";
import { make, type Overrides } from "#/test/factories";

export function platformService(
	overrides: Overrides<PlatformServiceView> = {},
): PlatformServiceView {
	return make("PlatformServiceView", {
		id: "ps-1",
		kind: "headscale",
		label: "Headscale",
		status: "ready",
		step: "done",
		message: "",
		hostname: "",
		publicUrl: "",
		details: {},
		services: null,
		warnings: null,
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		...overrides,
	});
}

export function platformKind(
	overrides: Overrides<PlatformKindView> = {},
): PlatformKindView {
	return make("PlatformKindView", {
		kind: "headscale",
		label: "Headscale",
		product: "headscale/headscale",
		license: "BSD-3-Clause",
		agpl: false,
		egress: true,
		holdsData: true,
		image: "headscale/headscale:0.26",
		footprint: "About 100 MB of memory",
		summary: "The control server of your private network.",
		...overrides,
	});
}

export function platformListing(
	instance?: PlatformServiceView,
	kind: Overrides<PlatformKindView> = {},
	listing: Overrides<PlatformListing> = {},
): PlatformListing {
	return make("PlatformListing", {
		kinds: [platformKind({ ...(instance ? { instance } : {}), ...kind })],
		...listing,
	});
}

/** An online server "hetzner-01" with a public address and a base domain. */
export function serverView(overrides: Overrides<Server> = {}): Server {
	return make("Server", {
		id: "srv-1",
		name: "hetzner-01",
		status: "online",
		publicIp: "203.0.113.7",
		baseDomain: "example.com",
		drainState: "none",
		acceptWorkloads: true,
		proxy: { status: "ready" },
		...overrides,
	});
}
