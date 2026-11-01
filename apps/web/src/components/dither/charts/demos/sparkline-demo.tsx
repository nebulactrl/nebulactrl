import type { ChartBloom, FillVariant } from "@nebulactrl/dither/charts/shared";
import { DitherSparkline } from "@nebulactrl/dither/charts/sparkline";
import { useId, useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { Switch } from "#/components/ui/switch";

const trend = (seed: number, length = 36) =>
	Array.from({ length }, (_, i) =>
		Math.round(
			40 +
				22 * Math.sin(i / 3 + seed) +
				12 * Math.cos(i / 1.7 + seed * 2) +
				i * seed,
		),
	);

const ROWS = [
	{ label: "Requests", color: "ink", data: trend(0.6) },
	{
		label: "Errors",
		color: "red",
		data: trend(0.2).map((v) => Math.max(2, v - 38)),
	},
	{ label: "Visitors", color: "green", data: trend(1.1) },
	{ label: "Latency", color: "warn", data: trend(0.4).reverse() },
] as const;

export function SparklineDemo() {
	const [fill, setFill] = useState<FillVariant>("gradient");
	const [area, setArea] = useState(true);
	const [interactive, setInteractive] = useState(false);
	const [bloom, setBloom] = useState<ChartBloom>("off");
	const areaId = useId();
	const interactiveId = useId();

	return (
		<section className="surface-raised flex min-w-0 flex-col gap-4 rounded-card p-4">
			<h3 className="type-micro">Sparkline</h3>
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
				<label
					htmlFor={areaId}
					className="flex items-center gap-2 font-mono text-2xs text-ink-2"
				>
					Area
					<Switch
						id={areaId}
						size="sm"
						aria-label="Fill under the line"
						checked={area}
						onCheckedChange={setArea}
					/>
				</label>
				<label
					htmlFor={interactiveId}
					className="flex items-center gap-2 font-mono text-2xs text-ink-2"
				>
					Scrub
					<Switch
						id={interactiveId}
						size="sm"
						aria-label="Scrub with the pointer or arrow keys"
						checked={interactive}
						onCheckedChange={setInteractive}
					/>
				</label>
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
			</div>
			<ul className="flex flex-col gap-2">
				{ROWS.map((row) => (
					<li
						key={row.label}
						className="grid grid-cols-[6rem_1fr_3.5rem] items-center gap-3"
					>
						<span className="font-mono text-2xs text-ink-3">{row.label}</span>
						<DitherSparkline
							data={row.data}
							label={row.label}
							color={row.color}
							fill={fill}
							area={area}
							interactive={interactive}
							bloom={bloom}
							height={36}
							className="w-full"
						/>
						<span className="text-right font-mono text-xs text-ink-1 tabular-nums">
							{row.data.at(-1)}
						</span>
					</li>
				))}
			</ul>
		</section>
	);
}
