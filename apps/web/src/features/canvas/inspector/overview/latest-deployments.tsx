import { useQuery } from "@tanstack/react-query";
import { deploymentsQuery } from "#/api/hooks";
import type { Deployment, Service } from "#/api/models";
import { Relative } from "#/components/common/relative";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	deploymentMessage,
	deploymentStatusKind,
	failedAtLabel,
	releaseDetail,
	releaseLabel,
	STATUS_LABEL,
} from "#/features/deployments/labels";

/** "a91c3f2 · Live", "3fd29a8 · Failed at Verify": release plus where it stands. */
function standing(deployment: Deployment, service: Service): string {
	const live = deployment.id === service.currentDeploymentId;
	const failedStep =
		deployment.id === service.latestDeployment?.id
			? failedAtLabel(service.latestDeployment.failedStep)
			: null;
	let state: string = STATUS_LABEL[deployment.status];
	if (live) state = "Live";
	else if (deployment.status === "succeeded") state = "Superseded";
	else if (failedStep) state = failedStep;
	return `${releaseLabel(deployment.release?.source, deployment.release?.imageRef)} · ${state}`;
}

/** The three newest deployments, as the Overview's list. */
export function LatestDeployments({ service }: { service: Service }) {
	const deployments = useQuery(deploymentsQuery(service.id));
	return (
		<SkeletonReveal
			loading={deployments.isPending}
			className="flex flex-col gap-2"
			skeleton={
				<>
					<Skeleton className="h-9 w-full" />
					<Skeleton className="h-9 w-full" />
				</>
			}
		>
			{() => (
				<DeploymentRows
					service={service}
					failed={deployments.isError}
					deployments={deployments.data ?? []}
				/>
			)}
		</SkeletonReveal>
	);
}

function DeploymentRows({
	service,
	failed,
	deployments,
}: {
	service: Service;
	failed: boolean;
	deployments: readonly Deployment[];
}) {
	if (failed) {
		return <p className="py-2 text-ink-3 text-xs">Deployments did not load.</p>;
	}
	const items = deployments.slice(0, 3);
	if (items.length === 0) {
		return (
			<p className="py-2 text-ink-3 text-xs">Nothing has been deployed yet.</p>
		);
	}

	return (
		<ol className="rule-rows">
			{items.map((deployment) => {
				const kind = deploymentStatusKind(
					deployment.status,
					deployment.id === service.currentDeploymentId,
				);
				const { text } = deploymentMessage(
					deployment,
					deployment.id === service.latestDeployment?.id
						? service.latestDeployment.failedStep
						: null,
				);
				return (
					<li
						key={deployment.id}
						className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3"
					>
						{kind ? (
							<StatusIcon kind={kind} label="" />
						) : (
							<span aria-hidden className="size-3.5" />
						)}
						<div className="flex min-w-0 flex-col gap-1.5">
							<span
								title={text}
								className="truncate font-medium text-ink-1 text-xs leading-none"
							>
								{text}
							</span>
							<span
								title={releaseDetail(
									deployment.release?.source,
									deployment.release?.imageRef,
								)}
								className="truncate font-mono text-[10.5px] text-ink-3 leading-none"
							>
								{standing(deployment, service)}
							</span>
						</div>
						<div className="flex flex-col items-end gap-1.5 text-[10.5px] text-ink-3 leading-none">
							<Relative iso={deployment.createdAt} />
							<span>{deployment.actorLabel}</span>
						</div>
					</li>
				);
			})}
		</ol>
	);
}
