import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import type { Environment, Service } from "#/api/models";
import { DocsButton } from "#/components/common/docs-button";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { EnvironmentPage } from "#/components/shell/page-frame";
import { Button } from "#/components/ui/button";
import { Kbd } from "#/components/ui/kbd";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { TrafficPanel } from "#/features/analytics";
import { useServices } from "#/features/canvas/data";
import { nodeStatus } from "#/features/canvas/lib/status";
import {
	attentionCount,
	EnvironmentUptimeError,
	EnvironmentUptimeList,
	EnvironmentUptimeSkeleton,
	environmentUptimeQuery,
} from "#/features/uptime";
import { EnvLogStream } from "./env-log-stream";
import { ServiceResourceCards } from "./service-resource-cards";

function Section({
	title,
	note,
	children,
}: {
	title: string;
	note?: ReactNode;
	children: ReactNode;
}) {
	return (
		<section className="flex min-w-0 flex-col gap-3">
			<h2 className="section-head">
				{title}
				{note ? (
					<span className="font-sans text-xs normal-case tracking-normal">
						{note}
					</span>
				) : null}
			</h2>
			{children}
		</section>
	);
}

function ObservabilityBody({
	environment,
	projectId,
	envSlug,
}: {
	environment: Environment;
	projectId: string;
	envSlug: string;
}) {
	const services = useServices(environment.id);
	return (
		<SkeletonReveal
			loading={services.isPending}
			skeleton={
				<div
					className="flex flex-col gap-4"
					role="status"
					aria-label="Loading observability"
				>
					<Skeleton className="h-48 rounded-card" />
					<Skeleton className="h-56 rounded-card" />
					<Skeleton className="h-72 rounded-card" />
				</div>
			}
		>
			{() => (
				<ObservabilityContent
					environment={environment}
					projectId={projectId}
					envSlug={envSlug}
					list={services.data ?? []}
					failed={services.isError && !services.data}
					retry={() => void services.refetch()}
				/>
			)}
		</SkeletonReveal>
	);
}

function ObservabilityContent({
	environment,
	projectId,
	envSlug,
	list,
	failed,
	retry,
}: {
	environment: Environment;
	projectId: string;
	envSlug: string;
	list: readonly Service[];
	failed: boolean;
	retry: () => void;
}) {
	const uptime = useQuery(environmentUptimeQuery(environment.id));
	const attention = attentionCount(uptime.data?.items);
	const failing = list.filter(
		(service) => nodeStatus(service).group === "failed",
	).length;

	if (failed) {
		return (
			<ErrorState
				frame="raised"
				title="Services did not load"
				body="The control plane did not answer, so there is nothing to show yet. Nothing was changed; try again."
				retry={retry}
			/>
		);
	}
	if (list.length === 0) {
		return (
			<EmptyState
				illustration="radar"
				frame="none"
				size="lg"
				title="Create a service to see it here"
				body={`Logs, resource use and traffic of ${environment.name} appear here once it has a deployed service.`}
				action={
					<Button
						variant="primary"
						nativeButton={false}
						render={
							<Link
								to="/p/$projectId/$envSlug"
								params={{ projectId, envSlug }}
							/>
						}
					>
						Open the canvas
					</Button>
				}
				secondaryAction={
					<DocsButton path="operations/monitoring.md">
						How monitoring works
					</DocsButton>
				}
				hint={
					<>
						<Kbd>⌘K</Kbd> opens the command palette
					</>
				}
			/>
		);
	}

	return (
		<div className="flex min-w-0 flex-col gap-8">
			<Section
				title="Uptime"
				note={
					attention > 0
						? `${attention} down or degraded`
						: "Checked every 30 seconds from each service's server."
				}
			>
				{uptime.isPending ? (
					<EnvironmentUptimeSkeleton />
				) : uptime.data ? (
					<EnvironmentUptimeList
						items={uptime.data.items ?? []}
						projectId={projectId}
						envSlug={envSlug}
					/>
				) : (
					<EnvironmentUptimeError retry={() => void uptime.refetch()} />
				)}
			</Section>
			<Section
				title="Traffic"
				note="Requests through the proxies of this environment's services."
			>
				<TrafficPanel
					scope={{ environmentId: environment.id }}
					variant="summary"
				/>
			</Section>
			<Section
				title="Services"
				note={`${list.length} ${list.length === 1 ? "service" : "services"}${failing > 0 ? `, ${failing} failing` : ", none failing"}`}
			>
				<ServiceResourceCards
					services={list}
					projectId={projectId}
					envSlug={envSlug}
				/>
			</Section>
			<Section
				title="Logs"
				note="Runtime output of every service, merged in time order."
			>
				<EnvLogStream environmentId={environment.id} services={list} />
			</Section>
		</div>
	);
}

/**
 * The environment's Observability page: each service's uptime, a traffic
 * summary, a resource card per service and the merged runtime log, on one
 * scrolling page.
 */
export function ObservabilityPage({
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
			page="observability"
			title="Observability"
			description={(environment) =>
				`Uptime, traffic, resource use and runtime logs of ${environment.name}.`
			}
		>
			{(environment) => (
				<ObservabilityBody
					environment={environment}
					projectId={projectId}
					envSlug={envSlug}
				/>
			)}
		</EnvironmentPage>
	);
}
