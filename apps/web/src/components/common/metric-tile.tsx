import { cn } from "cn";
import type { ReactNode } from "react";

/** A raised tile: a 10px micro-label, a big tabular number, one line of hint. */
export function MetricTile({
	label,
	value,
	hint,
	className,
}: {
	label: ReactNode;
	/** Pass "–" when not measured, never 0. */
	value: ReactNode;
	hint?: ReactNode;
	className?: string;
}) {
	return (
		<div
			data-slot="metric-tile"
			className={cn(
				"surface-raised flex min-w-0 flex-col gap-2.5 rounded-card px-5 py-[18px]",
				className,
			)}
		>
			<span className="type-micro">{label}</span>
			<span className="type-metric truncate text-ink-1 leading-none">
				{value}
			</span>
			{hint ? (
				<span className="text-ink-3 text-xs leading-[1.3]">{hint}</span>
			) : null}
		</div>
	);
}
