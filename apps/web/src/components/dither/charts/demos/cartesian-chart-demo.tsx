import {
	type CartesianContext,
	type CartesianSeries,
	DitherCartesianChart,
} from "@nebulactrl/dither/charts/cartesian-chart";
import {
	DitherAreaLayer,
	DitherBarLayer,
	DitherLineLayer,
} from "@nebulactrl/dither/charts/cartesian-layers";
import type { ChartBloom, FillVariant } from "@nebulactrl/dither/charts/shared";
import { useCallback, useId, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { Slider } from "#/components/ui/slider";

interface Row {
	month: string;
	revenue: number;
	forecast: number;
	target: number;
}

const MONTHS = [
	"Jan",
	"Feb",
	"Mar",
	"Apr",
	"May",
	"Jun",
	"Jul",
	"Aug",
	"Sep",
	"Oct",
	"Nov",
	"Dec",
];

/** Monthly revenue in $k: bars for actuals, an area for the forecast, a dashed target. */
const ROWS: readonly Row[] = MONTHS.map((month, i) => ({
	month,
	revenue: Math.round(48 + i * 6 + 9 * Math.sin(i * 1.4)),
	forecast: Math.round(44 + i * 6.4 + 4 * Math.cos(i * 0.9)),
	target: 52 + i * 5.5,
}));

const SERIES: readonly [CartesianSeries<Row>, ...CartesianSeries<Row>[]] = [
	{ label: "Revenue", value: "revenue", color: "blue" },
	{ label: "Forecast", value: "forecast", color: "purple" },
	{ label: "Target", value: "target", color: "orange", dashed: true },
];

const FILLS: { value: FillVariant; label: string }[] = [
	{ value: "gradient", label: "Gradient" },
	{ value: "dotted", label: "Dotted" },
	{ value: "hatched", label: "Hatched" },
	{ value: "solid", label: "Solid" },
];

const BLOOMS: { value: ChartBloom; label: string }[] = [
	{ value: "off", label: "Bloom off" },
	{ value: "low", label: "Low" },
	{ value: "high", label: "High" },
];

const money = (value: number) => `$${Math.round(value)}k`;

export function CartesianChartDemo() {
	const [fill, setFill] = useState<FillVariant>("gradient");
	const [bloom, setBloom] = useState<ChartBloom>("off");
	const [crosshair, setCrosshair] = useState<"line" | "column">("column");
	const [cell, setCell] = useState(3);
	const cellLabelId = useId();

	// Stable by knob, so scrubbing never redraws the layers.
	const layers = useCallback(
		(ctx: CartesianContext) => (
			<>
				<DitherAreaLayer ctx={ctx} index={1} fill={fill} curve="smooth" />
				<DitherBarLayer
					ctx={ctx}
					index={0}
					fill={fill}
					highlight="last"
					highlightLabel={(i) => money(ctx.values[0]?.[i] ?? 0)}
				/>
				<DitherLineLayer ctx={ctx} index={2} dashed markers="none" />
			</>
		),
		[fill],
	);

	return (
		<section className="surface-raised flex min-w-0 flex-col gap-4 rounded-card p-4">
			<h3 className="type-micro">
				Cartesian chart · bars, area and line on one frame
			</h3>
			<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
				<Segmented
					aria-label="Fill"
					value={fill}
					onValueChange={setFill}
					options={FILLS}
				/>
				<Segmented
					aria-label="Bloom"
					value={bloom}
					onValueChange={setBloom}
					options={BLOOMS}
				/>
				<Segmented
					aria-label="Crosshair"
					value={crosshair}
					onValueChange={setCrosshair}
					options={[
						{ value: "column", label: "Column" },
						{ value: "line", label: "Line" },
					]}
				/>
				<div className="flex items-center gap-2 font-mono text-2xs text-ink-2">
					<span id={cellLabelId}>Cell {cell}px</span>
					<Slider
						aria-labelledby={cellLabelId}
						className="w-28"
						min={1}
						max={5}
						step={1}
						value={cell}
						onValueChange={setCell}
					/>
				</div>
			</div>
			<DitherCartesianChart
				label="Monthly revenue against forecast and target"
				data={ROWS}
				x="month"
				series={SERIES}
				layout="band"
				crosshair={crosshair}
				bloom={bloom}
				cell={cell}
				height={260}
				format={money}
			>
				{layers}
			</DitherCartesianChart>
		</section>
	);
}
