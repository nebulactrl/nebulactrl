import { useTick } from "@nebulactrl/dither/lib/tick";
import { cn } from "cn";

export interface PopNumberProps {
	/** Already formatted: "12.3%", "212 MiB". The roll plays when this text changes. */
	text: string;
	className?: string;
}

/**
 * A number that rolls up from below with a blur-clear whenever its text
 * changes (via `useTick`). The first value
 * never animates and reduced motion skips it; the new value is also just
 * the text in place, so the change is never animation-only.
 */
export function PopNumber({ text, className }: PopNumberProps) {
	const ref = useTick();
	return (
		<span ref={ref} className={cn("inline-block tabular-nums", className)}>
			{text}
		</span>
	);
}
