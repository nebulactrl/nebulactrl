import type { Service } from "#/api/models";
import { StatusIcon } from "#/components/ui/status-icon";
import { Section, SectionBody } from "#/features/service-settings/fields";
import { MaintenanceForm } from "./maintenance-form";
import { maintenanceSummary, maintenanceSupported } from "./model";

/**
 * Settings → Maintenance. Like Scaling it is not part of the settings draft:
 * it is operational, so it acts on the proxy immediately and nothing is staged.
 */
export function MaintenanceSection({ service }: { service: Service }) {
	if (!maintenanceSupported(service)) return null;
	const on = service.maintenance.enabled;
	return (
		<Section
			title="Maintenance"
			description="Show a maintenance page (HTTP 503 with Retry-After) on every domain of this service. Takes effect immediately, without a deploy, and survives deployments."
			aside={
				<span className="inline-flex items-center gap-1.5">
					{on ? <StatusIcon kind="warning" label="" /> : null}
					<span>{maintenanceSummary(service.maintenance)}</span>
				</span>
			}
		>
			<SectionBody>
				<MaintenanceForm service={service} />
			</SectionBody>
		</Section>
	);
}
