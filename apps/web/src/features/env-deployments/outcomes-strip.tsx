import { DitherUptimeBar } from "@nebulactrl/dither/charts/uptime-bar";
import { useMemo } from "react";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { OUTCOME_WORDS, outcomeDays, outcomeSuccessShare } from "./outcomes";
import { useEnvDeploymentOutcomes } from "./queries";

/**
 * Deployments per day for the last 30 days, one cell a day: green when
 * everything shipped, amber with a canceled or rolled back deployment, red
 * with a failed one. Counts come from an aggregate over the whole
 * environment. Hidden for an environment that has never deployed.
 */
export function DeploymentOutcomes({
	environmentId,
}: {
	environmentId: string;
}) {
	const outcomes = useEnvDeploymentOutcomes(environmentId);
	const raw = outcomes.data?.days;
	const days = useMemo(() => outcomeDays(raw ?? []), [raw]);
	const share = useMemo(() => outcomeSuccessShare(raw ?? []), [raw]);

	return (
		<SkeletonReveal
			loading={outcomes.isPending}
			className="empty:hidden"
			skeleton={
				<div role="status" aria-label="Loading deployment history">
					<Skeleton className="h-[72px] rounded-card" />
				</div>
			}
		>
			{() => {
				if (outcomes.isError) {
					return (
						<p className="text-ink-3 text-xs">
							The 30-day history did not load. The list below is unaffected.
						</p>
					);
				}
				if (share === undefined) return null;
				return (
					<div className="surface-raised rounded-card px-4 py-3">
						<DitherUptimeBar
							label="Deployments, last 30 days"
							days={days}
							words={OUTCOME_WORDS}
							caption="succeeded"
							uptime={share}
							height={28}
							cell={2}
						/>
					</div>
				);
			}}
		</SkeletonReveal>
	);
}
