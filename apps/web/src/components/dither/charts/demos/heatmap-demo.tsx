import { DitherHeatmap } from "@nebulactrl/dither/charts/heatmap";
import type { ChartBloom } from "@nebulactrl/dither/charts/shared";
import { useId, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { Slider } from "#/components/ui/slider";

type Layout = "matrix" | "calendar";
type Tint = "ink" | "info" | "ok" | "purple" | "orange";

const DAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"] as const;
const HOURS = Array.from({ length: 24 }, (_, h) => String(h));

/** Deterministic 0…1 noise from an integer. */
const noise = (n: number) => (Math.imul(n + 1, 2654435761) >>> 0) / 2 ** 32;

/** Active hours: a weekday bump around 14:00, a quiet weekend, silent nights. */
const ACTIVE_HOURS: number[][] = DAYS.map((_, d) =>
	HOURS.map((_, h) => {
		const work = Math.exp(-((h - 14) ** 2) / 22);
		const weekend = d >= 5 ? 0.22 : 1;
		const value = Math.round(work * weekend * (55 + noise(d * 24 + h) * 45));
		return value < 4 ? 0 : value;
	}),
);

/** Thirty weeks of daily deploys from Sun 4 Jan 2026; a few days have no entry at all. */
const DEPLOYS = Array.from({ length: 210 }, (_, i) => {
	const weekday = i % 7;
	const n = noise(i * 7 + 3);
	return {
		date: new Date(Date.UTC(2026, 0, 4 + i)).toISOString().slice(0, 10),
		value:
			weekday === 0 || weekday === 6
				? Math.round(n * 3)
				: Math.round(n * n * 22),
	};
}).filter((_, i) => noise(i + 900) > 0.08);

const LAYOUTS = [
	{ value: "matrix", label: "Matrix" },
	{ value: "calendar", label: "Calendar" },
] as const;
const TINTS = [
	{ value: "ink", label: "Ink" },
	{ value: "info", label: "Info" },
	{ value: "ok", label: "Live" },
	{ value: "purple", label: "Purple" },
	{ value: "orange", label: "Orange" },
] as const;

export function HeatmapDemo() {
	const [layout, setLayout] = useState<Layout>("matrix");
	const [tint, setTint] = useState<Tint>("ink");
	const [cellSize, setCellSize] = useState(14);
	const [levels, setLevels] = useState(5);
	const [bloom, setBloom] = useState<ChartBloom>("off");
	const cellSizeLabelId = useId();
	const levelsLabelId = useId();

	return (
		<div className="surface-raised flex min-w-0 flex-col gap-4 rounded-card p-4">
			<div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
				<span className="type-micro">
					{layout === "matrix" ? "Active hours" : "Deploys per day"}
				</span>
				<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
					<Segmented
						aria-label="Layout"
						value={layout}
						onValueChange={setLayout}
						options={LAYOUTS}
					/>
					<Segmented
						aria-label="Colour"
						value={tint}
						onValueChange={setTint}
						options={TINTS}
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
						<span id={cellSizeLabelId}>Cell {cellSize}px</span>
						<Slider
							aria-labelledby={cellSizeLabelId}
							className="w-24"
							min={10}
							max={16}
							step={1}
							value={cellSize}
							onValueChange={setCellSize}
						/>
					</div>
					<div className="flex items-center gap-2 font-mono text-2xs text-ink-3">
						<span id={levelsLabelId}>Levels {levels}</span>
						<Slider
							aria-labelledby={levelsLabelId}
							className="w-24"
							min={3}
							max={7}
							step={1}
							value={levels}
							onValueChange={setLevels}
						/>
					</div>
				</div>
			</div>
			{layout === "matrix" ? (
				<DitherHeatmap
					label="Active hours by weekday and hour of day"
					rows={DAYS}
					columns={HOURS}
					values={ACTIVE_HOURS}
					unit="requests"
					color={tint}
					cellSize={cellSize}
					levels={levels}
					bloom={bloom}
				/>
			) : (
				<DitherHeatmap
					layout="calendar"
					label="Deploys per day"
					data={DEPLOYS}
					unit={{ one: "deploy", other: "deploys" }}
					color={tint}
					cellSize={cellSize}
					levels={levels}
					bloom={bloom}
				/>
			)}
		</div>
	);
}
