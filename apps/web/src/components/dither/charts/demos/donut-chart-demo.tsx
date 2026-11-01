import {
	DitherDonutChart,
	type DonutSlice,
} from "@nebulactrl/dither/charts/donut-chart";
import type { ChartBloom, FillVariant } from "@nebulactrl/dither/charts/shared";
import { useId, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { Slider } from "#/components/ui/slider";
import { Switch } from "#/components/ui/switch";

type Variant = "donut" | "pie";

const REGIONS: readonly DonutSlice[] = [
	{ label: "us-east", value: 48200 },
	{ label: "eu-west", value: 31400 },
	{ label: "ap-south", value: 17900 },
	{ label: "us-west", value: 12600 },
	{ label: "sa-east", value: 6100 },
	{ label: "af-south", value: 1900 },
];

const VARIANTS = [
	{ value: "donut", label: "Donut" },
	{ value: "pie", label: "Pie" },
] as const;

export function DonutChartDemo() {
	const [variant, setVariant] = useState<Variant>("donut");
	const [size, setSize] = useState(180);
	const [cell, setCell] = useState(2);
	const [fill, setFill] = useState<FillVariant | "mixed">("mixed");
	const [bloom, setBloom] = useState<ChartBloom>("off");
	const [legend, setLegend] = useState(true);
	const sizeLabelId = useId();
	const cellLabelId = useId();
	const legendId = useId();

	return (
		<div className="surface-raised flex min-w-0 flex-col gap-4 rounded-card p-4">
			<div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
				<span className="type-micro">Requests by region</span>
				<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
					<Segmented
						aria-label="Variant"
						value={variant}
						onValueChange={setVariant}
						options={VARIANTS}
					/>
					<Segmented
						aria-label="Fill"
						value={fill}
						onValueChange={setFill}
						options={[
							{ value: "mixed", label: "Mixed" },
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
						<span id={sizeLabelId}>Size {size}px</span>
						<Slider
							aria-labelledby={sizeLabelId}
							className="w-24"
							min={120}
							max={240}
							step={10}
							value={size}
							onValueChange={setSize}
						/>
					</div>
					<div className="flex items-center gap-2 font-mono text-2xs text-ink-3">
						<span id={cellLabelId}>Cell {cell}px</span>
						<Slider
							aria-labelledby={cellLabelId}
							className="w-20"
							min={1}
							max={4}
							step={0.5}
							value={cell}
							onValueChange={setCell}
						/>
					</div>
					<label
						htmlFor={legendId}
						className="flex items-center gap-2 font-mono text-2xs text-ink-3"
					>
						Legend
						<Switch
							id={legendId}
							size="sm"
							aria-label="Legend"
							checked={legend}
							onCheckedChange={setLegend}
						/>
					</label>
				</div>
			</div>
			<DitherDonutChart
				label="Requests by region"
				data={REGIONS}
				variant={variant}
				size={size}
				cell={cell}
				bloom={bloom}
				{...(fill === "mixed" ? {} : { fill })}
				legend={legend}
				centerLabel="Requests"
			/>
		</div>
	);
}
