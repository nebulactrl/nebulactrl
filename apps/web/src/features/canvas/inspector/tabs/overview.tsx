import { useQuery } from "@tanstack/react-query";
import { useDeferredValue } from "react";
import { deploymentQuery, pendingChangesQuery } from "#/api/hooks";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Skeleton } from "#/components/ui/skeleton";
import { DatabaseSection } from "#/features/databases/database-section";
import { isManagedDatabase } from "#/features/databases/queries";
import { RecoveryBanner } from "#/features/deployments/recovery-banner";
import { hasProcesses } from "#/features/processes/model";
import { ProcessesTable } from "#/features/processes/processes-table";
import { ComponentsTable } from "#/features/stacks/components-table";
import { COMPOSE_FIELD, composeChangeText } from "#/features/stacks/diff";
import { isStack } from "#/features/stacks/model";
import { useInspectorActions } from "../inspector-context";
import { openConnectionVariables } from "../overview/connection-link";
import { HealthCard } from "../overview/health-card";
import { Incidents } from "../overview/incidents";
import { LatestDeployments } from "../overview/latest-deployments";
import { RecentLogs } from "../overview/recent-logs";
import { ResourcesCard } from "../overview/resources-card";
import { ScalingSummary } from "../overview/scaling-summary";
import { OverviewSection } from "../overview/section";
import { UptimeCard } from "../overview/uptime-card";
import { VariablesPreview } from "../overview/variables-preview";
import type { ServiceTabProps } from "./types";

/*
 * What stands in for a deferred card: the blocks its own loading state shows,
 * built once. The section headings are already in place, so nothing moves.
 */
const DEPLOYMENTS_PENDING = (
	<div className="flex flex-col gap-2">
		<Skeleton className="h-9 w-full" />
		<Skeleton className="h-9 w-full" />
	</div>
);
const LOGS_PENDING = <Skeleton className="h-[104px] w-full rounded-card" />;
const VARIABLES_PENDING = (
	<div className="flex flex-col gap-2">
		<Skeleton className="h-5 w-full" />
		<Skeleton className="h-5 w-full" />
	</div>
);

/**
 * Overview: a dashboard of the service's state. Health, resources
 * and scaling beside the latest deployments, recent logs and a variables
 * preview (one column when narrow), each with the link to the tab that goes
 * deeper. A failed or waiting deployment, any staged changes and, for a
 * database, its header sit on top, where they cannot be missed.
 */
export function OverviewTab({ service }: ServiceTabProps) {
	const { openTab } = useInspectorActions();
	const latestId = service.latestDeployment?.id;
	const latest = useQuery({
		...deploymentQuery(latestId ?? ""),
		enabled: latestId !== undefined,
	});
	const pending = useQuery({
		...pendingChangesQuery(service.id),
		enabled: service.hasStagedChanges,
	});

	const database = isManagedDatabase(service);
	const stack = isStack(service);

	// The first render paints Health, Resources and Scaling; the cards below
	// them, with the three requests and the log stream they start, mount in a
	// second, interruptible render that React runs once the first has painted.
	const settled = useDeferredValue(true, false);

	// The body is the container: from 760px it takes two columns (state on the
	// left, history on the right); below that one, with Resources straight
	// after Health. The column wrappers are `contents` until then, so the one
	// column keeps the DOM order.
	return (
		<div className="@container min-h-full">
			<section
				data-tab="overview"
				data-service-id={service.id}
				aria-label="Overview"
				className="flex min-h-full flex-col gap-5 px-4 pt-4 pb-6"
			>
				{latest.data ? (
					<RecoveryBanner deployment={latest.data} service={service} />
				) : null}

				{service.hasStagedChanges && pending.data?.length !== 0 ? (
					<Alert variant="warning" role="status">
						<AlertTitle>
							Staged changes{pending.data ? ` (${pending.data.length})` : ""}
						</AlertTitle>
						<AlertDescription>
							<p>
								These apply the next time {service.name} is deployed. Review
								them from the Deploy button.
							</p>
							{pending.data && pending.data.length > 0 ? (
								<ul className="flex flex-col gap-1 font-mono text-[11px] leading-[1.4]">
									{pending.data.slice(0, 4).map((change) => (
										<li
											key={change.field}
											className="truncate text-ink-2"
											title={
												change.field === COMPOSE_FIELD
													? composeChangeText(change.from, change.to)
													: `${change.field}: ${change.from} → ${change.to}`
											}
										>
											<span className="text-status-staged-on">~</span>{" "}
											{change.field}
											<span className="text-ink-3">
												{" "}
												→{" "}
												{change.field === COMPOSE_FIELD
													? composeChangeText(change.from, change.to)
													: change.to || "(removed)"}
											</span>
										</li>
									))}
								</ul>
							) : null}
						</AlertDescription>
					</Alert>
				) : null}

				{database ? (
					<DatabaseSection
						service={service}
						onOpenConnectionVariables={() => openConnectionVariables(openTab)}
					/>
				) : null}

				{stack ? (
					<OverviewSection id="overview-components" title="Components">
						<ComponentsTable service={service} />
					</OverviewSection>
				) : null}

				{hasProcesses(service) ? (
					<OverviewSection id="overview-processes" title="Processes">
						<ProcessesTable service={service} />
					</OverviewSection>
				) : null}

				<div className="grid grid-cols-1 gap-5 @min-[760px]:grid-cols-2 @min-[760px]:items-start">
					<div className="contents @min-[760px]:flex @min-[760px]:flex-col @min-[760px]:gap-5">
						<OverviewSection id="overview-health" title="Health">
							<HealthCard service={service} />
						</OverviewSection>

						{settled ? <UptimeCard service={service} /> : null}

						{settled ? <Incidents service={service} /> : null}

						<OverviewSection
							id="overview-resources"
							title="Resources"
							action={{ label: "Metrics", onClick: () => openTab("metrics") }}
						>
							<ResourcesCard service={service} />
						</OverviewSection>

						{stack ? null : (
							<OverviewSection title="Scaling">
								<ScalingSummary service={service} />
							</OverviewSection>
						)}
					</div>

					<div className="contents @min-[760px]:flex @min-[760px]:flex-col @min-[760px]:gap-5">
						<OverviewSection
							id="overview-deployments"
							title="Latest deployments"
							action={{
								label: "View all",
								onClick: () => openTab("deployments"),
							}}
						>
							{settled ? (
								<LatestDeployments service={service} />
							) : (
								DEPLOYMENTS_PENDING
							)}
						</OverviewSection>

						<OverviewSection
							id="overview-logs"
							title="Recent logs"
							action={{ label: "Open", onClick: () => openTab("logs") }}
						>
							{settled ? <RecentLogs service={service} /> : LOGS_PENDING}
						</OverviewSection>

						<OverviewSection
							title={stack ? "Variables for interpolation" : "Variables"}
							action={{ label: "Open", onClick: () => openTab("variables") }}
						>
							{settled ? (
								<VariablesPreview service={service} />
							) : (
								VARIABLES_PENDING
							)}
						</OverviewSection>
					</div>
				</div>
			</section>
		</div>
	);
}
