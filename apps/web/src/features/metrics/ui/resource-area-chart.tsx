import { DitherAreaShape } from "@nebulactrl/dither/charts/recharts-shape";
import {
	Area,
	AreaChart,
	CartesianGrid,
	ReferenceLine,
	Tooltip,
	YAxis,
} from "recharts";
import { type ChartConfig, ChartContainer } from "#/components/ui/chart";
import { syncByTimestamp } from "#/components/ui/chart-time";
import {
	CURSOR_STROKE,
	type DeployMarker,
	deployMarkerLines,
	GRID_STROKE,
	LIMIT_CROSSED_FILL,
	LIMIT_CROSSED_STROKE,
	LIMIT_STROKE,
	TICK_FILL,
	TICK_STYLE,
	type TimeDomain,
	TimeTooltip,
	type TooltipRow,
	timeXAxis,
} from "./time-chart";

interface LimitLine {
	y: number;
	label: string;
	/** The series reached the line: it turns amber and says so in its label. */
	crossed: boolean;
}

export interface ResourceAreaChartProps<Row extends { t: number }> {
	rows: readonly Row[];
	dataKey: Extract<keyof Row, string>;
	/** Series name for the tooltip and the chart's accessible label. */
	name: string;
	/** A token reference such as `var(--color-accent-ink)`. */
	color: string;
	domain: TimeDomain;
	format: (value: number) => string;
	axisFormat?: (value: number) => string;
	/** Upper bound the axis must at least reach (a limit line, 100 %). */
	minMax?: number;
	/** Fix the axis to [0, max] regardless of data (percent of total). */
	fixedMax?: number;
	limit?: LimitLine | undefined;
	markers?: readonly DeployMarker[];
	/** Charts of one group share the hovered instant. */
	syncId: string;
	/** Extra tooltip rows beyond the series itself (peak values). */
	extraRows?: readonly TooltipRow[];
	height?: number;
}

/**
 * One resource series as an area: grid, shared time axis, crosshair tooltip
 * with local + UTC time, optional dashed limit line and deployment markers.
 * Animations are off on purpose: live charts refetch every few seconds and
 * redrawing from zero each time would read as flicker.
 */
export function ResourceAreaChart<Row extends { t: number }>({
	rows,
	dataKey,
	name,
	color,
	domain,
	format,
	axisFormat,
	minMax,
	fixedMax,
	limit,
	markers = [],
	syncId,
	extraRows = [],
	height = 168,
}: ResourceAreaChartProps<Row>) {
	const config: ChartConfig = { [dataKey]: { label: name, color } };
	const tooltipRows: TooltipRow[] = [
		{ key: dataKey, label: name, color, format },
		...extraRows,
	];
	const limitColor = limit?.crossed ? LIMIT_CROSSED_STROKE : LIMIT_STROKE;
	return (
		<ChartContainer
			config={config}
			role="img"
			aria-label={`${name} over time`}
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
					width={46}
					tickLine={false}
					axisLine={false}
					tick={TICK_STYLE}
					tickFormatter={axisFormat ?? format}
					domain={
						fixedMax !== undefined
							? [0, fixedMax]
							: [
									0,
									(dataMax: number) => Math.max(dataMax, minMax ?? 0, 1) * 1.08,
								]
					}
					allowDecimals={false}
				/>
				<Tooltip
					cursor={{ stroke: CURSOR_STROKE, strokeWidth: 1 }}
					isAnimationActive={false}
					content={(props) => (
						<TimeTooltip {...props} rows={tooltipRows} domain={domain} />
					)}
				/>
				<Area
					type="monotone"
					dataKey={dataKey}
					stroke={color}
					strokeWidth={1.5}
					shape={<DitherAreaShape color={color} />}
					dot={false}
					activeDot={{
						r: 3.5,
						strokeWidth: 2,
						stroke: "var(--dither-paper)",
						fill: color,
					}}
					isAnimationActive={false}
					connectNulls={false}
				/>
				{limit ? (
					<ReferenceLine
						y={limit.y}
						stroke={limitColor}
						strokeDasharray="5 4"
						ifOverflow="extendDomain"
						label={{
							value: limit.crossed ? `${limit.label} — reached` : limit.label,
							position: "insideTopLeft",
							fill: limit.crossed ? LIMIT_CROSSED_FILL : TICK_FILL,
							fontFamily: "var(--font-mono)",
							fontSize: 10,
						}}
					/>
				) : null}
				{deployMarkerLines(markers, domain)}
			</AreaChart>
		</ChartContainer>
	);
}
