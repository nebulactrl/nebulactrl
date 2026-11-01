import { useQuery } from "@tanstack/react-query";
import { serviceQuery } from "#/api/hooks";
import { Alert, AlertDescription } from "#/components/ui/alert";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Skeleton } from "#/components/ui/skeleton";
import { MaintenanceForm } from "./maintenance-form";
import { closeMaintenance, useMaintenanceTarget } from "./store";

/**
 * Mounted once by the canvas screen: shows the maintenance dialog for the
 * service `openMaintenance` asked for.
 */
export function MaintenanceHost() {
	const serviceId = useMaintenanceTarget();
	const service = useQuery({
		...serviceQuery(serviceId ?? ""),
		enabled: serviceId !== null,
	});
	return (
		<Dialog
			open={serviceId !== null}
			onOpenChange={(open) => {
				if (!open) closeMaintenance();
			}}
		>
			<DialogContent size="md" showCloseButton>
				<DialogHeader>
					<DialogTitle>
						{service.data
							? `Maintenance mode for ${service.data.name}`
							: "Maintenance mode"}
					</DialogTitle>
					<DialogDescription>
						Visitors see a maintenance page (HTTP 503) on every domain of the
						service. Applies immediately; nothing is deployed.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					{service.data ? (
						<MaintenanceForm
							primary
							service={service.data}
							onApplied={closeMaintenance}
						/>
					) : service.isError ? (
						<Alert variant="destructive">
							<AlertDescription>Could not load this service.</AlertDescription>
						</Alert>
					) : (
						<Skeleton className="h-40 w-full" />
					)}
				</DialogBody>
			</DialogContent>
		</Dialog>
	);
}
