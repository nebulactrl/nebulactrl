import { DitherAreaChart } from "@nebulactrl/dither/charts/area-chart";
import type {
	ChartBloom,
	ChartSeries,
	DitherMatrix,
	FillVariant,
} from "@nebulactrl/dither/charts/shared";
import { useId, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { Slider } from "#/components/ui/slider";

interface Row {
	hour: string;
	cached: number;
	origin: number;
}

/** Requests per second over a day; deterministic, with a lunchtime peak. */
const ROWS: readonly Row[] = Array.from({ length: 24 }, (_, i) => {
	const day = Math.max(0, Math.sin(((i - 5) / 18) * Math.PI));
	return {
		hour: `${String(i).padStart(2, "0")}:00`,
		cached: Math.round(150 + 360 * day + 24 * Math.sin(i * 1.9)),
		origin: Math.round(48 + 96 * day + 10 * Math.cos(i * 1.3)),
	};
});

const SERIES: readonly [ChartSeries<Row>, ...ChartSeries<Row>[]] = [
	{ label: "Cached", value: "cached", color: "ink" },
	{ label: "Origin", value: "origin", color: "info" },
];

const FILLS: { value: FillVariant; label: string }[] = [
	{ value: "gradient", label: "Gradient" },
	{ value: "dotted", label: "Dotted" },
	{ value: "hatched", label: "Hatched" },
	{ value: "solid", label: "Solid" },
];

export function AreaChartDemo() {
	const [stack, setStack] = useState<"none" | "stacked" | "percent">("none");
	const [fill, setFill] = useState<FillVariant>("gradient");
	const [curve, setCurve] = useState<"monotone" | "linear">("monotone");
	const [cell, setCell] = useState(3);
	const [bloom, setBloom] = useState<ChartBloom>("off");
	const [matrix, setMatrix] = useState<DitherMatrix>("bayer4");
	const cellLabelId = useId();

	return (
		<section className="surface-raised flex min-w-0 flex-col gap-4 rounded-card p-4">
			<h3 className="type-micro">Area chart · requests per second</h3>
			<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
				<Segmented
					aria-label="Stacking"
					value={stack}
					onValueChange={setStack}
					options={[
						{ value: "none", label: "Layered" },
						{ value: "stacked", label: "Stacked" },
						{ value: "percent", label: "100%" },
					]}
				/>
				<Segmented
					aria-label="Fill"
					value={fill}
					onValueChange={setFill}
					options={FILLS}
				/>
				<Segmented
					aria-label="Curve"
					value={curve}
					onValueChange={setCurve}
					options={[
						{ value: "monotone", label: "Smooth" },
						{ value: "linear", label: "Linear" },
					]}
				/>
				<Segmented
					aria-label="Bloom"
					value={bloom}
					onValueChange={setBloom}
					options={[
						{ value: "off", label: "Bloom off" },
						{ value: "low", label: "Low" },
						{ value: "high", label: "High" },
					]}
				/>
				<Segmented
					aria-label="Matrix"
					value={matrix}
					onValueChange={setMatrix}
					options={[
						{ value: "bayer2", label: "B2" },
						{ value: "bayer4", label: "B4" },
						{ value: "bayer8", label: "B8" },
						{ value: "blue-noise", label: "Blue" },
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
			<DitherAreaChart
				label="Requests per second over 24 hours"
				data={ROWS}
				x="hour"
				series={SERIES}
				stack={stack}
				fill={fill}
				curve={curve}
				cell={cell}
				bloom={bloom}
				matrix={matrix}
				height={240}
			/>
		</section>
	);
}
