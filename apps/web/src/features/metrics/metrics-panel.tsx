import { ServerMetrics } from "./server-metrics";
import { ServiceMetrics } from "./service-metrics";

type MetricsScope = { serviceId: string } | { serverId: string };

export interface MetricsPanelProps {
	scope: MetricsScope;
}

/**
 * Resource charts for one service (CPU %, memory in MiB with its limit line,
 * deployment markers) or one server (CPU, memory and disk as a percent of
 * the host). Range selector with live refresh up to 10 minutes; every
 * tooltip prints local and UTC time.
 */
export function MetricsPanel({ scope }: MetricsPanelProps) {
	return "serviceId" in scope ? (
		<ServiceMetrics serviceId={scope.serviceId} />
	) : (
		<ServerMetrics serverId={scope.serverId} />
	);
}
