import {
	DitherCapacityStat,
	DitherMeter,
} from "@nebulactrl/dither/charts/meter";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { cn } from "cn";
import { BoxIcon, DatabaseIcon, PlusIcon, ServerIcon } from "lucide-react";
import type { FleetServer, FleetService } from "#/api/gen/types.gen";
import { DocsButton } from "#/components/common/docs-button";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { KpiNumber, renderKpi } from "#/components/common/kpi-number";
import { MetricTile } from "#/components/common/metric-tile";
import { ToneIcon } from "#/components/common/tone-icon";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Kbd } from "#/components/ui/kbd";
import { Skeleton } from "#/components/ui/skeleton";
import { StatGrid } from "#/components/ui/stat-grid";
import { StatusIcon } from "#/components/ui/status-icon";
import { ServerStatus } from "#/features/servers/ui/server-status";
import { formatBytes } from "#/lib/format";
import { DrainChip } from "./drain-chip";
import {
	fleetServiceTone,
	memoryMeter,
	overcommitText,
	percentOf,
	serviceMemPercent,
	visibleFleetServers,
} from "./model";
import { type FleetData, fleetQuery } from "./queries";

function Totals({ data }: { data: FleetData }) {
	const { totals } = data;
	const overcommitted = data.servers.filter(
		(server) => memoryMeter(server).overcommitted,
	).length;
	const fleetMeter = memoryMeter(totals);
	return (
		<StatGrid>
			<MetricTile
				label="Servers"
				value={<KpiNumber text={String(totals.servers)} />}
			/>
			<MetricTile
				label="Services"
				value={<KpiNumber text={String(totals.services)} />}
			/>
			<MetricTile
				label="Memory used"
				value={
					<KpiNumber
						text={
							totals.memTotalBytes > 0 ? formatBytes(totals.memUsedBytes) : "–"
						}
					/>
				}
				hint={
					totals.memTotalBytes > 0 ? (
						<span className="flex flex-col gap-2">
							<span>{`of ${formatBytes(totals.memTotalBytes)} across the fleet`}</span>
							<DitherMeter
								bare
								label="Fleet memory"
								percent={fleetMeter.usedPercent}
								{...(fleetMeter.committedMarkerPercent === null
									? {}
									: {
											marker: {
												percent: fleetMeter.committedMarkerPercent,
												title: `Limits promised: ${formatBytes(totals.committedMemBytes)}`,
												warn: overcommitted > 0,
											},
										})}
							/>
						</span>
					) : (
						"Reported once an agent connects"
					)
				}
			/>
			<MetricTile
				label="Limits promised"
				value={<KpiNumber text={formatBytes(totals.committedMemBytes)} />}
				hint={
					overcommitted > 0 ? (
						<span
							data-tone="warning"
							className="tone-callout relative flex items-center gap-1.5 rounded-ctl px-2 py-1 text-warning-text"
						>
							<ToneIcon tone="warning" />
							<span>
								{overcommitted}{" "}
								{overcommitted === 1 ? "server is" : "servers are"}{" "}
								overcommitted
							</span>
						</span>
					) : (
						"Within capacity"
					)
				}
			/>
		</StatGrid>
	);
}

function ServiceRow({
	service,
	stale,
}: {
	service: FleetService;
	stale: boolean;
}) {
	const { kind, label } = fleetServiceTone(service.status);
	const KindIcon = service.kind === "database" ? DatabaseIcon : BoxIcon;
	const memPercent = serviceMemPercent(service);
	const memText =
		service.memBytes > 0 || service.memLimitBytes > 0
			? service.memLimitBytes > 0
				? `${formatBytes(service.memBytes)} / ${formatBytes(service.memLimitBytes)}`
				: `${formatBytes(service.memBytes)} · no limit`
			: "no usage yet";
	return (
		<li>
			<Link
				to="/p/$projectId/$envSlug"
				params={{
					projectId: service.projectId,
					envSlug: service.environmentSlug,
				}}
				search={{ service: service.id }}
				className="group/row grid items-center gap-x-4 gap-y-2 rounded-ctl px-3 py-2.5 text-ink-1 outline-none transition-[background-color] duration-150 ease-out hover:bg-white/4 hover:text-ink-1 focus-visible:focus-ring motion-reduce:transition-none sm:grid-cols-[minmax(0,1fr)_minmax(0,240px)]"
			>
				<span className="flex min-w-0 flex-col gap-1.5">
					<span className="flex items-center gap-2">
						<KindIcon className="size-3.5 shrink-0 text-ink-3" />
						<span
							title={service.name}
							className="min-w-0 truncate font-medium text-[13px] leading-none"
						>
							{service.name}
						</span>
						<span className="inline-flex shrink-0 items-center gap-1.5 text-ink-3 text-xs leading-none">
							<StatusIcon kind={kind} label="" />
							{label}
						</span>
						{service.stateful ? (
							<Badge
								tone="warn"
								title="Has volumes or is a database: its data does not move with it"
							>
								stateful
							</Badge>
						) : null}
					</span>
					<span className="type-mono truncate pl-[22px] text-ink-4 leading-none">
						{service.projectName} / {service.environmentName}
					</span>
				</span>
				<span className="flex flex-col gap-1.5">
					{memPercent === null ? null : (
						<DitherMeter
							bare
							label={`${service.name} memory`}
							percent={memPercent}
							stale={stale}
						/>
					)}
					<span
						className={cn(
							"type-mono text-ink-3 tabular-nums leading-none sm:text-right",
							stale && "opacity-50",
						)}
					>
						{memText}
					</span>
				</span>
			</Link>
		</li>
	);
}

function ServerRow({ server }: { server: FleetServer }) {
	const stale = server.status !== "online";
	const services = server.services ?? [];
	const diskPercent = percentOf(server.diskUsedBytes, server.diskTotalBytes);
	const hasData = server.memTotalBytes > 0;
	const meter = memoryMeter(server);
	return (
		<section
			aria-label={server.name}
			data-drain-state={server.drainState}
			className={cn(
				"flex flex-col gap-4 p-5",
				server.drainState === "drained" && "opacity-80",
			)}
		>
			<header className="flex flex-col gap-2">
				<div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
					<Link
						to="/servers/$serverId"
						params={{ serverId: server.id }}
						className="flex min-w-0 items-center gap-2.5 text-ink-1 outline-none hover:text-ink-1 focus-visible:focus-ring"
					>
						<span
							aria-hidden
							className="surface-well grid size-8 shrink-0 place-items-center rounded-ctl text-ink-3"
						>
							<ServerIcon className="size-4" />
						</span>
						<span
							title={server.name}
							className="truncate font-display font-semibold text-base leading-[1.15] tracking-[-0.01em]"
						>
							{server.name}
						</span>
					</Link>
					<div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
						<span className="type-mono text-ink-4">
							{server.cpus > 0 ? `${server.cpus} vCPU · ` : ""}
							{services.length} {services.length === 1 ? "service" : "services"}
						</span>
						<DrainChip
							state={server.drainState}
							message={server.drainMessage}
						/>
						<ServerStatus status={server.status} />
					</div>
				</div>
				{server.drainState === "failed" && server.drainMessage ? (
					<Alert variant="destructive">
						<AlertDescription>{server.drainMessage}</AlertDescription>
					</Alert>
				) : null}
				{server.drainState === "draining" ? (
					<p className="text-ink-3 text-xs leading-snug">
						Not accepting new services while its services move away.
					</p>
				) : null}
			</header>

			{hasData ? (
				<div className="flex flex-col gap-3">
					<div className="grid gap-x-6 gap-y-3 sm:grid-cols-2 lg:grid-cols-3">
						<DitherCapacityStat
							label="cpu"
							renderValue={renderKpi}
							percent={server.cpuPercent}
							warnAt={80}
							badAt={95}
							stale={stale}
						/>
						<DitherCapacityStat
							label="memory"
							renderValue={renderKpi}
							percent={meter.usedPercent}
							detail={`${formatBytes(server.memUsedBytes)} / ${formatBytes(server.memTotalBytes)}`}
							stale={stale}
							{...(meter.committedMarkerPercent !== null
								? {
										marker: {
											percent: meter.committedMarkerPercent,
											title: `Committed limits: ${formatBytes(server.committedMemBytes)}`,
											warn: meter.overcommitted,
										},
									}
								: {})}
						/>
						<DitherCapacityStat
							label="disk"
							renderValue={renderKpi}
							percent={diskPercent}
							detail={`${formatBytes(server.diskUsedBytes)} / ${formatBytes(server.diskTotalBytes)}`}
							stale={stale}
						/>
					</div>
					<p className="type-mono text-ink-3 leading-snug">
						{server.committedMemBytes > 0
							? `Limits promise ${formatBytes(server.committedMemBytes)}`
							: "No memory limits set"}
						{server.unlimitedServices > 0
							? ` · ${server.unlimitedServices} without a limit`
							: ""}
					</p>
					{meter.overcommitted ? (
						<Alert variant="warning">
							<AlertDescription>
								<span className="font-medium">Overcommitted. </span>
								{overcommitText(server, formatBytes)}
							</AlertDescription>
						</Alert>
					) : null}
				</div>
			) : (
				<p className="text-ink-3 text-xs leading-snug">
					{server.status === "pending"
						? "Waiting for the agent; capacity appears once it connects."
						: "No heartbeat has been recorded yet."}
				</p>
			)}

			{services.length > 0 ? (
				<ul className="rule-rows -mx-3">
					{services.map((service) => (
						<ServiceRow key={service.id} service={service} stale={stale} />
					))}
				</ul>
			) : (
				<p className="rounded-ctl border border-line-strong border-dashed px-3 py-3 text-center text-ink-3 text-xs">
					No services placed on this server.
				</p>
			)}
		</section>
	);
}

/**
 * The fleet: totals, then one raised panel with a row per server: its
 * capacity (CPU, memory with the committed-limits marker, disk) and the
 * services placed on it. A click on a service opens it on its canvas.
 */
export function FleetView() {
	const fleet = useQuery(fleetQuery());
	const data = fleet.data;

	if (fleet.isPending) {
		return (
			<div
				className="flex flex-col gap-5"
				role="status"
				aria-label="Loading fleet"
			>
				<Skeleton className="h-[104px] rounded-card" />
				<Skeleton className="h-[320px] rounded-card" />
			</div>
		);
	}
	if (!data) {
		return (
			<ErrorState
				frame="raised"
				title="The fleet did not load"
				body="The control plane did not answer. Nothing was changed; check that it is running, then try again."
				retry={() => void fleet.refetch()}
			/>
		);
	}
	if (data.servers.length === 0) {
		return (
			<EmptyState
				illustration="rack"
				frame="none"
				size="lg"
				title="No servers yet"
				body="Capacity and placement show up here once a server is added."
				action={
					<Button
						variant="primary"
						nativeButton={false}
						render={<Link to="/servers" search={{ enroll: true }} />}
					>
						<PlusIcon />
						Add server
					</Button>
				}
				secondaryAction={
					<DocsButton path="operations/servers.md">How servers work</DocsButton>
				}
				hint={
					<>
						<Kbd>⌘K</Kbd> then “Add server” works from any page
					</>
				}
			/>
		);
	}
	return (
		<div className="flex flex-col gap-5">
			<Totals data={data} />
			<div className="surface-raised divide-y divide-line overflow-hidden rounded-card">
				{visibleFleetServers(data.servers).map((server) => (
					<ServerRow key={server.id} server={server} />
				))}
			</div>
		</div>
	);
}
