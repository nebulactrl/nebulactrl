import { Progress as ProgressPrimitive } from "@base-ui/react/progress";
import { cn } from "cn";

interface ProgressProps extends ProgressPrimitive.Root.Props {
	/**
	 * Percentage at and above which the bar turns amber. For capacity (CPU,
	 * memory, volume) where "nearly full" needs a person; leave unset for
	 * work that is simply moving, like a deploy.
	 */
	warnAt?: number;
	size?: "default" | "hairline";
	/**
	 * Work in flight: the fill glides between positions, light sweeps along
	 * it and its leading edge glows. A null value runs a segment across the
	 * track instead of inventing a number.
	 */
	live?: boolean;
	/** Amber regardless of value, for a build rather than a rollout. */
	tone?: "default" | "warning";
	/**
	 * Render as spans, for a bar inside a button, where a div is not
	 * allowed.
	 */
	inline?: boolean;
}

function Progress({
	className,
	children,
	value,
	warnAt,
	size = "default",
	live = false,
	tone = "default",
	inline = false,
	...props
}: ProgressProps) {
	const warn =
		tone === "warning" ||
		(warnAt !== undefined && value !== null && value >= warnAt);
	const hairline = size === "hairline";
	return (
		<ProgressPrimitive.Root
			value={value}
			data-slot="progress"
			data-warn={warn || undefined}
			data-live={live || undefined}
			className={cn(
				"group/progress flex flex-wrap gap-3",
				inline && "block",
				className,
			)}
			{...(inline ? { render: <span /> } : {})}
			{...props}
		>
			{children}
			<ProgressTrack
				hairline={hairline}
				className={cn(
					live && hairline && "h-[3px]",
					// The fill's glow has to reach past the track; only a
					// segment crossing an unmeasured track needs clipping.
					live && "overflow-visible has-[[data-indeterminate]]:overflow-hidden",
				)}
				{...(inline ? { render: <span /> } : {})}
			>
				<ProgressIndicator
					hairline={hairline}
					className={cn(live && liveIndicatorClassName)}
					{...(inline ? { render: <span /> } : {})}
				/>
			</ProgressTrack>
		</ProgressPrimitive.Root>
	);
}

/*
 * The fill glides on the sheet's own curve, so a stage landing reads as
 * the same motion as the panels around it. The sweep is a gradient on the
 * fill itself, so it only ever lights the part that is done; reduced
 * motion keeps the glide and drops the sweep.
 */
const liveIndicatorClassName =
	"shadow-progress duration-700 ease-smooth-out group-data-[warn]/progress:shadow-progress-warning bg-linear-to-r from-transparent via-accent-ink to-transparent bg-size-[40%_100%] bg-no-repeat group-data-[warn]/progress:via-status-staged-on motion-safe:animate-nc-sheen data-indeterminate:w-1/3 data-indeterminate:bg-none motion-safe:data-indeterminate:animate-nc-indeterminate motion-reduce:data-indeterminate:w-full motion-reduce:data-indeterminate:opacity-60";

/**
 * The groove: an engraved `track-well` with the 3:1 control border. A
 * `hairline` track is the flat 2px line used inside dense cards, where a
 * bordered well would be heavier than the data.
 */
function ProgressTrack({
	className,
	hairline = false,
	...props
}: ProgressPrimitive.Track.Props & { hairline?: boolean }) {
	return (
		<ProgressPrimitive.Track
			className={cn(
				"relative flex w-full items-center overflow-hidden",
				hairline ? "h-0.5 bg-white/10" : "track-well h-2 rounded-full",
				className,
			)}
			data-slot="progress-track"
			{...props}
		/>
	);
}

/** The lit fill: an accent gradient with a faint glow, amber when `warn`. */
function ProgressIndicator({
	className,
	hairline = false,
	...props
}: ProgressPrimitive.Indicator.Props & { hairline?: boolean }) {
	return (
		<ProgressPrimitive.Indicator
			data-slot="progress-indicator"
			className={cn(
				"h-full transition-[width,background-color,box-shadow] duration-[380ms] ease-linear group-data-[warn]/progress:bg-none group-data-[warn]/progress:bg-warn group-data-[warn]/progress:shadow-none",
				hairline
					? "bg-violet-bar shadow-progress"
					: "rounded-full bg-linear-to-b from-accent-fill-top to-accent-fill-bot shadow-progress",
				className,
			)}
			{...props}
		/>
	);
}

export { Progress };
