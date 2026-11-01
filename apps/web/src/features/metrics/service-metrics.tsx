import { useQuery } from "@tanstack/react-query";
import { useId, useMemo, useState } from "react";
import { deploymentsQuery, serviceQuery } from "#/api/hooks";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import {
	hasProcesses,
	processFilterOptions,
	processReplicaLabel,
	resolveProcessFilter,
} from "#/features/processes/model";
import { useProcessParam } from "#/features/processes/url";
import { componentLabel, stackComponents } from "#/features/stacks/model";
import { useStackNavigation } from "#/features/stacks/url";
import { formatMegabytes } from "#/lib/format";
import { deploymentMarkers } from "./deploy-markers";
import { MetricsNote, metaOf } from "./metrics-note";
import {
	breakGaps,
	hasPeaks,
	isDelayed,
	latest,
	latestReplicas,
	medianStepMs,
	processResources,
	replicaBars,
	type ServiceChartPoint,
	showReplicaList,
	toServicePoints,
	visibleReplicas,
} from "./model";
import { serviceMetricsQuery } from "./queries";
import {
	isLiveRange,
	isMetricRange,
	METRIC_RANGES,
	type MetricRange,
} from "./ranges";
import { ChartCard } from "./ui/chart-card";
import { EYEBROW } from "./ui/eyebrow";
import { MiniStat } from "./ui/mini-stat";
import { ProcessSelect } from "./ui/process-select";
import { RangeControl } from "./ui/range-control";
import { ReplicaFilter, ReplicaList } from "./ui/replica-panel";
import { ResourceAreaChart } from "./ui/resource-area-chart";
import { useChartDomain } from "./use-chart-domain";
import { usePersisted } from "./use-persisted";

type MemoryUnit = "mb" | "pct";
const isMemoryUnit = (value: unknown): value is MemoryUnit =>
	value === "mb" || value === "pct";

type CpuView = "total" | "perReplica";

const NO_LIMIT_TITLE = "Set a memory limit to scale by percent.";
const NO_LIMIT_HINT = ` ${NO_LIMIT_TITLE}`;

const blank = (t: number): ServiceChartPoint => ({
	t,
	cpu: null,
	cpuMax: null,
	cpuPerReplica: null,
	replicaCount: 0,
	memMb: null,
	memMaxMb: null,
	limitMb: null,
	memPct: null,
});

/**
 * The service Metrics tab: CPU % and memory with limit lines and deploys.
 * With several replicas, CPU and memory are the sum over them; the CPU chart
 * can switch to the per-replica average, and the newest sample's per-replica
 * usage is listed under it.
 */
export function ServiceMetrics({ serviceId }: { serviceId: string }) {
	const [range, setRange] = usePersisted<MetricRange>(
		"metrics-range",
		"1h",
		isMetricRange,
	);
	const [unit, setUnit] = usePersisted<MemoryUnit>(
		"metrics-memory-unit",
		"mb",
		isMemoryUnit,
	);
	const [cpuView, setCpuView] = useState<CpuView>("total");
	const [picked, setPicked] = useState<ReadonlySet<number>>(new Set());
	const syncId = useId();
	const service = useQuery(serviceQuery(serviceId));
	const processes = service.data ? processFilterOptions(service.data) : [];
	const pickedProcess = useProcessParam();
	// A stale ?process= (renamed or removed) falls back to the service total;
	// until the service has loaded the address is taken as it is.
	const process = service.data
		? resolveProcessFilter(processes, pickedProcess)
		: pickedProcess;
	const { show } = useStackNavigation();
	const metrics = useQuery(serviceMetricsQuery(serviceId, range, process));
	const deployments = useQuery(deploymentsQuery(serviceId));
	const meta = metaOf(metrics.data);

	const base = useMemo(
		() => toServicePoints(metrics.data?.points),
		[metrics.data],
	);
	const stepMs = meta.stepSeconds
		? meta.stepSeconds * 1000
		: medianStepMs(base) || 10_000;
	const rows = useMemo(() => breakGaps(base, stepMs, blank), [base, stepMs]);
	const domain = useChartDomain(range, metrics.dataUpdatedAt, stepMs / 1000);
	const markers = useMemo(
		() => deploymentMarkers(deployments.data),
		[deployments.data],
	);
	const replicas = useMemo(
		() => latestReplicas(metrics.data?.points),
		[metrics.data],
	);

	const live = isLiveRange("metrics", range);
	const newest = latest(base);
	const delayed = isDelayed(
		newest?.t,
		stepMs,
		metrics.dataUpdatedAt || Date.now(),
	);

	const replicaCount = Math.max(newest?.replicaCount ?? 1, 1);
	const multi = replicaCount > 1 || replicas.length > 1;
	const components = service.data ? stackComponents(service.data) : [];
	const stack = components.length > 0;
	const withProcesses = service.data ? hasProcesses(service.data) : false;
	// Replicas are numbered inside their process; the total of several
	// processes mixes those numberings, so it lists none.
	const processTotal = withProcesses && process === undefined;
	let labelFor: ((index: number) => string) | undefined;
	if (stack) labelFor = (index) => componentLabel(components, index);
	else if (process !== undefined) {
		labelFor = (index) => processReplicaLabel(process, index);
	}
	const processViews = service.data?.processes ?? [];
	const scaling = withProcesses
		? process === undefined
			? {
					ready: processViews.reduce((sum, p) => sum + p.scaling.ready, 0),
					replicas: processViews.reduce(
						(sum, p) => sum + p.scaling.replicas,
						0,
					),
				}
			: processViews.find((p) => p.name === process)?.scaling
		: service.data?.scaling;
	const resources = service.data
		? processResources(service.data.config, process)
		: { memoryMb: undefined, cpus: undefined };

	// The limit comes from the samples (what the containers actually run
	// with, summed over the replicas) and falls back to the configuration,
	// which is per container.
	const limitMb =
		newest?.limitMb ??
		(resources.memoryMb ? resources.memoryMb * replicaCount : null);
	const memLimitPerReplicaMb = limitMb ? limitMb / replicaCount : null;
	const quotaPerReplica = resources.cpus ? resources.cpus * 100 : null;
	const perReplicaView = multi && cpuView === "perReplica";
	const cpuQuota = quotaPerReplica
		? perReplicaView
			? quotaPerReplica
			: quotaPerReplica * replicaCount
		: null;
	const cpuKey = perReplicaView ? "cpuPerReplica" : "cpu";
	const memMax = base.reduce(
		(max, p) => Math.max(max, p.memMaxMb ?? p.memMb ?? 0),
		0,
	);
	const cpuPeak = base.reduce(
		(max, p) =>
			Math.max(
				max,
				perReplicaView ? (p.cpuPerReplica ?? 0) : (p.cpuMax ?? p.cpu ?? 0),
			),
		0,
	);
	const showPct = unit === "pct" && limitMb !== null;

	const bars = replicaBars(
		visibleReplicas(replicas, picked),
		quotaPerReplica ?? 100,
		memLimitPerReplicaMb,
	);

	let body: React.ReactNode;
	if (metrics.isError && !metrics.data) {
		body = (
			<ErrorState
				title="Metrics did not load"
				body="The control plane did not answer. Try again in a moment."
				retry={() => void metrics.refetch()}
			/>
		);
	} else if (base.length === 0) {
		body = (
			<EmptyState
				illustration="radar"
				frame="none"
				className="surface-well rounded-card py-10"
				title="No samples yet"
				body="The container must be running before NebulaCtrl can show its metrics. Samples arrive every 10 seconds once it is deployed and healthy."
			/>
		);
	} else {
		const cpuNow = newest?.cpu ?? 0;
		body = (
			<>
				<div className="grid grid-cols-2 gap-2.5 @xl:grid-cols-3 @3xl:grid-cols-5">
					<MiniStat
						label="CPU now"
						text={`${cpuNow.toFixed(1)}%`}
						hint={
							multi
								? `${(newest?.cpuPerReplica ?? 0).toFixed(1)}% per replica`
								: quotaPerReplica
									? `of ${quotaPerReplica}% quota`
									: "of one core = 100%"
						}
					/>
					<MiniStat
						label="Memory now"
						text={formatMegabytes(newest?.memMb ?? 0)}
						hint={
							limitMb ? `of ${formatMegabytes(limitMb)} limit` : "no limit set"
						}
						tone={
							limitMb && (newest?.memMb ?? 0) >= limitMb * 0.9
								? "warning"
								: "default"
						}
					/>
					<MiniStat
						label={`CPU peak ${range}`}
						text={`${cpuPeak.toFixed(1)}%`}
						hint="highest sample"
					/>
					<MiniStat
						label={`Memory peak ${range}`}
						text={formatMegabytes(memMax)}
						hint={
							limitMb
								? `${Math.round((memMax / limitMb) * 100)}% of limit`
								: "highest sample"
						}
					/>
					<MiniStat
						label="Replicas"
						text={String(replicaCount)}
						hint={
							scaling
								? `${scaling.ready} of ${scaling.replicas} ready`
								: "sampled now"
						}
						tone={
							scaling && scaling.ready < scaling.replicas
								? "warning"
								: "default"
						}
					/>
				</div>
				<ChartCard
					title="CPU"
					subtitle={
						perReplicaView
							? "Average per replica, percent of one core."
							: multi
								? stack
									? "Summed over the components, percent of one core; 200% is two cores busy."
									: processTotal
										? "Summed over the processes, percent of one core; 200% is two cores busy."
										: "Summed over the replicas, percent of one core; 200% is two cores busy."
								: "Percent of one core; 200% is two cores busy."
					}
					actions={
						multi && !stack ? (
							<Segmented
								aria-label="CPU series"
								font="sans"
								value={cpuView}
								onValueChange={setCpuView}
								options={[
									{ value: "total", label: "Total" },
									{ value: "perReplica", label: "Per replica" },
								]}
							/>
						) : null
					}
				>
					<ResourceAreaChart
						rows={rows}
						dataKey={cpuKey}
						name={perReplicaView ? "CPU per replica" : "CPU"}
						color="var(--color-accent-ink)"
						domain={domain}
						format={(v) => `${v.toFixed(1)}%`}
						axisFormat={(v) => `${Math.round(v)}%`}
						minMax={cpuQuota ?? 0}
						limit={
							cpuQuota
								? {
										y: cpuQuota,
										label: `quota ${cpuQuota}%`,
										crossed: cpuPeak >= cpuQuota,
									}
								: undefined
						}
						markers={markers}
						syncId={syncId}
						extraRows={
							!perReplicaView && hasPeaks(rows)
								? [
										{
											key: "cpuMax",
											label: "Peak",
											color: "var(--color-warn)",
											format: (v: number) => `${v.toFixed(1)}%`,
										},
									]
								: []
						}
					/>
					{showReplicaList(withProcesses, process, replicas.length) ? (
						<div className="flex flex-col gap-2.5 border-line-faint border-t pt-3">
							<div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
								<span className={EYEBROW}>
									{stack ? "Per component · now" : "Per replica · now"}
								</span>
								<ReplicaFilter
									replicas={replicas}
									picked={picked}
									onPickedChange={setPicked}
									labelFor={labelFor}
								/>
							</div>
							<ReplicaList
								bars={bars}
								cpuHint={
									quotaPerReplica
										? `of ${quotaPerReplica}% quota`
										: "of one core = 100%"
								}
								memLimitMb={stack ? null : memLimitPerReplicaMb}
								labelFor={labelFor}
							/>
							<p className="text-ink-4 text-xs leading-snug">
								The newest sample only; the API keeps history for the total and
								the per-replica average.
							</p>
						</div>
					) : null}
				</ChartCard>
				<ChartCard
					title="Memory"
					subtitle={
						showPct
							? "Percent of the memory limit (working set, cache excluded)."
							: multi
								? `Working set summed over the ${processTotal ? "processes" : "replicas"}, in MiB; page cache excluded.${limitMb === null ? NO_LIMIT_HINT : ""}`
								: `Working set in MiB, page cache excluded; matches docker stats.${limitMb === null ? NO_LIMIT_HINT : ""}`
					}
					actions={
						<span title={limitMb === null ? NO_LIMIT_TITLE : undefined}>
							<Segmented
								aria-label="Memory unit"
								value={showPct ? "pct" : "mb"}
								onValueChange={setUnit}
								// A disabled segment drops to half opacity, which is 2.8:1.
								// Keep it at the faintest readable ink instead.
								className="[&_button:disabled]:text-ink-4 [&_button:disabled]:opacity-100"
								options={[
									{ value: "mb", label: "MiB" },
									{
										value: "pct",
										label: "% limit",
										disabled: limitMb === null,
									},
								]}
							/>
						</span>
					}
				>
					<ResourceAreaChart
						rows={rows}
						dataKey={showPct ? "memPct" : "memMb"}
						name="Memory"
						color="var(--color-status-live)"
						domain={domain}
						format={showPct ? (v) => `${v.toFixed(0)}%` : formatMegabytes}
						{...(showPct ? { fixedMax: 100 } : { minMax: limitMb ?? 0 })}
						limit={
							!showPct && limitMb
								? {
										y: limitMb,
										label: `limit ${formatMegabytes(limitMb)}`,
										crossed: memMax >= limitMb,
									}
								: undefined
						}
						markers={markers}
						syncId={syncId}
					/>
				</ChartCard>
			</>
		);
	}

	return (
		<div className="@container flex flex-col gap-4">
			<div className="flex flex-wrap items-center justify-between gap-3">
				<RangeControl
					ranges={METRIC_RANGES}
					value={range}
					onChange={setRange}
					live={live && base.length > 0}
					delayed={delayed}
				/>
				{withProcesses ? (
					<ProcessSelect
						processes={processes}
						value={process}
						onChange={(next) => show({ process: next ?? null })}
					/>
				) : null}
			</div>
			<SkeletonReveal
				loading={metrics.isPending}
				className="flex flex-col gap-4"
				skeleton={<MetricsSkeleton />}
			>
				{() => body}
			</SkeletonReveal>
			<MetricsNote meta={meta} range={range} />
		</div>
	);
}

const SKELETON_TILES = ["cpu", "mem", "cpu-peak", "mem-peak", "replicas"];

function MetricsSkeleton() {
	return (
		<div
			className="flex flex-col gap-4"
			role="status"
			aria-label="Loading metrics"
		>
			<div className="grid grid-cols-2 gap-2.5 @xl:grid-cols-3 @3xl:grid-cols-5">
				{SKELETON_TILES.map((id) => (
					<Skeleton key={id} className="h-[84px] rounded-card" />
				))}
			</div>
			<Skeleton className="h-[236px] rounded-card" />
			<Skeleton className="h-[236px] rounded-card" />
		</div>
	);
}
