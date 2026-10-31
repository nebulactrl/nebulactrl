import { keepPreviousData, useQuery } from "@tanstack/react-query";
import {
	getServerAnalyticsOptions,
	getServiceAnalyticsOptions,
	getTrafficAnalyticsOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { refetchIntervalMs, type TrafficRange } from "../metrics/ranges";

export type TrafficScope =
	| { serviceId: string }
	| { serverId: string }
	| { environmentId: string };

export interface TrafficParams {
	range: TrafficRange;
	/** Ask for the previous period's totals (`compare=prev`). */
	compare: boolean;
	/** Narrow a service to one hostname. */
	host: string | null;
	/** Top paths count HTML documents only (the API default). */
	pagesOnly: boolean;
}

/**
 * The traffic report for a scope: a service, a server (every host, plus
 * unrouted ones) or an environment. 5 s refetch while the range is live
 * (≤ 1 h), 15 s beyond; the previous report stays on screen while a new
 * range loads, so switching ranges never flashes a skeleton.
 *
 * The three endpoints return one shape, so this is one hook: only the
 * matching query is enabled and its result is returned.
 */
export function useTrafficReport(
	scope: TrafficScope,
	params: TrafficParams,
	enabled = true,
) {
	const common = {
		refetchInterval: refetchIntervalMs("traffic", params.range),
		placeholderData: keepPreviousData,
	};
	const query = {
		range: params.range,
		...(params.compare ? { compare: "prev" as const } : {}),
		pages: params.pagesOnly,
	};
	const service = useQuery({
		...getServiceAnalyticsOptions({
			path: { serviceId: "serviceId" in scope ? scope.serviceId : "" },
			query: { ...query, ...(params.host ? { host: params.host } : {}) },
		}),
		...common,
		enabled: enabled && "serviceId" in scope,
	});
	const server = useQuery({
		...getServerAnalyticsOptions({
			path: { serverId: "serverId" in scope ? scope.serverId : "" },
			query,
		}),
		...common,
		enabled: enabled && "serverId" in scope,
	});
	const environment = useQuery({
		...getTrafficAnalyticsOptions({
			query: {
				...query,
				...("environmentId" in scope
					? { environmentId: scope.environmentId }
					: {}),
			},
		}),
		...common,
		enabled: enabled && "environmentId" in scope,
	});
	if ("serviceId" in scope) return service;
	if ("serverId" in scope) return server;
	return environment;
}
