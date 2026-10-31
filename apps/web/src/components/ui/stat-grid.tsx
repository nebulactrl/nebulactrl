import { cn } from "cn";
import type { ReactNode } from "react";

/**
 * Raised tiles on the floor, 12px apart: each stat is its own card, and the
 * grid reflows to any column count.
 */
export function StatGrid({
	className,
	children,
}: {
	className?: string;
	children: ReactNode;
}) {
	return (
		<div
			data-slot="stat-grid"
			className={cn(
				"grid grid-cols-[repeat(auto-fit,minmax(min(100%,200px),1fr))] gap-3",
				className,
			)}
		>
			{children}
		</div>
	);
}
