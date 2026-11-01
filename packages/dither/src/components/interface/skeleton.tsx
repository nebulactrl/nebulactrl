import { cn } from "cn";
import * as React from "react";
import type { DitherMatrix } from "../../lib/bayer";
import { type BandMask, bandMaskClass } from "./bands";
import { type DitherColor, type DitherFillVariant, inkCss, mix } from "./fill";
import { ensureInterfaceStyles } from "./keyframes";
import {
	lineWidths,
	PULSE_LEVELS,
	restMask,
	SWEEP_STOPS,
} from "./skeleton-math";

export interface DitherSkeletonProps
	extends Omit<React.ComponentProps<"div">, "color"> {
	/** `false` leaves the static grain only. Reduced motion does the same. Same as `animation="none"`. */
	shimmer?: boolean;
	/** A rectangle (size it with `className`), lines of text, or an avatar. */
	shape?: "block" | "text" | "circle";
	/** Text only: how many lines; the last one is shorter. Default 3. */
	lines?: number;
	/** Circle only: diameter in px. Default 40. */
	size?: number;
	/** A palette name, a custom property or any CSS colour. Default: the muted ink. */
	color?: DitherColor | undefined;
	/** Texture of the resting layer: a Bayer ramp from the left, a dot lattice, diagonals, or a plain tint. Default: the house grain. */
	fill?: DitherFillVariant | undefined;
	/** A dense band sweeping across, the dither stepping up and down through three levels, or nothing. Default `sweep`. */
	animation?: "sweep" | "pulse" | "none";
	/** Size of one dither cell in CSS px. Default 2. */
	cell?: number | undefined;
	/** The threshold pattern. Default `bayer4`. */
	matrix?: DitherMatrix | undefined;
	/** Seconds for one sweep or pulse. Default 1.6 for a sweep, 1.8 for a pulse. */
	duration?: number | undefined;
	/** Seconds to wait before the first sweep. Offset it per row for a wave. */
	delay?: number | undefined;
}

interface WellProps
	extends Omit<DitherSkeletonProps, "shape" | "lines" | "size"> {
	pulseDuration: number;
}

/** One placeholder well: the resting grain, and a sweep or a pulse on top of it. */
function Well({
	className,
	shimmer = true,
	color,
	fill,
	animation,
	cell = 2,
	matrix,
	duration,
	delay = 0,
	style,
	children,
	pulseDuration,
	...props
}: WellProps) {
	const mode = animation ?? (shimmer ? "sweep" : "none");
	const hue = color === undefined ? undefined : inkCss(color);
	const masks = React.useMemo(() => {
		const extra = matrix ? { matrix } : {};
		const rest = restMask(fill);
		const restClass = bandMaskClass({
			axis: rest.axis,
			stops: rest.stops,
			cell,
			...(rest.texture ? { texture: rest.texture } : {}),
			...extra,
		});
		const sweep: BandMask = {
			axis: "x",
			stops: SWEEP_STOPS,
			cell,
			...(fill ? { texture: fill } : {}),
			...extra,
		};
		const levels = PULSE_LEVELS.map((tone) =>
			bandMaskClass({
				axis: "y",
				stops: [tone],
				cell,
				texture: fill ?? "gradient",
				...extra,
			}),
		);
		return { rest: restClass, sweep: bandMaskClass(sweep), levels };
	}, [fill, cell, matrix]);

	ensureInterfaceStyles();
	const timing: React.CSSProperties = {
		...(duration !== undefined ? { animationDuration: `${duration}s` } : {}),
		...(delay > 0
			? { animationDelay: `${delay}s`, animationFillMode: "backwards" }
			: {}),
	};

	return (
		<div
			data-slot="skeleton"
			data-animation={mode}
			className={cn(
				"relative isolate overflow-hidden rounded-md bg-surface-well",
				className,
			)}
			style={style}
			{...props}
		>
			<span
				aria-hidden
				className={cn(
					"pointer-events-none absolute inset-0 forced-colors:hidden",
					hue ? "" : "bg-ink-1/[0.09]",
					masks.rest,
				)}
				style={
					hue ? { backgroundColor: mix(hue, "transparent", 40) } : undefined
				}
			/>
			{mode === "sweep" ? (
				<span
					aria-hidden
					className={cn(
						"pointer-events-none absolute inset-y-0 left-0 w-2/5 animate-dither-sweep will-change-transform motion-reduce:hidden forced-colors:hidden",
						hue ? "" : "bg-ink-1/[0.2]",
						masks.sweep,
					)}
					style={{
						...timing,
						...(hue ? { backgroundColor: mix(hue, "transparent", 70) } : null),
					}}
				/>
			) : null}
			{mode === "pulse"
				? masks.levels.map((levelClass, index) => (
						<span
							key={levelClass}
							aria-hidden
							className={cn(
								"nc-if-pulse pointer-events-none absolute inset-0 forced-colors:hidden",
								`nc-if-p${index + 1}`,
								hue ? "" : "bg-ink-1/[0.2]",
								levelClass,
							)}
							style={
								{
									"--nc-if-dur": `${duration ?? pulseDuration}s`,
									"--nc-if-delay": `${delay}s`,
									...(hue
										? { backgroundColor: mix(hue, "transparent", 55) }
										: null),
								} as React.CSSProperties
							}
						/>
					))
				: null}
			{children}
		</div>
	);
}

/**
 * A placeholder finished in dither: a fixed grain, and a band of denser
 * dither that sweeps across it. The sweep is a `transform`, so it costs no
 * layout or paint per frame. `animation="pulse"` steps the whole block up
 * through three nested Bayer levels and back instead. Under reduced motion the
 * band is dropped and the grain alone stays: still readable as "something goes
 * here", nothing moves.
 *
 * `shape="text"` stacks `lines` of them, the last one shorter; `shape="circle"`
 * is an avatar of `size` px. Skeletons are decoration: put `aria-busy` on the
 * region that is loading and a visually hidden sentence in it.
 */
function DitherSkeleton({
	shape = "block",
	lines = 3,
	size = 40,
	className,
	style,
	delay = 0,
	...props
}: DitherSkeletonProps) {
	const pulseDuration = 1.8;
	if (shape === "text") {
		const widths = lineWidths(lines);
		return (
			<div
				data-slot="skeleton-text"
				className={cn("flex w-full flex-col gap-2", className)}
				style={style}
			>
				{widths.map((width, index) => (
					<Well
						// biome-ignore lint/suspicious/noArrayIndexKey: lines are positional
						key={index}
						{...props}
						className="h-3 rounded-sm"
						style={{ width }}
						delay={delay + index * 0.08}
						pulseDuration={pulseDuration}
					/>
				))}
			</div>
		);
	}
	if (shape === "circle") {
		return (
			<Well
				{...props}
				delay={delay}
				className={cn("shrink-0 rounded-full", className)}
				style={{ width: size, height: size, ...style }}
				pulseDuration={pulseDuration}
			/>
		);
	}
	return (
		<Well
			{...props}
			delay={delay}
			className={className}
			{...(style ? { style } : {})}
			pulseDuration={pulseDuration}
		/>
	);
}

export { DitherSkeleton };
