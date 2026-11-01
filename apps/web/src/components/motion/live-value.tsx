import { useTick } from "@nebulactrl/dither/lib/tick";
import { cn } from "cn";

export interface LiveValueProps {
	/** Already formatted ("12.5%", "212 MiB"). The roll plays when this text changes. */
	children: string;
	className?: string;
}

/**
 * A live figure that re-enters from below with a blur-clear whenever its text
 * changes (transitions.dev 02, number pop-in, through the kit's `useTick`).
 * For a plain value inside a row or table cell that refetches; a KPI tile
 * uses `KpiNumber`, which resolves from dither first. The first value never
 * animates and reduced motion skips it; the new value is also just the text
 * in place, so a change is never animation-only.
 */
export function LiveValue({ children, className }: LiveValueProps) {
	const ref = useTick();
	return (
		<span ref={ref} className={cn("inline-block tabular-nums", className)}>
			{children}
		</span>
	);
}
