import {
	DitherRadarChart,
	type RadarSeries,
} from "@nebulactrl/dither/charts/radar-chart";
import type { ChartBloom, FillVariant } from "@nebulactrl/dither/charts/shared";
import { useId, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { Slider } from "#/components/ui/slider";

type Grid = "line" | "dither" | "polygon" | "circle";
type Count = "1" | "2" | "3";

const AXES = [
	"Latency",
	"Errors",
	"Saturation",
	"Traffic",
	"Uptime",
	"Cost",
] as const;

/** Scores out of 100: higher is healthier. Staging reads as a record to show both input shapes. */
const SERIES: readonly [RadarSeries, ...RadarSeries[]] = [
	{ label: "Production", values: [82, 91, 64, 88, 97, 58] },
	{
		label: "Staging",
		values: {
			Latency: 70,
			Errors: 76,
			Saturation: 85,
			Traffic: 42,
			Uptime: 90,
			Cost: 80,
		},
	},
	{ label: "Preview", values: [48, 60, 90, 25, 72, 92] },
];

const GRIDS = [
	{ value: "line", label: "Lines" },
	{ value: "dither", label: "Dither" },
	{ value: "circle", label: "Circle" },
] as const;
const COUNTS = [
	{ value: "1", label: "1" },
	{ value: "2", label: "2" },
	{ value: "3", label: "3" },
] as const;

export function RadarChartDemo() {
	const [grid, setGrid] = useState<Grid>("line");
	const [count, setCount] = useState<Count>("2");
	const [rings, setRings] = useState(4);
	const [fill, setFill] = useState<FillVariant | "flat">("flat");
	const [bloom, setBloom] = useState<ChartBloom>("off");
	const ringsLabelId = useId();
	const [first, ...rest] = SERIES;
	const series: readonly [RadarSeries, ...RadarSeries[]] = [
		first,
		...rest.slice(0, Number(count) - 1),
	];

	return (
		<div className="surface-raised flex min-w-0 flex-col gap-4 rounded-card p-4">
			<div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
				<span className="type-micro">Service health</span>
				<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
					<Segmented
						aria-label="Grid"
						value={grid}
						onValueChange={setGrid}
						options={GRIDS}
					/>
					<Segmented
						aria-label="Series"
						value={count}
						onValueChange={setCount}
						options={COUNTS}
					/>
					<Segmented
						aria-label="Fill"
						value={fill}
						onValueChange={setFill}
						options={[
							{ value: "flat", label: "Flat" },
							{ value: "gradient", label: "Gradient" },
							{ value: "dotted", label: "Dotted" },
							{ value: "hatched", label: "Hatched" },
							{ value: "solid", label: "Solid" },
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
					<div className="flex items-center gap-2 font-mono text-2xs text-ink-3">
						<span id={ringsLabelId}>Rings {rings}</span>
						<Slider
							aria-labelledby={ringsLabelId}
							className="w-20"
							min={3}
							max={5}
							step={1}
							value={rings}
							onValueChange={setRings}
						/>
					</div>
				</div>
			</div>
			<DitherRadarChart
				label="Service health by signal"
				axes={AXES}
				series={series}
				max={100}
				rings={rings}
				grid={grid}
				bloom={bloom}
				{...(fill === "flat" ? {} : { fill })}
			/>
		</div>
	);
}
