import { MetricsPanel } from "#/features/metrics";
import type { ServiceTabProps } from "./types";

/** The Metrics tab: CPU and memory of the service's container. */
export function MetricsTab({ service }: ServiceTabProps) {
	return (
		<section
			data-tab="metrics"
			data-service-id={service.id}
			aria-label="Metrics"
			className="min-h-full p-5"
		>
			<MetricsPanel scope={{ serviceId: service.id }} />
		</section>
	);
}
