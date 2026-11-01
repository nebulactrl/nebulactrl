import { useState } from "react";
import { CartesianGrid, Line, LineChart, Tooltip, YAxis } from "recharts";
import { type ChartConfig, ChartContainer } from "#/components/ui/chart";
import { syncByTimestamp } from "#/components/ui/chart-time";
import { Toggle } from "#/components/ui/toggle";
import { formatMs } from "#/features/metrics/chart-format";
import {
	CURSOR_STROKE,
	type DeployMarker,
	deployMarkerLines,
	GRID_STROKE,
	TICK_STYLE,
	type TimeDomain,
	TimeTooltip,
	type TooltipRow,
	timeXAxis,
} from "#/features/metrics/ui/time-chart";
import type { SeriesRow } from "../series";

const PERCENTILES = [
	{ key: "p50Ms", label: "p50", color: "var(--color-status-live)" },
	{ key: "p95Ms", label: "p95", color: "var(--color-warn)" },
	{ key: "p99Ms", label: "p99", color: "var(--color-bad)" },
] as const;

type PercentileKey = (typeof PERCENTILES)[number]["key"];

export interface LatencyChartProps {
	rows: readonly SeriesRow[];
	domain: TimeDomain;
	markers?: readonly DeployMarker[];
	syncId: string;
	height?: number;
}

/** p50 / p95 / p99 response time per bucket, with toggle chips that show or hide each line. */
export function LatencyChart({
	rows,
	domain,
	markers = [],
	syncId,
	height = 170,
}: LatencyChartProps) {
	const [hidden, setHidden] = useState<ReadonlySet<PercentileKey>>(new Set());
	const config: ChartConfig = Object.fromEntries(
		PERCENTILES.map((p) => [p.key, { label: p.label, color: p.color }]),
	);
	const tooltipRows: TooltipRow[] = PERCENTILES.filter(
		(p) => !hidden.has(p.key),
	).map((p) => ({
		key: p.key,
		label: p.label,
		color: p.color,
		format: formatMs,
	}));

	const toggle = (key: PercentileKey) =>
		setHidden((current) => {
			const next = new Set(current);
			if (next.has(key)) next.delete(key);
			else next.add(key);
			return next;
		});

	return (
		<div className="flex flex-col gap-2.5">
			<ul
				className="flex flex-wrap items-center gap-1.5"
				aria-label="Percentiles"
			>
				{PERCENTILES.map((p) => {
					const off = hidden.has(p.key);
					return (
						<li key={p.key}>
							<Toggle
								variant="outline"
								size="sm"
								pressed={!off}
								onPressedChange={() => toggle(p.key)}
								title={off ? `Show ${p.label}` : `Hide ${p.label}`}
								className="gap-1.5 font-mono text-2xs"
							>
								<span
									aria-hidden
									className="h-0.5 w-3 rounded-full"
									style={{
										backgroundColor: off ? "transparent" : p.color,
										boxShadow: `inset 0 0 0 1px ${p.color}`,
									}}
								/>
								{p.label}
							</Toggle>
						</li>
					);
				})}
			</ul>
			<ChartContainer
				config={config}
				role="img"
				aria-label="Response time percentiles over time"
				className="aspect-auto w-full"
				style={{ height }}
			>
				<LineChart
					data={[...rows]}
					syncId={syncId}
					syncMethod={syncByTimestamp}
					margin={{ top: 10, right: 10, left: 0, bottom: 0 }}
				>
					<CartesianGrid
						vertical={false}
						stroke={GRID_STROKE}
						strokeDasharray="3 3"
					/>
					{timeXAxis(domain)}
					<YAxis
						width={52}
						tickLine={false}
						axisLine={false}
						tick={TICK_STYLE}
						tickFormatter={(v: number) => (v === 0 ? "0" : formatMs(v))}
						domain={[0, (max: number) => Math.max(max * 1.1, 10)]}
					/>
					<Tooltip
						cursor={{ stroke: CURSOR_STROKE, strokeWidth: 1 }}
						isAnimationActive={false}
						content={(props) => (
							<TimeTooltip {...props} rows={tooltipRows} domain={domain} />
						)}
					/>
					{PERCENTILES.filter((p) => !hidden.has(p.key)).map((p) => (
						<Line
							key={p.key}
							type="monotone"
							dataKey={p.key}
							stroke={p.color}
							strokeWidth={1.5}
							dot={false}
							activeDot={{
								r: 3.5,
								strokeWidth: 2,
								stroke: "var(--color-surface-raised)",
								fill: p.color,
							}}
							isAnimationActive={false}
							connectNulls={false}
						/>
					))}
					{deployMarkerLines(markers, domain)}
				</LineChart>
			</ChartContainer>
		</div>
	);
}
