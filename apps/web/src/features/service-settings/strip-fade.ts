/**
 * The category strip scrolls sideways on a narrow panel. A tab cut off at the
 * edge ("Storag…") reads as a rendering fault unless the edge fades out, so
 * each end that has more behind it gets a short fade.
 */
export interface StripFade {
	/** More tabs to the left of what is in view. */
	start: boolean;
	/** More tabs to the right of what is in view. */
	end: boolean;
}

/** Wide enough to read as a fade, narrow enough to leave the tab legible. */
export const STRIP_FADE_PX = 20;

/** Sub-pixel scroll positions must not leave a fade on an end that is reached. */
const EDGE_SLACK_PX = 1;

export function stripFade(
	scrollLeft: number,
	clientWidth: number,
	scrollWidth: number,
): StripFade {
	return {
		start: scrollLeft > EDGE_SLACK_PX,
		end: scrollLeft + clientWidth < scrollWidth - EDGE_SLACK_PX,
	};
}

/** The `mask-image` that fades the ends in `fade`, or undefined when none. */
export function stripMask({ start, end }: StripFade): string | undefined {
	if (!start && !end) return undefined;
	const left = start ? `transparent, #000 ${STRIP_FADE_PX}px` : "#000, #000";
	const right = end
		? `#000 calc(100% - ${STRIP_FADE_PX}px), transparent`
		: "#000, #000";
	return `linear-gradient(to right, ${left}, ${right})`;
}
