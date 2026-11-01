import { DitherAreaShape } from "@nebulactrl/dither/charts/recharts-shape";
import { useState } from "react";
import { Area, AreaChart, CartesianGrid, Tooltip, YAxis } from "recharts";
import { type ChartConfig, ChartContainer } from "#/components/ui/chart";
import { syncByTimestamp } from "#/components/ui/chart-time";
import { Toggle } from "#/components/ui/toggle";
import { compactNumber } from "#/features/metrics/chart-format";
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

export const STATUS_CLASSES = [
	{
		key: "s2xx",
		label: "2xx",
		name: "Success",
		color: "var(--color-status-live)",
	},
	{
		key: "s3xx",
		label: "3xx",
		name: "Redirect",
		color: "var(--color-info)",
	},
	{
		key: "s4xx",
		label: "4xx",
		name: "Client error",
		color: "var(--color-warn)",
	},
	{
		key: "s5xx",
		label: "5xx",
		name: "Server error",
		color: "var(--color-bad)",
	},
] as const;

type StatusKey = (typeof STATUS_CLASSES)[number]["key"];

export interface StatusAreaChartProps {
	rows: readonly SeriesRow[];
	domain: TimeDomain;
	markers?: readonly DeployMarker[];
	syncId: string;
	height?: number;
}

/**
 * Requests per bucket stacked by status class. The legend doubles as the
 * toggles: each chip names its class and shows its total in text, so colour
 * is never the only cue, and pressing one hides that class.
 */
export function StatusAreaChart({
	rows,
	domain,
	markers = [],
	syncId,
	height = 190,
}: StatusAreaChartProps) {
	const [hidden, setHidden] = useState<ReadonlySet<StatusKey>>(new Set());
	const config: ChartConfig = Object.fromEntries(
		STATUS_CLASSES.map((c) => [c.key, { label: c.label, color: c.color }]),
	);
	const totals = STATUS_CLASSES.map((c) => ({
		...c,
		total: rows.reduce((sum, row) => sum + row[c.key], 0),
	}));
	const tooltipRows: TooltipRow[] = STATUS_CLASSES.filter(
		(c) => !hidden.has(c.key),
	).map((c) => ({
		key: c.key,
		label: `${c.label} ${c.name.toLowerCase()}`,
		color: c.color,
		format: (v) => v.toLocaleString(),
	}));

	const toggle = (key: StatusKey) =>
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
				aria-label="Status classes"
			>
				{totals.map((c) => {
					const off = hidden.has(c.key);
					return (
						<li key={c.key}>
							<Toggle
								variant="outline"
								size="sm"
								pressed={!off}
								onPressedChange={() => toggle(c.key)}
								title={off ? `Show ${c.label}` : `Hide ${c.label}`}
								className="gap-1.5 font-mono text-2xs"
							>
								<span
									aria-hidden
									className="size-2 rounded-[2px]"
									style={{
										backgroundColor: off ? "transparent" : c.color,
										boxShadow: `inset 0 0 0 1px ${c.color}`,
									}}
								/>
								<span className="font-medium">{c.label}</span>
								<span className="text-ink-3 tabular-nums">
									{compactNumber(c.total)}
								</span>
							</Toggle>
						</li>
					);
				})}
			</ul>
			<ChartContainer
				config={config}
				role="img"
				aria-label="Requests by status class over time"
				className="aspect-auto w-full"
				style={{ height }}
			>
				<AreaChart
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
						width={40}
						tickLine={false}
						axisLine={false}
						allowDecimals={false}
						tick={TICK_STYLE}
						tickFormatter={(v: number) => compactNumber(v)}
						domain={[0, (max: number) => Math.max(max, 4)]}
					/>
					<Tooltip
						cursor={{ stroke: CURSOR_STROKE, strokeWidth: 1 }}
						isAnimationActive={false}
						content={(props) => (
							<TimeTooltip {...props} rows={tooltipRows} domain={domain} />
						)}
					/>
					{STATUS_CLASSES.filter((c) => !hidden.has(c.key)).map((c) => (
						<Area
							key={c.key}
							type="monotone"
							dataKey={c.key}
							stackId="status"
							stroke={c.color}
							strokeWidth={1}
							shape={<DitherAreaShape color={c.color} level={0.8} bands={6} />}
							dot={false}
							isAnimationActive={false}
						/>
					))}
					{deployMarkerLines(markers, domain)}
				</AreaChart>
			</ChartContainer>
		</div>
	);
}
