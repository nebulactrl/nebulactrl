import type {
	ServerPatchSummary,
	Server as ServerWire,
} from "#/api/gen/types.gen";
import { type Server, toServer } from "#/api/models";
import { formatBytes } from "#/lib/format";

/** Per-server settings the control plane keeps beside the enrolment facts. */
export interface ServerSettings {
	metricsRetentionDays: number;
	diskAlertThresholdPercent: number;
	trafficAnalyticsEnabled: boolean;
	trafficHourlyRetentionDays: number;
	trafficDailyRetentionDays: number;
	/** Count where requests come from (country, and city at the city level). */
	geoEnabled: boolean;
	geoSource: "dbip" | "maxmind";
	geoLevel: "country" | "city";
	geoRefreshDays: number;
}

/** The PATCH /servers/{id} bounds (OpenAPI minimum/maximum). */
export const SERVER_SETTING_LIMITS = {
	metricsRetentionDays: { min: 1, max: 90 },
	diskAlertThresholdPercent: { min: 50, max: 99 },
	trafficHourlyRetentionDays: { min: 2, max: 90 },
	trafficDailyRetentionDays: { min: 7, max: 365 },
	geoRefreshDays: { min: 1, max: 90 },
} as const;

export interface ServerView extends Server {
	settings: ServerSettings;
	/** Root-filesystem usage is at or above the alert threshold. */
	diskAlert: boolean;
	/** Pending OS updates and reboot-required, from the agent's latest check. */
	patches: ServerPatchSummary;
	/** The agent can empty its host and remove itself (the clean removal). */
	canDecommission: boolean;
	/** Set while the server is being removed or its removal failed. */
	removal?: ServerWire["removal"];
	/** The GeoIP database as the agent last reported it; absent while it is offline or geolocation is off. */
	geoStatus?: ServerWire["geoStatus"];
}

/** Disk use in percent from the latest heartbeat; null before the first. */
export function diskPercent(server: Pick<Server, "latest">): number | null {
	const latest = server.latest;
	if (!latest || latest.diskTotal <= 0) return null;
	return (latest.diskUsed / latest.diskTotal) * 100;
}

/**
 * The size of the disk the agent watches, in bytes: the newest heartbeat's,
 * so the facts strip, the charts and the lists quote one figure. The facts
 * the agent sent when it connected are the fallback before a first
 * heartbeat. Null when neither has one.
 */
export function diskTotalBytes(
	server: Pick<Server, "latest" | "facts">,
): number | null {
	const sampled = server.latest?.diskTotal ?? 0;
	if (sampled > 0) return sampled;
	const reported = server.facts?.diskTotal ?? 0;
	return reported > 0 ? reported : null;
}

/**
 * "2.8 / 15.7 GiB": used against total, the unit written once when both
 * share it, so the pair stays short enough to sit on one line.
 */
export function usageText(used: number, total: number): string {
	const usedText = formatBytes(used);
	const totalText = formatBytes(total);
	const [usedValue, usedUnit] = usedText.split(" ");
	const [, totalUnit] = totalText.split(" ");
	return usedUnit === totalUnit
		? `${usedValue} / ${totalText}`
		: `${usedText} / ${totalText}`;
}

export function toServerView(wire: ServerWire): ServerView {
	return {
		...toServer(wire),
		settings: {
			metricsRetentionDays: wire.metricsRetentionDays,
			diskAlertThresholdPercent: wire.diskAlertThresholdPercent,
			trafficAnalyticsEnabled: wire.trafficAnalyticsEnabled,
			trafficHourlyRetentionDays: wire.trafficHourlyRetentionDays,
			trafficDailyRetentionDays: wire.trafficDailyRetentionDays,
			geoEnabled: wire.geoEnabled,
			geoSource: wire.geoSource,
			geoLevel: wire.geoLevel,
			geoRefreshDays: wire.geoRefreshDays,
		},
		diskAlert: wire.diskAlert,
		patches: wire.patches,
		canDecommission: wire.canDecommission,
		removal: wire.removal,
		geoStatus: wire.geoStatus,
	};
}

/** Whether the control plane currently sees the agent. */
export function isOnline(server: Pick<Server, "status">): boolean {
	return server.status === "online";
}

/** The geolocation refresh interval in days, or null unless the text is a whole number in range. */
export function parseRefreshDays(text: string): number | null {
	const { min, max } = SERVER_SETTING_LIMITS.geoRefreshDays;
	const value = Number(text);
	return text.trim() !== "" &&
		Number.isInteger(value) &&
		value >= min &&
		value <= max
		? value
		: null;
}
