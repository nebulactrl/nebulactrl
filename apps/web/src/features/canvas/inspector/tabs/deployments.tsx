import { DeploymentsTab as DeploymentsPanel } from "#/features/deployments/deployments-tab";
import type { ServiceTabProps } from "./types";

/** The Deployments tab: history, step timeline, logs, redeploy and rollback. */
export function DeploymentsTab({ service }: ServiceTabProps) {
	return <DeploymentsPanel service={service} />;
}
