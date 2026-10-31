import { TrafficPanel } from "#/features/analytics";
import { useInspectorActions } from "../inspector-context";
import type { ServiceTabProps } from "./types";

/** The Analytics tab: request traffic through the proxy for this service's domains. */
export function AnalyticsTab({ service }: ServiceTabProps) {
	const { openTab } = useInspectorActions();
	return (
		<section
			data-tab="analytics"
			data-service-id={service.id}
			aria-label="Analytics"
			className="min-h-full p-5"
		>
			<TrafficPanel
				scope={{ serviceId: service.id }}
				onAddDomain={() => openTab("settings")}
			/>
		</section>
	);
}
