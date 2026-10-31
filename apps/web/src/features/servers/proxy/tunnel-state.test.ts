import { describe, expect, test } from "bun:test";
import type { TunnelView } from "#/api/gen/types.gen";
import {
	connectionsText,
	enableBlocker,
	tunnelBusy,
	tunnelChip,
	tunnelNote,
} from "./tunnel-state";

const view = (over: Partial<TunnelView> = {}): TunnelView => ({
	accountReady: true,
	active: false,
	agentSupported: true,
	cloudflareConnected: true,
	connections: 0,
	enabled: false,
	ingressHosts: 0,
	ingressSyncedAt: null,
	message: "",
	observedAt: null,
	state: "off",
	upstream: "http://nebulactrl-traefik:80",
	...over,
});

describe("tunnel chip", () => {
	test("every state has a word and a status icon", () => {
		expect(tunnelChip("online")).toEqual({
			kind: "live",
			label: "Connected",
		});
		expect(tunnelChip("provisioning").kind).toBe("deploying");
		expect(tunnelChip("removing").kind).toBe("deploying");
		expect(tunnelChip("needs_attention").kind).toBe("failed");
		expect(tunnelChip("off").kind).toBe("stopped");
		expect(tunnelChip("offline").label).toBe("Offline");
		expect(tunnelChip("removing").label).toBe("Removing");
		expect(tunnelChip("off").label).toBe("Off");
	});
});

describe("connections text", () => {
	test("pluralises and explains zero", () => {
		expect(connectionsText(0)).toBe("No connection to Cloudflare yet");
		expect(connectionsText(1)).toBe("1 connection to Cloudflare");
		expect(connectionsText(4)).toBe("4 connections to Cloudflare");
	});
});

describe("enable blocker", () => {
	test("nothing blocks a connected team with a current agent", () => {
		expect(enableBlocker(view())).toBeNull();
	});
	test("names the first missing prerequisite", () => {
		expect(enableBlocker(view({ cloudflareConnected: false }))).toContain(
			"not connected",
		);
		expect(enableBlocker(view({ accountReady: false }))).toContain("account");
		expect(enableBlocker(view({ agentSupported: false }))).toContain("agent");
		// The connection comes first: without it the account is moot.
		expect(
			enableBlocker(view({ cloudflareConnected: false, accountReady: false })),
		).toContain("not connected");
	});
});

describe("busy and notes", () => {
	test("polls while connecting or removing", () => {
		expect(tunnelBusy(undefined)).toBe(false);
		expect(tunnelBusy(view({ state: "provisioning" }))).toBe(true);
		expect(tunnelBusy(view({ state: "removing" }))).toBe(true);
		expect(tunnelBusy(view({ state: "online" }))).toBe(false);
	});
	test("an active tunnel says the ports may stay closed", () => {
		expect(tunnelNote(view({ state: "online", active: true }))).toContain(
			"Ports 80 and 443 may stay closed",
		);
		expect(tunnelNote(view({ state: "online", active: false }))).not.toContain(
			"stay closed",
		);
		expect(tunnelNote(view())).toBeNull();
	});
});
