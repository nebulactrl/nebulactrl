import { cn } from "cn";
import type { ReactNode } from "react";
import { Slider } from "#/components/ui/slider";

/** A titled panel of the gallery: a preview stage and a column of knobs. */
export function GallerySection({
	title,
	description,
	stage,
	children,
	stageClassName,
}: {
	title: string;
	description: string;
	stage: ReactNode;
	/** The knobs. */
	children: ReactNode;
	stageClassName?: string;
}) {
	return (
		<section className="flex flex-col gap-3">
			<header className="flex flex-col gap-0.5">
				<h3 className="font-medium text-ink-1 text-sm">{title}</h3>
				<p className="max-w-2xl text-ink-3 text-xs">{description}</p>
			</header>
			<div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_380px]">
				<div
					className={cn(
						"relative isolate flex min-h-48 items-center justify-center overflow-hidden rounded-panel border border-line bg-surface-base p-6",
						stageClassName,
					)}
				>
					{stage}
				</div>
				<div className="flex flex-col gap-3 rounded-panel border border-line bg-surface-raised p-4">
					{children}
				</div>
			</div>
		</section>
	);
}

/** A labelled knob. */
export function Knob({
	label,
	children,
}: {
	label: string;
	children: ReactNode;
}) {
	return (
		<div className="flex flex-col gap-1.5">
			<span className="font-mono text-2xs text-ink-3 uppercase tracking-wider">
				{label}
			</span>
			{children}
		</div>
	);
}

/** A number knob: slider plus a tabular readout. */
export function RangeKnob({
	label,
	value,
	min,
	max,
	step,
	onChange,
}: {
	label: string;
	value: number;
	min: number;
	max: number;
	step: number;
	onChange: (next: number) => void;
}) {
	const decimals = step >= 1 ? 0 : step >= 0.1 ? 1 : 2;
	return (
		<Knob label={label}>
			<div className="flex items-center gap-3">
				<Slider
					aria-label={label}
					min={min}
					max={max}
					step={step}
					value={value}
					onValueChange={(next) => {
						if (typeof next === "number") onChange(next);
					}}
				/>
				<output className="w-10 shrink-0 text-right font-mono text-ink-3 text-xs tabular-nums">
					{value.toFixed(decimals)}
				</output>
			</div>
		</Knob>
	);
}
