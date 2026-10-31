import { useQuery } from "@tanstack/react-query";
import { ErrorState } from "#/components/common/error-state";
import { Skeleton } from "#/components/ui/skeleton";
import { DefaultQuotaPlate } from "./default-quota-plate";
import { servicesLoadFailure } from "./model";
import { instanceServicesQuery } from "./queries";
import { ServicesPlate } from "./services-plate";
import { TeamsPlate } from "./teams-plate";

/**
 * Admin → Built-in services: the backup storage, image registry and central
 * logs that run beside the control plane and serve every team. Teams cannot
 * create, move or delete them; each gets a slice, and this page is where the
 * instance admin sees that they work and sets the quotas. Instance admin only.
 */
export function InstanceServicesSection() {
	const query = useQuery(instanceServicesQuery());
	const view = query.data;

	if (query.isPending) return <Skeleton className="h-72 rounded-card" />;
	if (!view) {
		return (
			<ErrorState
				frame="raised"
				title="Built-in services did not load"
				body={servicesLoadFailure(query.error)}
				retry={() => void query.refetch()}
			/>
		);
	}

	return (
		<div className="flex flex-col gap-5">
			<ServicesPlate view={view} />
			<DefaultQuotaPlate defaultQuotaBytes={view.defaultQuotaBytes} />
			<TeamsPlate view={view} />
		</div>
	);
}
