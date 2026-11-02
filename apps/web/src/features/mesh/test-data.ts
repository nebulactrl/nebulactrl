// Wire-shaped fixtures for the mesh tests.

import type {
	MeshLinkView,
	NodeView,
	RuleView,
	View,
} from "#/api/gen/types.gen";
import { make } from "#/test/factories";

export function node(overrides: Partial<NodeView> = {}): NodeView {
	return {
		agentCapable: true,
		devices: 1,
		exposures: [],
		fqdn: "web-01.tail1234.ts.net.",
		hostname: "nebula-web-01",
		ips: ["100.64.0.1"],
		lastSeenAt: null,
		message: "",
		needsReprovision: false,
		peers: [],
		serverId: "srv-web",
		serverName: "web-01",
		state: "online",
		tunDevice: true,
		...overrides,
	};
}

export function link(overrides: Partial<MeshLinkView> = {}): MeshLinkView {
	return {
		environmentId: "env-1",
		environmentName: "production",
		grants: [],
		hostname: "nebula-link-1",
		id: "link-1",
		ips: ["100.64.0.7"],
		message: "",
		needsReprovision: false,
		projectName: "shop",
		purpose: "service",
		remoteServerId: "srv-db",
		remoteServerName: "db-01",
		serverId: "srv-web",
		serverName: "web-02",
		state: "online",
		...overrides,
	};
}

export function rule(overrides: Partial<RuleView> = {}): RuleView {
	return {
		because: "postgres is referenced by api",
		dst: "100.64.0.7:15432",
		linkId: "link-1",
		port: 15432,
		src: "100.64.0.9",
		...overrides,
	};
}

export function meshView(overrides: Partial<View> = {}): View {
	return {
		aclMessage: "",
		aclMode: "advisory",
		aclState: "advisory",
		autoJoin: false,
		coordinator: { checkedAt: null, ok: true },
		devices: { used: 3 },
		enabled: true,
		links: [],
		loginServer: "",
		managedPushAvailable: true,
		meshTag: "tag:nebula-mesh",
		nodes: [],
		paused: false,
		pausedReason: "",
		provider: "tailscale",
		rules: [],
		warnings: [],
		...overrides,
	};
}

/** GET /me with the given scopes. */
export function meBody(scopes: string[]) {
	return make("MeBody", { principal: { scopes } });
}
