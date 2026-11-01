import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import {
	getEnvironmentUptimeOptions,
	getServiceUptimeOptions,
	getUptimeMonitorOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import type { UptimeRange } from "./model";

/**
 * The agent probes every 30 s and the control plane rolls the checks up per
 * minute, so a view that shows recent checks asks again at that pace. A state
 * change (`uptime.state.changed`) refreshes it at once through the live feed;
 * TanStack pauses the timer in a hidden tab.
 */
const UPTIME_REFETCH_MS = 30_000;

/** A service's uptime over `range`. Switching range keeps the old strip until the new one lands. */
export const serviceUptimeQuery = (serviceId: string, range: UptimeRange) =>
	queryOptions({
		...getServiceUptimeOptions({ path: { serviceId }, query: { range } }),
		refetchInterval: UPTIME_REFETCH_MS,
		placeholderData: keepPreviousData,
	});

/** Every service of an environment with its state and 30 daily points. */
export const environmentUptimeQuery = (environmentId: string) =>
	queryOptions({
		...getEnvironmentUptimeOptions({ path: { environmentId } }),
		refetchInterval: UPTIME_REFETCH_MS,
	});

/** The monitor settings of a service (the saved copy the form starts from). */
export const uptimeMonitorQuery = (serviceId: string) =>
	getUptimeMonitorOptions({ path: { serviceId } });

/** Everything a monitor change touches: the settings, the card and the environment list. */
export const MONITOR_OPERATIONS = [
	"getUptimeMonitor",
	"getServiceUptime",
	"getEnvironmentUptime",
] as const;
