import { useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import { HammerIcon, RocketIcon, RotateCcwIcon, XIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { deploymentsQuery } from "#/api/hooks";
import type { Deployment, Service } from "#/api/models";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { count, formatDuration } from "#/lib/format";
import { DeploymentDetail } from "./deployment-detail";
import {
	deploymentMessage,
	deploymentStatusKind,
	durationMs,
	failedAtLabel,
	isCancelable,
	isInFlight,
	releaseDetail,
	releaseLabel,
	STATUS_LABEL,
	TRIGGER_LABEL,
} from "./labels";
import { RecoveryBanner } from "./recovery-banner";
import { useDeploymentActions } from "./use-deployment-actions";

/** The status word's colour; the word itself always says the state. */
const KIND_TEXT: Partial<Record<StatusKind, string>> = {
	building: "text-status-building-on",
	deploying: "text-status-deploying-on",
	verifying: "text-status-deploying-on",
	live: "text-status-live-on",
	failed: "text-status-failed-on",
	degraded: "text-status-degraded-on",
};

/**
 * The deployment history: newest first, each row opening to its step
 * timeline and build/deploy log (which follows live while the deployment is
 * running). Redeploy, Cancel and "Rollback to this release" live here.
 */
export function DeploymentsTab({ service }: { service: Service }) {
	const deployments = useQuery(deploymentsQuery(service.id));
	const actions = useDeploymentActions(service);
	const [expanded, setExpanded] = useState<string | null>(null);
	const items = deployments.data;
	const active = items?.find((deployment) => isInFlight(deployment.status));
	const latest = items?.[0];

	// The deployment that is running opens by itself, so a deploy started from
	// anywhere shows its progress here without a click.
	const activeId = active?.id;
	useEffect(() => {
		if (activeId) setExpanded(activeId);
	}, [activeId]);

	return (
		<SkeletonReveal
			loading={deployments.isPending}
			className="flex min-h-full flex-col"
			skeleton={
				<div
					className="flex flex-col gap-2 p-4"
					role="status"
					aria-label="Loading deployments"
				>
					<Skeleton className="h-14 w-full rounded-card" />
					<Skeleton className="h-14 w-full rounded-card" />
				</div>
			}
		>
			{() =>
				deployments.isError ? (
					<ErrorState
						className="m-4"
						title="Deployments did not load"
						body={errorMessage(deployments.error)}
						retry={() => void deployments.refetch()}
					/>
				) : (
					<section
						data-tab="deployments"
						data-service-id={service.id}
						aria-label="Deployments"
						className="flex min-h-full min-w-0 flex-1 flex-col gap-4 p-4"
					>
						<header className="flex items-center gap-2">
							<h3 className="type-label mr-auto">
								{count(items?.length ?? 0, "deployment")}
							</h3>
							{service.sourceType === "git" ? (
								<Button
									size="md"
									variant="secondary"
									disabled={
										actions.deploy.isPending ||
										actions.deployNoCache.isPending ||
										active !== undefined
									}
									onClick={() => void actions.confirmDeployNoCache()}
									title={
										active
											? "A deployment is already running"
											: "Build from scratch: ignore every cached layer and pull the base images again"
									}
								>
									<HammerIcon aria-hidden />
									Without cache
								</Button>
							) : null}
						</header>

						{latest ? (
							<RecoveryBanner deployment={latest} service={service} />
						) : null}

						{items && items.length > 0 ? (
							<ol
								className="flex flex-col gap-2"
								aria-label="Deployments, newest first"
							>
								{items.map((deployment) => (
									<DeploymentRow
										key={deployment.id}
										deployment={deployment}
										service={service}
										open={expanded === deployment.id}
										canceling={
											actions.cancel.isPending &&
											actions.cancel.variables === deployment.id
										}
										onToggle={() =>
											setExpanded((current) =>
												current === deployment.id ? null : deployment.id,
											)
										}
										onRollback={() =>
											void actions.rollback(
												deployment,
												releaseLabel(
													deployment.release?.source,
													deployment.release?.imageRef,
												),
											)
										}
										onCancel={() => void actions.confirmCancel(deployment)}
									/>
								))}
							</ol>
						) : (
							<div className="surface-raised flex flex-col items-center gap-2 rounded-card px-5 py-8 text-center">
								<span className="surface-well grid size-10 place-items-center rounded-card text-ink-3">
									<RocketIcon aria-hidden className="size-4" />
								</span>
								<p className="type-panel-title text-ink-1">Not deployed yet</p>
								<p className="type-secondary max-w-[34ch]">
									Redeploy from the panel header sends the service's
									configuration to its server.
								</p>
							</div>
						)}
					</section>
				)
			}
		</SkeletonReveal>
	);
}

function DeploymentRow({
	deployment,
	service,
	open,
	canceling,
	onToggle,
	onRollback,
	onCancel,
}: {
	deployment: Deployment;
	service: Service;
	open: boolean;
	canceling: boolean;
	onToggle(): void;
	onRollback(): void;
	onCancel(): void;
}) {
	const live = deployment.id === service.currentDeploymentId;
	const duration = durationMs(deployment);
	const kind = deploymentStatusKind(deployment.status, live);
	// The status word below already says where it failed, so the headline
	// carries only the reason.
	const message = deploymentMessage(deployment);
	const headline = message.text;
	// Only the service's latest deployment carries the step it failed at.
	const failedStep =
		service.latestDeployment?.id === deployment.id
			? service.latestDeployment.failedStep
			: null;
	const statusText =
		deployment.status === "failed"
			? (failedAtLabel(failedStep) ?? STATUS_LABEL.failed)
			: STATUS_LABEL[deployment.status];
	const meta = [
		TRIGGER_LABEL[deployment.trigger],
		message.detail ?? "",
		deployment.actorLabel,
		duration !== null && !isInFlight(deployment.status)
			? formatDuration(duration)
			: "",
	].filter((part) => part !== "");
	const canRollback =
		deployment.status === "succeeded" &&
		!live &&
		deployment.releaseId !== null &&
		deployment.releaseId !== service.currentRelease?.id;
	const cancelable = isCancelable(deployment.status);
	const panelId = `deployment-${deployment.id}`;

	return (
		<li className="surface-raised min-w-0 rounded-card">
			<button
				type="button"
				aria-expanded={open}
				aria-controls={panelId}
				onClick={onToggle}
				className="flex w-full min-w-0 cursor-pointer items-start gap-2.5 rounded-card px-3 py-2.5 text-left outline-none transition-[background-color] duration-quick ease-out hover:bg-surface-hover focus-visible:focus-ring"
			>
				<DisclosureChevron
					open={open}
					className="mt-[3px] size-3.5 shrink-0 text-ink-3"
				/>
				{kind ? (
					<StatusIcon kind={kind} label="" className="mt-[3px]" />
				) : (
					<span aria-hidden className="mt-[3px] size-3.5 shrink-0" />
				)}
				<span className="flex min-w-0 flex-1 flex-col gap-1">
					<span className="flex min-w-0 items-center gap-2">
						<span
							className="min-w-0 truncate font-medium text-[13px] text-ink-1 leading-5"
							title={headline}
						>
							{headline}
						</span>
						{live ? <Badge tone="live">Live</Badge> : null}
					</span>
					<span
						className="block truncate font-mono text-[11px] text-ink-3 leading-4"
						title={`${statusText} · ${meta.join(" · ")} · ${releaseDetail(
							deployment.release?.source,
							deployment.release?.imageRef,
						)}`}
					>
						<span className={cn("font-medium", kind && KIND_TEXT[kind])}>
							{statusText}
						</span>
						{` · ${meta.join(" · ")}`}
					</span>
				</span>
				<span className="shrink-0 pt-0.5 text-[11px] text-ink-4 leading-4">
					<Relative iso={deployment.createdAt} />
				</span>
			</button>
			{canRollback || cancelable ? (
				<div className="flex flex-wrap gap-2 pr-3 pb-2.5 pl-9">
					{cancelable ? (
						<Button
							variant="danger"
							size="sm"
							disabled={canceling || !!deployment.cancelRequestedAt}
							onClick={onCancel}
							title="Stop this deployment at the step it has reached; the current release keeps serving"
						>
							{canceling || deployment.cancelRequestedAt ? (
								<Spinner />
							) : (
								<XIcon aria-hidden />
							)}
							{deployment.cancelRequestedAt
								? "Canceling…"
								: "Cancel deployment"}
						</Button>
					) : null}
					{canRollback ? (
						<Button
							variant="secondary"
							size="sm"
							onClick={onRollback}
							title="Deploy this release again"
						>
							<RotateCcwIcon aria-hidden />
							Rollback to this release
						</Button>
					) : null}
				</div>
			) : null}
			<DisclosurePanel open={open} id={panelId}>
				<div className="border-line-faint border-t">
					<DeploymentDetail deployment={deployment} />
				</div>
			</DisclosurePanel>
		</li>
	);
}
