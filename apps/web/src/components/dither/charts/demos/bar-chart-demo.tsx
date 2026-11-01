import { DitherBarChart } from "@nebulactrl/dither/charts/bar-chart";
import type {
	ChartBloom,
	ChartSeries,
	DitherMatrix,
	FillVariant,
} from "@nebulactrl/dither/charts/shared";
import { useId, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { Slider } from "#/components/ui/slider";
import { Switch } from "#/components/ui/switch";

interface Row {
	hour: string;
	ok: number;
	redirect: number;
	client: number;
	server: number;
}

/** Requests per hour by status class; deterministic, with a 5xx spike at 17:00. */
const ROWS: readonly Row[] = Array.from({ length: 18 }, (_, i) => {
	const base = 1800 + 700 * Math.sin(i / 2.4) + i * 40;
	return {
		hour: `${String(i + 6).padStart(2, "0")}:00`,
		ok: Math.round(base),
		redirect: Math.round(base * 0.12 + 40 * Math.cos(i)),
		client: Math.round(base * 0.05 + 30 * (1 + Math.sin(i * 1.7))),
		server: i === 11 ? 210 : Math.round(4 + ((i * 7) % 9)),
	};
});

const SERIES: readonly [ChartSeries<Row>, ...ChartSeries<Row>[]] = [
	{ label: "2xx", value: "ok", color: "ok" },
	{ label: "3xx", value: "redirect", color: "info" },
	{ label: "4xx", value: "client", color: "warn" },
	{ label: "5xx", value: "server", color: "bad" },
];

const FILLS: { value: FillVariant; label: string }[] = [
	{ value: "gradient", label: "Gradient" },
	{ value: "dotted", label: "Dotted" },
	{ value: "hatched", label: "Hatched" },
	{ value: "solid", label: "Solid" },
];

export function BarChartDemo() {
	const [mode, setMode] = useState<"stacked" | "grouped">("stacked");
	const [fill, setFill] = useState<FillVariant>("gradient");
	const [percent, setPercent] = useState(false);
	const [cell, setCell] = useState(3);
	const [bloom, setBloom] = useState<ChartBloom>("off");
	const [matrix, setMatrix] = useState<DitherMatrix>("bayer4");
	const [highlight, setHighlight] = useState<"unset" | "last" | "max">("unset");
	const percentId = useId();
	const cellLabelId = useId();

	return (
		<section className="surface-raised flex flex-col gap-4 rounded-card p-4">
			<h3 className="type-micro text-ink-3">
				Bar chart · requests by status class
			</h3>
			<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
				<Segmented
					aria-label="Bar mode"
					value={mode}
					onValueChange={setMode}
					options={[
						{ value: "stacked", label: "Stacked" },
						{ value: "grouped", label: "Grouped" },
					]}
				/>
				<Segmented
					aria-label="Bar fill"
					value={fill}
					onValueChange={setFill}
					options={FILLS}
				/>
				<Segmented
					aria-label="Highlight"
					value={highlight}
					onValueChange={setHighlight}
					options={[
						{ value: "unset", label: "No highlight" },
						{ value: "last", label: "Last" },
						{ value: "max", label: "Max" },
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
				<label
					htmlFor={percentId}
					className="flex items-center gap-2 font-mono text-2xs text-ink-2"
				>
					Percent
					<Switch
						id={percentId}
						size="sm"
						aria-label="Stack to 100 percent"
						checked={percent}
						onCheckedChange={setPercent}
						disabled={mode !== "stacked"}
					/>
				</label>
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
			<DitherBarChart
				label="Requests per hour by status class"
				data={ROWS}
				x="hour"
				series={SERIES}
				mode={mode}
				percent={percent}
				fill={fill}
				cell={cell}
				bloom={bloom}
				matrix={matrix}
				{...(highlight === "unset" ? {} : { highlight })}
				height={240}
			/>
		</section>
	);
}
