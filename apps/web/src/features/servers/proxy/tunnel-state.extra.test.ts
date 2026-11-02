import { describe, expect, test } from "bun:test";
import type { TunnelView } from "#/api/gen/types.gen";
import { hostnamesText, tunnelChip, tunnelNote } from "./tunnel-state";

const view = (over: Partial<TunnelView> = {}): TunnelView => ({
	accountReady: true,
	active: false,
	agentSupported: true,
	cloudflareConnected: true,
	connections: 0,
	enabled: true,
	ingressHosts: 0,
	ingressSyncedAt: null,
	message: "",
	observedAt: null,
	state: "off",
	upstream: "http://nebulactrl-traefik:80",
	...over,
});

describe("tunnel notes per state", () => {
	test("a connected tunnel that is not active yet says domains switch soon", () => {
		expect(tunnelNote(view({ state: "online", active: false }))).toBe(
			"Connected. Domains switch to the tunnel in a moment.",
		);
	});

	test("provisioning keeps the current DNS until the connector connects", () => {
		expect(tunnelNote(view({ state: "provisioning" }))).toContain(
			"Domains keep their current DNS",
		);
	});

	test("offline warns that domains on the tunnel are unreachable", () => {
		expect(tunnelNote(view({ state: "offline" }))).toContain("unreachable");
	});

	test("needs attention tells the operator to re-sync", () => {
		expect(tunnelNote(view({ state: "needs_attention" }))).toContain(
			"Re-sync to try again",
		);
	});

	test("removing says domains are pointed back at the server", () => {
		expect(tunnelNote(view({ state: "removing" }))).toContain(
			"pointed back at the server",
		);
	});

	test("an unknown state has no note and falls back to the off chip", () => {
		const unknown = "mystery" as TunnelView["state"];
		expect(tunnelNote(view({ state: unknown }))).toBeNull();
		expect(tunnelChip(unknown)).toEqual({ kind: "stopped", label: "Off" });
	});
});

describe("hostnames text", () => {
	test("pluralises the count", () => {
		expect(hostnamesText(0)).toBe("0 hostnames");
		expect(hostnamesText(1)).toBe("1 hostname");
		expect(hostnamesText(3)).toBe("3 hostnames");
	});
});
