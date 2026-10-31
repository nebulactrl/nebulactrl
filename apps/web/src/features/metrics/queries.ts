import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import {
	getServerMetricsOptions,
	getServiceMetricsOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { serviceMetricsParams } from "./model";
import type { MetricRange } from "./ranges";
import { refetchIntervalMs } from "./ranges";

/**
 * Service resource samples. 5 s refetch on the live ranges (≤10m), 15 s
 * beyond; the previous range's chart stays on screen while the next loads,
 * so changing the range never flashes a skeleton. With `process` the series
 * is that process' own; without it, the service total.
 */
export const serviceMetricsQuery = (
	serviceId: string,
	range: MetricRange,
	process?: string,
) =>
	queryOptions({
		...getServiceMetricsOptions({
			path: { serviceId },
			query: serviceMetricsParams(range, process),
		}),
		refetchInterval: refetchIntervalMs("metrics", range),
		placeholderData: keepPreviousData,
	});

export const serverMetricsQuery = (serverId: string, range: MetricRange) =>
	queryOptions({
		...getServerMetricsOptions({ path: { serverId }, query: { range } }),
		refetchInterval: refetchIntervalMs("metrics", range),
		placeholderData: keepPreviousData,
	});
