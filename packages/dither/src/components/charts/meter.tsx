import { cn } from "cn";
import type { ReactNode } from "react";
import { DitherProgressBar } from "./progress-bar";

export type DitherMeterTone = "accent" | "warn" | "bad";

const TONE_COLOR = {
	accent: "ink",
	warn: "var(--color-warn)",
	bad: "var(--color-bad)",
} as const;

export interface DitherMeterProps {
	label: string;
	/** 0–100; null draws an empty groove and a dash (nothing was measured). */
	percent: number | null;
	/** Text beside the percentage: "3.1 GiB / 8 GiB". */
	detail?: ReactNode;
	/** Starting colour. Crossing `warnAt` / `badAt` overrides it. */
	tone?: DitherMeterTone;
	/** The fill turns amber at or above this percentage. */
	warnAt?: number;
	/** The fill turns red at or above this percentage. */
	badAt?: number;
	/** A tick at this percentage (the disk alert threshold). */
	threshold?: number;
	/** A second marker (committed limits). It turns amber when `warn`. */
	marker?: { percent: number; title: string; warn?: boolean };
	/** The last reading is old: draw the meter dimmed. */
	stale?: boolean;
	/** Hide the label row (the caller labels it elsewhere). */
	bare?: boolean;
	/** Grow in once on mount, then tween between readings. Default true; see `DitherProgressBar`. */
	animate?: boolean;
	className?: string;
}

/**
 * A capacity meter in dither: the same contract as the solid `Meter` (label,
 * percent, thresholds, markers), drawn as a dithered fill thinning out behind
 * a solid leading edge. The number stays in text; crossing a threshold
 * recolours the fill and adds a word, so colour is never the only cue.
 */
export function DitherMeter({
	label,
	percent,
	detail,
	tone = "accent",
	warnAt,
	badAt,
	threshold,
	marker,
	stale = false,
	bare = false,
	animate = true,
	className,
}: DitherMeterProps) {
	const markers = [
		...(threshold === undefined
			? []
			: [{ percent: threshold, title: `Alert at ${threshold}%` }]),
		...(marker ? [marker] : []),
	];
	const high =
		percent !== null &&
		((badAt !== undefined && percent >= badAt) ||
			(warnAt !== undefined && percent >= warnAt));
	return (
		<div className={cn("flex flex-col gap-1.5", className)}>
			{bare ? null : (
				<div className="flex items-baseline justify-between gap-2 text-xs leading-none">
					<span className="type-label">{label}</span>
					<span
						className={cn(
							"font-mono tabular-nums",
							high ? "text-warning-text" : "text-ink-1",
						)}
					>
						{percent === null
							? "–"
							: `${percent.toFixed(percent < 10 ? 1 : 0)}%`}
						{detail ? <span className="text-ink-4"> · {detail}</span> : null}
					</span>
				</div>
			)}
			<DitherProgressBar
				label={label}
				hideLabel
				value={percent ?? 0}
				valueText={
					percent === null
						? "not measured"
						: `${percent.toFixed(percent < 10 ? 1 : 0)}%`
				}
				color={TONE_COLOR[tone]}
				size="md"
				stale={stale}
				animate={animate}
				markers={markers}
				{...(warnAt === undefined ? {} : { warnAt })}
				{...(badAt === undefined ? {} : { badAt })}
			/>
		</div>
	);
}

export interface DitherCapacityStatProps
	extends Omit<DitherMeterProps, "bare" | "detail" | "label"> {
	/** The 10px mono micro-label: "cpu", "memory", "disk". */
	label: string;
	/** One line under the groove: "5.4 GiB / 15.7 GiB". */
	detail?: ReactNode;
	/** Draws the number's text (`"12.3"`, without the `%`) yourself, for example as a `DitherNumber`. The text it receives is also the accessible value, so keep it in the output. */
	renderValue?: (text: string) => ReactNode;
}

/**
 * A capacity figure: a big tabular number over a micro-label, with the
 * dithered meter under it. Not measured reads a dash, never 0.
 */
export function DitherCapacityStat({
	label,
	percent,
	detail,
	renderValue = (text) => text,
	className,
	...meter
}: DitherCapacityStatProps) {
	const warn =
		percent !== null &&
		((meter.badAt !== undefined && percent >= meter.badAt) ||
			(meter.warnAt !== undefined && percent >= meter.warnAt));
	return (
		<div
			className={cn(
				"flex min-w-0 flex-col gap-2",
				meter.stale && "opacity-50",
				className,
			)}
		>
			<span className="type-micro">{label}</span>
			<span
				className={cn(
					"type-metric leading-none",
					warn ? "text-warning-text" : "text-ink-1",
				)}
			>
				{percent === null
					? "–"
					: renderValue(percent.toFixed(percent < 10 ? 1 : 0))}
				{percent === null ? null : (
					<span className="ml-0.5 font-medium text-ink-3 text-sm">%</span>
				)}
			</span>
			<DitherMeter
				label={label}
				percent={percent}
				bare
				{...meter}
				stale={false}
			/>
			{detail ? (
				<span className="font-mono text-2xs text-ink-4 leading-snug">
					{detail}
				</span>
			) : null}
		</div>
	);
}
