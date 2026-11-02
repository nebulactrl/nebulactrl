import type { Facts, Server } from "#/api/gen/types.gen";

const GiB = 1024 ** 3;

/** A complete `Facts` as a connected agent reports it. */
export function factsWire(over: Partial<Facts> = {}): Facts {
	return {
		arch: "arm64",
		cgroupVersion: "2",
		cpus: 4,
		diskTotal: 160 * GiB,
		dockerVersion: "27.1.1",
		kernel: "6.8.0",
		memTotal: 8 * GiB,
		os: "ubuntu 24.04",
		port443Free: true,
		port80Free: true,
		...over,
	};
}

/**
 * A complete server as `GET /servers` and `GET /servers/{id}` send it: online,
 * accepting workloads, with a heartbeat sample and an idle proxy. Every field the
 * spec requires is present, so a canned reply built from it is valid as it is.
 */
export function serverWire(over: Partial<Server> = {}): Server {
	return {
		id: "srv-1",
		name: "alpha",
		status: "online",
		publicIp: null,
		baseDomain: null,
		acmeEmail: null,
		agentVersion: "1.0.0",
		protocolVersion: 1,
		facts: factsWire(),
		lastSeenAt: "2026-01-01T00:00:00Z",
		createdAt: "2026-01-01T00:00:00Z",
		proxy: {
			status: "ok",
			v2: true,
			generation: 0,
			appliedGeneration: 0,
			tailnet: {
				mode: "off",
				appliedMode: "off",
				hostname: "",
				state: "",
				ips: null,
			},
		},
		latest: {
			cpuPercent: 12.4,
			memUsed: 500,
			memTotal: 1000,
			diskUsed: 40,
			diskTotal: 100,
			sampledAt: "2026-01-01T00:00:00Z",
		},
		serviceCount: 0,
		metricsRetentionDays: 7,
		diskAlertThresholdPercent: 90,
		trafficAnalyticsEnabled: false,
		trafficHourlyRetentionDays: 7,
		trafficDailyRetentionDays: 30,
		diskAlert: false,
		geoEnabled: false,
		geoSource: "dbip",
		geoLevel: "country",
		geoRefreshDays: 7,
		drainState: "none",
		drainMessage: "",
		drainStartedAt: null,
		acceptWorkloads: true,
		buildEnabled: false,
		buildMaxConcurrent: 1,
		buildAllowEmulation: false,
		buildAllowPreviews: false,
		controlPlane: false,
		unrestricted: false,
		composeVersion: "",
		tunnel: {
			enabled: false,
			state: "off",
			active: false,
			connections: 0,
			observedAt: null,
		},
		patches: {
			known: false,
			supported: true,
			updates: 0,
			security: 0,
			rebootRequired: false,
			checkedAt: null,
			busy: "",
		},
		canDecommission: false,
		...over,
	};
}
