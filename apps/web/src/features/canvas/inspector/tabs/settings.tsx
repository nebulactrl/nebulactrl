import { SettingsTab as SettingsPanel } from "#/features/service-settings/settings-tab";
import type { ServiceTabProps } from "./types";

/**
 * The Settings tab: General, Deploy, Networking, Resources, Storage,
 * Operations, Advanced and Danger zone, one category at a time (`?section=`).
 */
export function SettingsTab({
	service,
	section,
	onSectionChange,
}: ServiceTabProps) {
	return (
		<SettingsPanel
			service={service}
			section={section}
			onSectionChange={onSectionChange}
		/>
	);
}
