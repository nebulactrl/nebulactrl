import { durations } from "@nebulactrl/tokens";
import { cn } from "cn";
import * as React from "react";

// A div that takes a size and a radius from its className. The look is a flat
// `surface-well` block with a hairline border that breathes in opacity (the
// `nc-skeleton` class in styles.css; still at 0.7 under reduced motion). No
// grain, no sweep: dither encodes data or identity, never waiting.
function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
	const hasRole = props.role !== undefined;
	return (
		<div
			data-slot="skeleton"
			aria-hidden={hasRole ? undefined : true}
			className={cn(
				"nc-skeleton",
				className?.includes("rounded") ? null : "rounded-ctl",
				className,
			)}
			{...props}
		/>
	);
}

type RevealPhase = "loading" | "revealing" | "done";

interface SkeletonRevealProps {
	/** True while the data is on its way (a query's `isPending`). */
	loading: boolean;
	/** What stands in the slot meanwhile: `Skeleton` blocks. */
	skeleton: React.ReactNode;
	/** The loaded content; a function when it reads data that is not there yet. */
	children: React.ReactNode | (() => React.ReactNode);
	/** The caller's layout classes; they apply to the skeleton and to the content alike. */
	className?: string;
}

/**
 * The skeleton-to-content swap (transitions.dev 14): when `loading` ends the
 * content fades in and un-blurs (400ms, `ease-in-out`, 2px) over the skeleton,
 * which fades and blurs out on top of it, so the swap reads as one motion.
 * The wrapper is the slot: it carries `className`, so a flex or grid parent
 * sees the same single item before, during and after. Content that is
 * already loaded on mount never animates, and reduced motion cross-fades
 * with opacity only.
 */
function SkeletonReveal({
	loading,
	skeleton,
	children,
	className,
}: SkeletonRevealProps) {
	const [phase, setPhase] = React.useState<RevealPhase>(
		loading ? "loading" : "done",
	);
	const [revealed, setRevealed] = React.useState(!loading);

	React.useEffect(() => {
		if (loading) {
			setPhase("loading");
			setRevealed(false);
			return;
		}
		setPhase((current) => (current === "loading" ? "revealing" : current));
	}, [loading]);

	React.useEffect(() => {
		if (phase !== "revealing") return;
		// Two frames: the first paints the content hidden, the second starts the tween.
		let second = 0;
		const first = requestAnimationFrame(() => {
			second = requestAnimationFrame(() => setRevealed(true));
		});
		const done = window.setTimeout(() => setPhase("done"), durations.slow + 80);
		return () => {
			cancelAnimationFrame(first);
			cancelAnimationFrame(second);
			window.clearTimeout(done);
		};
	}, [phase]);

	const revealing = phase === "revealing";
	return (
		<div
			className={cn(
				className,
				revealing && "t-skel",
				revealing && revealed && "is-revealed",
			)}
		>
			{phase === "loading" ? (
				skeleton
			) : (
				<>
					{revealing ? (
						<div aria-hidden className={cn("t-skel-skeleton", className)}>
							{skeleton}
						</div>
					) : null}
					{typeof children === "function" ? children() : children}
				</>
			)}
		</div>
	);
}

export { Skeleton, SkeletonReveal };
