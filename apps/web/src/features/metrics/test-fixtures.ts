// Test-only builders for the metrics tests: API responses as the control plane sends them.
import type {
	MetricPoint,
	MetricsOutputBody,
	Server,
	ServerMetricsOutputBody,
	ServiceMetricPoint,
} from "#/api/gen/types.gen";
import { make } from "#/test/factories";

export const MB = 1024 * 1024;

/** An ISO timestamp `secondsAgo` before now. */
export const isoAgo = (secondsAgo: number): string =>
	new Date(Date.now() - secondsAgo * 1000).toISOString();

export function servicePoint(
	over: Partial<ServiceMetricPoint> = {},
): ServiceMetricPoint {
	return {
		ts: isoAgo(10),
		cpuPercent: 40,
		cpuPerReplica: 40,
		cpuMax: 55,
		memBytes: 256 * MB,
		memLimitBytes: 512 * MB,
		memMax: 300 * MB,
		replicaCount: 1,
		...over,
	};
}

export function serviceMetrics(
	points: ServiceMetricPoint[] | null,
	over: Partial<MetricsOutputBody> = {},
): MetricsOutputBody {
	return { points, retentionDays: 7, stepSeconds: 10, ...over };
}

export function serverPoint(over: Partial<MetricPoint> = {}): MetricPoint {
	return {
		ts: isoAgo(10),
		cpuPercent: 30,
		cpuMax: 45,
		memBytes: 2048 * MB,
		memMax: 3072 * MB,
		memPercent: 25,
		memTotalBytes: 8192 * MB,
		diskPercent: 62,
		diskUsedBytes: 62 * 1024 * MB,
		diskTotalBytes: 100 * 1024 * MB,
		...over,
	};
}

export function serverMetrics(
	points: MetricPoint[] | null,
	over: Partial<ServerMetricsOutputBody> = {},
): ServerMetricsOutputBody {
	return { points, retentionDays: 7, stepSeconds: 10, ...over };
}

/** A server as GET /servers/{id} returns it; the metrics view reads its disk alert threshold. */
export function serverBody(diskAlertThresholdPercent = 80): Server {
	return make("Server", {
		id: "srv_1",
		name: "hetzner-01",
		status: "online",
		createdAt: "2025-01-01T00:00:00Z",
		serviceCount: 0,
		diskAlertThresholdPercent,
	});
}
