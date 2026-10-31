import { useQuery } from "@tanstack/react-query";
import { useId, useMemo } from "react";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { KpiNumber } from "#/components/common/kpi-number";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { formatBytes } from "#/lib/format";
import { serverViewQuery } from "../servers/queries";
import { MetricsNote, metaOf } from "./metrics-note";
import {
	breakGaps,
	hasPeaks,
	isDelayed,
	latest,
	medianStepMs,
	type ServerChartPoint,
	toServerPoints,
} from "./model";
import { serverMetricsQuery } from "./queries";
import {
	isLiveRange,
	isMetricRange,
	METRIC_RANGES,
	type MetricRange,
} from "./ranges";
import { ChartCard } from "./ui/chart-card";
import { RangeControl } from "./ui/range-control";
import { ResourceAreaChart } from "./ui/resource-area-chart";
import { useChartDomain } from "./use-chart-domain";
import { usePersisted } from "./use-persisted";

const blank = (t: number): ServerChartPoint => ({
	t,
	cpu: null,
	cpuMax: null,
	memPct: null,
	memMaxPct: null,
	diskPct: null,
	memUsed: 0,
	memTotal: 0,
	diskUsed: 0,
	diskTotal: 0,
});

const pct = (v: number) => `${v.toFixed(1)}%`;

/** The plot's height: three of them share a row. */
const CHART_HEIGHT = 104;

/** The latest value, in the corner of its chart card. */
function NowValue({ text }: { text: string }) {
	return (
		<span className="font-medium font-mono text-ink-1 text-sm tabular-nums">
			<KpiNumber text={text} />
		</span>
	);
}

/**
 * The server's resource charts, small and side by side: CPU % and memory % of
 * the host's total, and disk %. Each card carries its latest value; a disk at
 * or over its alert threshold says so there, in words.
 */
export function ServerMetrics({ serverId }: { serverId: string }) {
	const [range, setRange] = usePersisted<MetricRange>(
		"server-metrics-range",
		"1h",
		isMetricRange,
	);
	const syncId = useId();
	const metrics = useQuery(serverMetricsQuery(serverId, range));
	const server = useQuery(serverViewQuery(serverId));
	const meta = metaOf(metrics.data);

	const base = useMemo(
		() => toServerPoints(metrics.data?.points),
		[metrics.data],
	);
	const stepMs = meta.stepSeconds
		? meta.stepSeconds * 1000
		: medianStepMs(base) || 10_000;
	const rows = useMemo(() => breakGaps(base, stepMs, blank), [base, stepMs]);
	const domain = useChartDomain(range, metrics.dataUpdatedAt, stepMs / 1000);

	const live = isLiveRange("metrics", range);
	const newest = latest(base);
	const delayed = isDelayed(
		newest?.t,
		stepMs,
		metrics.dataUpdatedAt || Date.now(),
	);
	const threshold = server.data?.settings.diskAlertThresholdPercent;
	const diskNow = newest?.diskPct ?? null;
	const diskOver =
		threshold !== undefined && diskNow !== null && diskNow >= threshold;
	const diskPeak = base.reduce((max, p) => Math.max(max, p.diskPct ?? 0), 0);

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
				body="Host samples arrive every 10 seconds while the agent is connected. Nothing has been reported for this range."
			/>
		);
	} else {
		body = (
			<div className="grid gap-2.5 @3xl:grid-cols-3">
				<ChartCard
					title="CPU"
					subtitle="Percent of all cores"
					actions={<NowValue text={pct(newest?.cpu ?? 0)} />}
				>
					<ResourceAreaChart
						rows={rows}
						dataKey="cpu"
						name="CPU"
						color="var(--color-accent-ink)"
						domain={domain}
						format={pct}
						axisFormat={(v) => `${Math.round(v)}%`}
						fixedMax={100}
						syncId={syncId}
						height={CHART_HEIGHT}
						extraRows={
							hasPeaks(rows)
								? [
										{
											key: "cpuMax",
											label: "Peak",
											color: "var(--color-warn)",
											format: pct,
										},
									]
								: []
						}
					/>
				</ChartCard>
				<ChartCard
					title="Memory"
					subtitle={`${formatBytes(newest?.memUsed ?? 0)} of ${formatBytes(newest?.memTotal ?? 0)}`}
					actions={<NowValue text={pct(newest?.memPct ?? 0)} />}
				>
					<ResourceAreaChart
						rows={rows}
						dataKey="memPct"
						name="Memory"
						color="var(--color-status-live)"
						domain={domain}
						format={pct}
						axisFormat={(v) => `${Math.round(v)}%`}
						fixedMax={100}
						syncId={syncId}
						height={CHART_HEIGHT}
					/>
				</ChartCard>
				<ChartCard
					title="Disk"
					subtitle={
						diskOver ? (
							<span className="text-status-staged-on">
								Over the {threshold}% alert
							</span>
						) : (
							`${formatBytes(newest?.diskUsed ?? 0)} of ${formatBytes(newest?.diskTotal ?? 0)}`
						)
					}
					actions={<NowValue text={diskNow === null ? "–" : pct(diskNow)} />}
				>
					<ResourceAreaChart
						rows={rows}
						dataKey="diskPct"
						name="Disk"
						color="var(--color-info)"
						domain={domain}
						format={pct}
						axisFormat={(v) => `${Math.round(v)}%`}
						fixedMax={100}
						limit={
							threshold === undefined
								? undefined
								: {
										y: threshold,
										label: `alert ${threshold}%`,
										crossed: diskPeak >= threshold,
									}
						}
						syncId={syncId}
						height={CHART_HEIGHT}
					/>
				</ChartCard>
			</div>
		);
	}

	return (
		<div className="@container flex flex-col gap-3">
			<div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
				<RangeControl
					ranges={METRIC_RANGES}
					value={range}
					onChange={setRange}
					live={live && base.length > 0}
					delayed={delayed}
				/>
				<MetricsNote meta={meta} range={range} />
			</div>
			<SkeletonReveal
				loading={metrics.isPending}
				skeleton={
					<div
						className="grid gap-2.5 @3xl:grid-cols-3"
						role="status"
						aria-label="Loading metrics"
					>
						{["cpu", "memory", "disk"].map((id) => (
							<Skeleton key={id} className="h-[196px] rounded-card" />
						))}
					</div>
				}
			>
				{() => body}
			</SkeletonReveal>
		</div>
	);
}
