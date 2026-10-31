import { Link, useNavigate } from "@tanstack/react-router";
import { cn } from "cn";
import { ChevronRightIcon } from "lucide-react";
import { useEffect, useMemo } from "react";
import { errorMessage } from "#/api/client-config";
import type { EnvDeploymentView } from "#/api/gen/types.gen";
import {
	asDeploymentStatus,
	type Environment,
	toDeployment,
} from "#/api/models";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { EnvironmentPage } from "#/components/shell/page-frame";
import { Button } from "#/components/ui/button";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { useServices } from "#/features/canvas/data";
import {
	ServiceIcon,
	type ServiceIconSource,
} from "#/features/canvas/service-icon";
import {
	deploymentMessage,
	durationMs,
	isInFlight,
	STATUS_LABEL,
	TRIGGER_LABEL,
} from "#/features/deployments/labels";
import { OptionSelect } from "#/features/metrics/ui/option-select";
import { formatDuration } from "#/lib/format";
import { vt } from "#/lib/transitions";
import { DeploymentOutcomes } from "./outcomes-strip";
import { useEnvDeployments } from "./queries";
import {
	actorText,
	isStatusFilter,
	STATUS_FILTERS,
	STATUS_KIND,
	type StatusFilter,
} from "./status";

const ALL_SERVICES = "all";

/** The kind filter: every deployment, or only those that built an image. */
const KIND_OPTIONS = [
	{ value: "all", label: "All deployments" },
	{ value: "builds", label: "Builds" },
] as const;

export interface DeploymentFilterChange {
	status?: string | undefined;
	service?: string | undefined;
	type?: "builds" | undefined;
}

function Row({
	item,
	environment,
	service,
}: {
	item: EnvDeploymentView;
	environment: Environment;
	service: ServiceIconSource | undefined;
}) {
	const navigate = useNavigate();
	const status = asDeploymentStatus(item.status);
	const deployment = toDeployment(item);
	const duration = durationMs(deployment);
	const message = deploymentMessage(deployment);
	const open = () =>
		void navigate({
			to: "/p/$projectId/$envSlug",
			params: { projectId: environment.projectId, envSlug: environment.slug },
			search: { service: item.serviceId, tab: "deployments" },
		});
	return (
		<TableRow
			{...vt("service", item.serviceId)}
			className="cursor-pointer"
			onClick={(event) => {
				// The service name is a real link; a click anywhere else on the
				// row does the same for pointer users.
				if ((event.target as HTMLElement).closest("a,button")) return;
				open();
			}}
		>
			<TableCell pin="end" className="h-12 whitespace-nowrap">
				<span className="flex items-center gap-2">
					<StatusIcon kind={STATUS_KIND[status]} label="" />
					<span className="text-[13px] text-ink-1">{STATUS_LABEL[status]}</span>
				</span>
			</TableCell>
			<TableCell pin="start" label="Service">
				<Link
					to="/p/$projectId/$envSlug"
					params={{
						projectId: environment.projectId,
						envSlug: environment.slug,
					}}
					search={{ service: item.serviceId, tab: "deployments" }}
					className="flex min-w-0 items-center gap-2.5 rounded-sm outline-none focus-visible:focus-ring"
				>
					<ServiceIcon
						service={service ?? { kind: "app", sourceType: "image" }}
						size={24}
					/>
					<span className="truncate font-semibold text-[13px] text-ink-1">
						{item.serviceName}
					</span>
				</Link>
			</TableCell>
			<TableCell label="Message" className="max-w-[340px]">
				{/* The message is the row's own link: it opens the deployment. */}
				<Link
					to="/p/$projectId/$envSlug"
					params={{
						projectId: environment.projectId,
						envSlug: environment.slug,
					}}
					search={{ service: item.serviceId, tab: "deployments" }}
					title="Open this deployment"
					className="flex min-w-0 flex-col gap-[3px] rounded-sm outline-none hover:[&>span:first-child]:text-accent-ink focus-visible:focus-ring"
				>
					<span
						className={cn(
							"truncate text-[13px] transition-colors duration-quick",
							message.kind === "note" ? "text-ink-1" : "text-ink-3",
						)}
						title={message.text}
					>
						{message.text}
					</span>
					{message.detail ? (
						<span className="truncate font-mono text-[11px] text-ink-4">
							{message.detail}
						</span>
					) : null}
				</Link>
			</TableCell>
			<TableCell label="Trigger">
				<span className="whitespace-nowrap text-[12px] text-ink-3">
					{TRIGGER_LABEL[item.trigger]}
				</span>
			</TableCell>
			<TableCell label="Actor">
				<span className="whitespace-nowrap text-[12px] text-ink-3">
					{actorText(item.actorType, item.actorLabel)}
				</span>
			</TableCell>
			<TableCell label="Started">
				<span className="whitespace-nowrap font-mono text-[12px] text-ink-3">
					<Relative iso={item.startedAt ?? item.createdAt} />
				</span>
			</TableCell>
			<TableCell label="Finished">
				<span className="whitespace-nowrap font-mono text-[12px] text-ink-3">
					{isInFlight(status) ? "running" : <Relative iso={item.finishedAt} />}
				</span>
			</TableCell>
			<TableCell label="Duration">
				<span className="whitespace-nowrap font-mono text-[12px] text-ink-3 tabular-nums">
					{duration === null ? "—" : formatDuration(duration)}
				</span>
			</TableCell>
			<TableCell className="w-px pl-0" aria-hidden>
				<ChevronRightIcon className="size-3.5 text-ink-4 transition-colors duration-quick group-hover/row:text-ink-2" />
			</TableCell>
		</TableRow>
	);
}

function DeploymentsList({
	environment,
	status,
	serviceId,
	buildsOnly,
	onFilterChange,
}: {
	environment: Environment;
	status: string | undefined;
	serviceId: string | undefined;
	buildsOnly: boolean;
	onFilterChange(next: DeploymentFilterChange): void;
}) {
	const services = useServices(environment.id);
	const statusFilter: StatusFilter = isStatusFilter(status) ? status : "all";
	const statuses = STATUS_FILTERS.find(
		(filter) => filter.value === statusFilter,
	)?.statuses;
	const deployments = useEnvDeployments(environment.id, {
		status: statuses,
		serviceId,
	});

	// A deployment that built an image carries the build's method; the control
	// plane has no filter for it, so the loaded pages are narrowed here.
	const items = useMemo(
		() =>
			(deployments.data?.pages ?? [])
				.flatMap((page) => page.items ?? [])
				.filter((item) => !buildsOnly || Boolean(item.build?.method)),
		[deployments.data, buildsOnly],
	);
	const { hasNextPage, isFetchingNextPage, fetchNextPage } = deployments;
	// Narrowed to nothing so far: the older pages may still hold builds.
	useEffect(() => {
		if (
			buildsOnly &&
			items.length === 0 &&
			hasNextPage &&
			!isFetchingNextPage
		) {
			void fetchNextPage();
		}
	}, [
		buildsOnly,
		items.length,
		hasNextPage,
		isFetchingNextPage,
		fetchNextPage,
	]);
	const byId = useMemo(
		() => new Map((services.data ?? []).map((s) => [s.id, s])),
		[services.data],
	);
	const serviceOptions = useMemo(
		() => [
			{ value: ALL_SERVICES, label: "All services" },
			...(services.data ?? []).map((s) => ({ value: s.id, label: s.name })),
		],
		[services.data],
	);
	const filtered =
		statusFilter !== "all" || serviceId !== undefined || buildsOnly;

	const filters = (
		<div className="flex flex-wrap items-center gap-2">
			<OptionSelect
				aria-label="Filter by type"
				value={buildsOnly ? "builds" : "all"}
				options={KIND_OPTIONS.map(({ value, label }) => ({ value, label }))}
				onChange={(next) =>
					onFilterChange({ type: next === "builds" ? "builds" : undefined })
				}
			/>
			<OptionSelect
				aria-label="Filter by status"
				value={statusFilter}
				options={STATUS_FILTERS.map(({ value, label }) => ({ value, label }))}
				onChange={(next) =>
					onFilterChange({ status: next === "all" ? undefined : next })
				}
			/>
			<OptionSelect
				aria-label="Filter by service"
				value={serviceId ?? ALL_SERVICES}
				options={serviceOptions}
				onChange={(next) =>
					onFilterChange({ service: next === ALL_SERVICES ? undefined : next })
				}
			/>
			{filtered ? (
				<Button
					variant="ghost"
					size="sm"
					onClick={() =>
						onFilterChange({
							status: undefined,
							service: undefined,
							type: undefined,
						})
					}
				>
					Clear filters
				</Button>
			) : null}
		</div>
	);

	let body: React.ReactNode;
	if (deployments.isError && items.length === 0) {
		body = (
			<ErrorState
				frame="raised"
				title="Deployments did not load"
				body={`${errorMessage(deployments.error)} Nothing was changed; try again in a moment.`}
				retry={() => void deployments.refetch()}
			/>
		);
	} else if (items.length === 0) {
		body = (
			<EmptyState
				illustration={filtered ? "search" : "box"}
				title={filtered ? "No deployment matches" : "Nothing deployed yet"}
				body={
					filtered
						? "Change or clear the filters to see the rest of this environment's deployments."
						: `Deployments of ${environment.name}'s services appear here as they start. Deploy a service from the canvas to begin.`
				}
				action={
					filtered ? (
						<Button
							variant="secondary"
							onClick={() =>
								onFilterChange({
									status: undefined,
									service: undefined,
									type: undefined,
								})
							}
						>
							Clear filters
						</Button>
					) : undefined
				}
			/>
		);
	} else {
		body = (
			<div className="flex flex-col gap-3">
				<div className="surface-raised overflow-hidden rounded-card">
					<Table stack>
						<TableHeader>
							<TableRow>
								<TableHead>Status</TableHead>
								<TableHead>Service</TableHead>
								<TableHead>Message</TableHead>
								<TableHead>Trigger</TableHead>
								<TableHead>Actor</TableHead>
								<TableHead>Started</TableHead>
								<TableHead>Finished</TableHead>
								<TableHead>Duration</TableHead>
								<TableHead className="w-px" />
							</TableRow>
						</TableHeader>
						<TableBody>
							{items.map((item) => (
								<Row
									key={item.id}
									item={item}
									environment={environment}
									service={byId.get(item.serviceId)}
								/>
							))}
						</TableBody>
					</Table>
				</div>
				{deployments.hasNextPage ? (
					<Button
						variant="secondary"
						className="self-center"
						disabled={deployments.isFetchingNextPage}
						onClick={() => void deployments.fetchNextPage()}
					>
						{deployments.isFetchingNextPage ? (
							<Spinner data-icon="inline-start" />
						) : null}
						Load older deployments
					</Button>
				) : null}
			</div>
		);
	}

	return (
		<div className="flex flex-col gap-4">
			<DeploymentOutcomes environmentId={environment.id} />
			{filters}
			<SkeletonReveal
				loading={deployments.isPending}
				skeleton={
					<div role="status" aria-label="Loading deployments">
						<Skeleton className="h-64 rounded-card" />
					</div>
				}
			>
				{() => body}
			</SkeletonReveal>
		</div>
	);
}

/**
 * The Deployments page: every deployment of every service in the
 * environment, newest first. A row opens that service's Deployments tab in
 * the canvas inspector.
 */
export function DeploymentsPage({
	projectId,
	envSlug,
	status,
	serviceId,
	buildsOnly,
	onFilterChange,
}: {
	projectId: string;
	envSlug: string;
	status: string | undefined;
	serviceId: string | undefined;
	buildsOnly: boolean;
	onFilterChange(next: DeploymentFilterChange): void;
}) {
	return (
		<EnvironmentPage
			projectId={projectId}
			envSlug={envSlug}
			page="deployments"
			title="Deployments"
			description={(environment) =>
				`Every release of every service in ${environment.name}, newest first.`
			}
		>
			{(environment) => (
				<DeploymentsList
					environment={environment}
					status={status}
					serviceId={serviceId}
					buildsOnly={buildsOnly}
					onFilterChange={onFilterChange}
				/>
			)}
		</EnvironmentPage>
	);
}
