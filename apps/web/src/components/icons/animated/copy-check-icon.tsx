// Composes this directory's `CopyIcon` and `CheckIcon` (adapted from
// lucide-animated, MIT, Copyright (c) 2024-2026 pqoqubbw; see ./LICENSE).

import { cn } from "cn";
import { CheckIcon } from "./check-icon";
import { CopyIcon } from "./copy-icon";
import type { AnimatedIconProps } from "./icon-base";

type FaceProps = Pick<AnimatedIconProps, "size" | "className">;

export interface CopyCheckIconProps extends FaceProps {
	/** The value was copied: the check replaces the copy glyph. */
	copied: boolean;
	/**
	 * Swap without motion (the copy was started from the keyboard). The check
	 * still appears; it just does not scale in or draw itself.
	 */
	instant?: boolean | undefined;
	/** Extra classes for the check face only (its confirming colour). */
	checkClassName?: string | undefined;
}

// better-ui, exact: scale 0.25 to 1, opacity 0 to 1 and 4px of blur to 0, over
// 300ms on cubic-bezier(0.2, 0, 0, 1) (`--icon-swap-*`). Both faces stay in
// the DOM and cross-fade in one cell.
const FACE =
	"rm-fade col-start-1 row-start-1 transition-[opacity,scale,filter] duration-(--icon-swap-duration) ease-(--icon-swap-ease)";
const HIDDEN = "scale-(--icon-swap-scale-from) opacity-0 blur-[4px]";

/**
 * The copy glyph that becomes a check once the value is on the clipboard:
 * the two faces share one cell, the copy sheets shrink away while the check
 * scales in and draws its tick. A keyboard copy swaps with no transition;
 * reduced motion cross-fades with opacity only.
 */
export function CopyCheckIcon({
	copied,
	instant = false,
	checkClassName,
	className,
	...face
}: CopyCheckIconProps) {
	const still = instant;
	return (
		<span aria-hidden className="inline-grid shrink-0 place-items-center">
			<CopyIcon
				{...face}
				className={cn(
					FACE,
					still && "transition-none",
					copied && HIDDEN,
					className,
				)}
			/>
			<CheckIcon
				{...face}
				animate={copied && !still}
				className={cn(
					FACE,
					still && "transition-none",
					copied ? "" : HIDDEN,
					className,
					checkClassName,
				)}
			/>
		</span>
	);
}
