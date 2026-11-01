import { cn } from "cn";
import type * as React from "react";
import { installToneBandMask, type ToneBandTone } from "./tone-band-math";

export interface DitherToneBandProps
	extends Omit<React.ComponentProps<"div">, "color"> {
	/** The meaning of the marker; the text inside must say the same in words. */
	tone: ToneBandTone;
}

/**
 * A box with a dithered tone band in its left edge: the marker of an alert,
 * banner, callout, inline error or toast. Static. Under forced colours the
 * band is a solid 3px edge. Elements that already exist can take the same look
 * with `toneBandProps(tone)` (or the `tone-band` class plus `data-tone`).
 */
function DitherToneBand({ tone, className, ...props }: DitherToneBandProps) {
	installToneBandMask();
	return (
		<div
			data-slot="dither-tone-band"
			data-tone={tone}
			className={cn("tone-band relative", className)}
			{...props}
		/>
	);
}

export { DitherToneBand };
