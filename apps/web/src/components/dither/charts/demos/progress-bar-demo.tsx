import {
	DitherCapacityStat,
	DitherMeter,
} from "@nebulactrl/dither/charts/meter";
import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import type { ChartBloom, FillVariant } from "@nebulactrl/dither/charts/shared";
import { useId, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { Slider } from "#/components/ui/slider";

const PARTS = [
	{ label: "Images", value: 4.1, color: "ink", text: "4.1 GB" },
	{ label: "Video", value: 2.3, color: "info", text: "2.3 GB" },
	{ label: "Backups", value: 0.8, color: "ok", text: "0.8 GB" },
] as const;

export function ProgressBarDemo() {
	const [value, setValue] = useState(64);
	const [fill, setFill] = useState<FillVariant>("gradient");
	const [bloom, setBloom] = useState<ChartBloom>("off");
	const [size, setSize] = useState<"sm" | "md" | "lg">("md");
	const valueLabelId = useId();

	return (
		<section className="surface-raised flex min-w-0 flex-col gap-5 rounded-card p-4">
			<h3 className="type-micro">Progress bar and meter</h3>
			<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
				<Segmented
					aria-label="Fill"
					value={fill}
					onValueChange={setFill}
					options={[
						{ value: "gradient", label: "Gradient" },
						{ value: "dotted", label: "Dotted" },
						{ value: "hatched", label: "Hatched" },
						{ value: "solid", label: "Solid" },
					]}
				/>
				<Segmented
					aria-label="Size"
					value={size}
					onValueChange={setSize}
					options={[
						{ value: "sm", label: "sm" },
						{ value: "md", label: "md" },
						{ value: "lg", label: "lg" },
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
				<div className="flex items-center gap-2 font-mono text-2xs text-ink-2">
					<span id={valueLabelId}>Value {value}</span>
					<Slider
						aria-labelledby={valueLabelId}
						className="w-28"
						min={0}
						max={100}
						step={1}
						value={value}
						onValueChange={setValue}
					/>
				</div>
			</div>
			<DitherProgressBar
				label="Disk"
				value={value}
				fill={fill}
				size={size}
				warnAt={80}
				badAt={95}
				bloom={bloom}
			/>
			<DitherProgressBar
				label="Storage by type"
				parts={PARTS}
				max={10}
				valueText="7.2 of 10 GB"
				unit=" GB"
				bloom={bloom}
				size={size}
			/>
			<DitherProgressBar
				label="Onboarding"
				value={3}
				max={5}
				segments={5}
				valueText="3 of 5"
				size={size}
			/>
			<DitherProgressBar label="Indexing repository" size={size} />
			<div className="grid grid-cols-3 gap-4">
				<DitherCapacityStat label="cpu" percent={42} warnAt={80} badAt={95} />
				<DitherCapacityStat
					label="memory"
					percent={86}
					detail="6.9 GiB / 8 GiB"
					warnAt={85}
					marker={{
						percent: 92,
						title: "Committed limits: 7.4 GiB",
						warn: true,
					}}
				/>
				<DitherCapacityStat label="disk" percent={null} />
			</div>
			<DitherMeter
				label="Volume"
				percent={71}
				detail="35 GiB / 50 GiB"
				threshold={85}
			/>
		</section>
	);
}
