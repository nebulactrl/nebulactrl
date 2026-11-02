import { describe, expect, test } from "bun:test";
import type { Server as ServerWire } from "#/api/gen/types.gen";
import {
	diskPercent,
	isOnline,
	SERVER_SETTING_LIMITS,
	toServerView,
} from "./model";

const GiB = 1024 ** 3;

function wireServer(overrides: Partial<ServerWire> = {}): ServerWire {
	return {
		acceptWorkloads: true,
		acmeEmail: "ops@example.com",
		agentVersion: "v0.4.2",
		baseDomain: "apps.example.com",
		buildAllowEmulation: false,
		buildAllowPreviews: false,
		buildEnabled: false,
		buildMaxConcurrent: 2,
		canDecommission: true,
		composeVersion: "2.29",
		controlPlane: false,
		createdAt: "2026-01-01T00:00:00Z",
		diskAlert: true,
		diskAlertThresholdPercent: 85,
		drainMessage: "",
		drainStartedAt: null,
		drainState: "none",
		facts: {
			arch: "arm64",
			cgroupVersion: "2",
			cpus: 4,
			diskTotal: 100 * GiB,
			dockerVersion: "27.3",
			kernel: "6.8",
			memTotal: 16 * GiB,
			os: "Ubuntu 24.04",
			port443Free: true,
			port80Free: true,
		},
		geoEnabled: true,
		geoLevel: "city",
		geoRefreshDays: 14,
		geoSource: "maxmind",
		id: "srv-1",
		lastSeenAt: "2026-01-02T00:00:00Z",
		latest: {
			cpuPercent: 10,
			diskTotal: 100 * GiB,
			diskUsed: 90 * GiB,
			memTotal: 16 * GiB,
			memUsed: 4 * GiB,
			sampledAt: "2026-01-02T00:00:00Z",
		},
		metricsRetentionDays: 30,
		name: "hetzner-01",
		patches: {
			busy: "check",
			checkedAt: null,
			known: true,
			rebootRequired: true,
			security: 2,
			supported: true,
			updates: 5,
		},
		protocolVersion: 1,
		proxy: {
			appliedGeneration: 1,
			generation: 2,
			status: "error",
			error: "port 443 in use",
			tailnet: {
				appliedMode: "off",
				hostname: "",
				ips: null,
				mode: "off",
				state: "",
			},
			v2: true,
		},
		publicIp: "203.0.113.10",
		serviceCount: 2,
		status: "online",
		trafficAnalyticsEnabled: true,
		trafficDailyRetentionDays: 90,
		trafficHourlyRetentionDays: 10,
		tunnel: {
			active: false,
			connections: 0,
			enabled: false,
			observedAt: null,
			state: "off",
		},
		unrestricted: true,
		...overrides,
	};
}

describe("diskPercent", () => {
	test("is used over total from the latest heartbeat", () => {
		expect(
			diskPercent({
				latest: { diskUsed: 60 * GiB, diskTotal: 120 * GiB },
			} as Parameters<typeof diskPercent>[0]),
		).toBe(50);
	});

	test("is null before a first heartbeat", () => {
		expect(diskPercent({ latest: null })).toBeNull();
	});

	test("is null, not a division by zero, when the heartbeat reports no disk", () => {
		expect(
			diskPercent({
				latest: { diskUsed: 5, diskTotal: 0 },
			} as Parameters<typeof diskPercent>[0]),
		).toBeNull();
		expect(
			diskPercent({
				latest: { diskUsed: 5, diskTotal: -1 },
			} as Parameters<typeof diskPercent>[0]),
		).toBeNull();
	});
});

describe("isOnline", () => {
	test("only an online server is seen by the control plane", () => {
		expect(isOnline({ status: "online" })).toBe(true);
		expect(isOnline({ status: "offline" })).toBe(false);
		expect(isOnline({ status: "pending" })).toBe(false);
		expect(isOnline({ status: "revoked" })).toBe(false);
	});
});

describe("toServerView", () => {
	test("gathers the per-server settings into one object", () => {
		expect(toServerView(wireServer()).settings).toEqual({
			metricsRetentionDays: 30,
			diskAlertThresholdPercent: 85,
			trafficAnalyticsEnabled: true,
			trafficHourlyRetentionDays: 10,
			trafficDailyRetentionDays: 90,
			geoEnabled: true,
			geoSource: "maxmind",
			geoLevel: "city",
			geoRefreshDays: 14,
		});
	});

	test("carries the alert, patch summary, decommission flag, removal and geo status", () => {
		const removal = { state: "removing", startedAt: "2026-01-03T00:00:00Z" };
		const geoStatus = { state: "ready" };
		const view = toServerView(
			wireServer({
				removal,
				geoStatus,
			} as unknown as Partial<ServerWire>),
		);
		expect(view.diskAlert).toBe(true);
		expect(view.patches).toMatchObject({
			busy: "check",
			rebootRequired: true,
			security: 2,
			updates: 5,
		});
		expect(view.canDecommission).toBe(true);
		expect(view.removal).toEqual(removal as never);
		expect(view.geoStatus).toEqual(geoStatus as never);
	});

	test("keeps the base server fields the cards read", () => {
		const view = toServerView(wireServer());
		expect(view).toMatchObject({
			id: "srv-1",
			name: "hetzner-01",
			status: "online",
			publicIp: "203.0.113.10",
			baseDomain: "apps.example.com",
			acmeEmail: "ops@example.com",
			agentVersion: "v0.4.2",
			serviceCount: 2,
			unrestricted: true,
			composeVersion: "2.29",
		});
		expect(view.facts?.os).toBe("Ubuntu 24.04");
		expect(view.latest?.cpuPercent).toBe(10);
		expect(view.proxy).toMatchObject({
			status: "error",
			error: "port 443 in use",
		});
	});

	test("a server that never connected has no facts and no heartbeat", () => {
		const view = toServerView(
			wireServer({
				status: "pending",
				agentVersion: null,
				facts: undefined as unknown as ServerWire["facts"],
				latest: undefined as unknown as ServerWire["latest"],
			}),
		);
		expect(view.facts).toBeNull();
		expect(view.latest).toBeNull();
		expect(view.agentVersion).toBeNull();
		expect(view.removal).toBeUndefined();
		expect(view.geoStatus).toBeUndefined();
	});
});

describe("SERVER_SETTING_LIMITS", () => {
	test("match the PATCH /servers/{id} bounds", () => {
		expect(SERVER_SETTING_LIMITS).toEqual({
			metricsRetentionDays: { min: 1, max: 90 },
			diskAlertThresholdPercent: { min: 50, max: 99 },
			trafficHourlyRetentionDays: { min: 2, max: 90 },
			trafficDailyRetentionDays: { min: 7, max: 365 },
			geoRefreshDays: { min: 1, max: 90 },
		});
	});
});
