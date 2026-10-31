import { DitherMeter } from "@nebulactrl/dither/charts/meter";
import { DitherSparkline } from "@nebulactrl/dither/charts/sparkline";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { cn } from "cn";
import { memo, useMemo } from "react";
import type { Service } from "#/api/models";
import { KpiNumber } from "#/components/common/kpi-number";
import { Badge } from "#/components/ui/badge";
import { StatusIcon } from "#/components/ui/status-icon";
import { latencyText } from "#/features/analytics/latency-text";
import { useTrafficReport } from "#/features/analytics/queries";
import { nodeStatus, sourceLabel } from "#/features/canvas/lib/status";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { compactNumber, formatRate } from "#/features/metrics/chart-format";
import { toServicePoints } from "#/features/metrics/model";
import { serviceMetricsQuery } from "#/features/metrics/queries";
import { formatMegabytes } from "#/lib/format";
import { cpuTrend } from "./trend";

const WINDOW = "30m" as const;

/** CPU amber from 80%, red from 95%: the same rule as the fleet meters. */
const WARN_AT = 80;
const BAD_AT = 95;

/** The sparkline's readout: CPU as whole percent of one core. */
const CPU_FORMAT = (value: number) => `${Math.round(value)}%`;

/** Ready over desired replicas, for a service that has a release to run. */
function ReplicaBadge({ service }: { service: Service }) {
	if (service.currentDeploymentId === null) return null;
	const { ready, replicas } = service.scaling;
	const short = ready < replicas;
	return (
		<Badge
			tone={short ? "warn" : "ok"}
			shape="capsule"
			title={short ? "Some replicas are not ready" : "All replicas are ready"}
			// The label is the chip's whole point: it is never squeezed or cut
			// (a card too narrow for it and the name wraps the chip below).
			className="min-w-max shrink-0 overflow-visible"
		>
			<StatusIcon kind={short ? "warning" : "healthy"} label="" />
			{ready}/{replicas} {replicas === 1 ? "replica" : "replicas"}
			{short ? <span className="sr-only"> ready; some are not</span> : null}
		</Badge>
	);
}

/** A big tabular number over a 10px mono label. */
function Kpi({ label, value }: { label: string; value: string }) {
	return (
		<div className="flex min-w-0 flex-col gap-1.5">
			<span className="type-metric truncate text-ink-1 leading-none">
				<KpiNumber text={value} />
			</span>
			<span className="type-micro">{label}</span>
		</div>
	);
}

const ServiceResourceCard = memo(function ServiceResourceCard({
	service,
	projectId,
	envSlug,
}: {
	service: Service;
	projectId: string;
	envSlug: string;
}) {
	const status = nodeStatus(service);
	const hasDomain = service.domains.length > 0;
	const metrics = useQuery({
		...serviceMetricsQuery(service.id, WINDOW),
		enabled: service.observed !== null,
	});
	const traffic = useTrafficReport(
		{ serviceId: service.id },
		{ range: "1h", compare: false, host: null, pagesOnly: false },
		hasDomain,
	);
	const points = useMemo(
		() => toServicePoints(metrics.data?.points),
		[metrics.data],
	);
	const newest = points.at(-1);
	const trend = useMemo(() => cpuTrend(points), [points]);
	const summary = traffic.data?.summary;
	const stale = service.stale;

	return (
		<div className="surface-raised flex min-w-0 flex-col gap-4 rounded-card p-4">
			<div className="flex flex-wrap items-start justify-between gap-x-3 gap-y-2">
				<div className="flex min-w-0 flex-[1_1_12rem] items-center gap-2.5">
					<ServiceIcon service={service} size={28} />
					<div className="flex min-w-0 flex-col gap-1">
						<Link
							to="/p/$projectId/$envSlug"
							params={{ projectId, envSlug }}
							search={{ service: service.id }}
							className="truncate font-semibold text-ink-1 text-sm leading-none outline-none hover:text-accent-ink focus-visible:focus-ring"
						>
							{service.name}
						</Link>
						<span className="truncate font-mono text-2xs text-ink-4">
							{sourceLabel(service)}
							{service.server ? ` · ${service.server.name}` : ""}
						</span>
					</div>
				</div>
				<ReplicaBadge service={service} />
			</div>
			<p className="flex items-center gap-2 text-ink-2 text-xs leading-snug">
				<StatusIcon kind={status.kind} label="" />
				<span className="min-w-0 truncate" title={status.label}>
					{status.label}
				</span>
			</p>
			<div className="flex flex-col gap-3">
				<DitherMeter
					label="CPU"
					percent={newest?.cpu ?? null}
					detail={newest ? `last ${WINDOW}` : undefined}
					warnAt={WARN_AT}
					badAt={BAD_AT}
					stale={stale}
				/>
				{trend ? (
					<DitherSparkline
						data={trend.values}
						label={`CPU of ${service.name}, last ${WINDOW}`}
						height={32}
						domain={trend.domain}
						format={CPU_FORMAT}
						className={cn("w-full", stale && "opacity-50")}
					/>
				) : null}
				{newest?.limitMb ? (
					<DitherMeter
						label="Memory"
						percent={newest.memPct}
						detail={`${formatMegabytes(newest.memMb ?? 0)} / ${formatMegabytes(newest.limitMb)}`}
						warnAt={WARN_AT}
						badAt={BAD_AT}
						stale={stale}
					/>
				) : (
					<div
						className={cn(
							"flex items-baseline justify-between gap-2 text-xs leading-none",
							stale && "opacity-50",
						)}
					>
						<span className="type-label">Memory</span>
						<span className="font-mono text-ink-1 tabular-nums">
							{newest ? formatMegabytes(newest.memMb ?? 0) : "–"}
							{newest ? <span className="text-ink-4"> · no limit</span> : null}
						</span>
					</div>
				)}
			</div>
			{hasDomain && summary ? (
				<div className="grid grid-cols-3 gap-3 border-line-faint border-t pt-3">
					<Kpi label="req / h" value={compactNumber(summary.requests)} />
					<Kpi label="errors" value={formatRate(summary.errorRate)} />
					<Kpi
						label="p95"
						value={latencyText(summary.requests, summary.p95Ms)}
					/>
				</div>
			) : (
				<p className="border-line-faint border-t pt-3 font-mono text-2xs text-ink-3">
					{!hasDomain
						? "No domain, so no traffic is collected"
						: traffic.isPending
							? "Loading traffic…"
							: "Traffic unavailable"}
				</p>
			)}
		</div>
	);
});

/** One resource + traffic mini-card per service in the environment. */
export function ServiceResourceCards({
	services,
	projectId,
	envSlug,
}: {
	services: readonly Service[];
	projectId: string;
	envSlug: string;
}) {
	return (
		<div className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,300px),1fr))] gap-4">
			{services.map((service) => (
				<ServiceResourceCard
					key={service.id}
					service={service}
					projectId={projectId}
					envSlug={envSlug}
				/>
			))}
		</div>
	);
}
