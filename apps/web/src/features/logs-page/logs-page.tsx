import { cn } from "cn";
import type { Environment } from "#/api/models";
import { ErrorState } from "#/components/common/error-state";
import { EnvironmentPage } from "#/components/shell/page-frame";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { useServices } from "#/features/canvas/data";
import { EXPLORER_HEIGHT_CLASS, LogsExplorer } from "./explorer/explorer";

function LogsBody({ environment }: { environment: Environment }) {
	const services = useServices(environment.id);
	return (
		<SkeletonReveal
			loading={services.isPending}
			skeleton={
				<div role="status" aria-label="Loading logs">
					<Skeleton className={cn("rounded-card", EXPLORER_HEIGHT_CLASS)} />
				</div>
			}
		>
			{() =>
				services.isError && !services.data ? (
					<ErrorState
						frame="raised"
						title="Services did not load"
						body="The control plane did not answer, so there are no logs to show yet. Nothing was changed; try again."
						retry={() => void services.refetch()}
					/>
				) : (
					<LogsExplorer
						environment={environment}
						services={services.data ?? []}
					/>
				)
			}
		</SkeletonReveal>
	);
}

/**
 * The environment's Logs page: its merged log, live or stored on the servers,
 * under one search box, filling the room below the page header.
 */
export function LogsPage({
	projectId,
	envSlug,
}: {
	projectId: string;
	envSlug: string;
}) {
	return (
		<EnvironmentPage
			projectId={projectId}
			envSlug={envSlug}
			page="logs"
			title="Logs"
			width="full"
			description={() =>
				"Search runtime output and requests across this environment."
			}
		>
			{(environment) => <LogsBody environment={environment} />}
		</EnvironmentPage>
	);
}
