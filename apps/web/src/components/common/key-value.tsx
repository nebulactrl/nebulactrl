import { cn } from "cn";
import type { ReactNode } from "react";

/**
 * A grid of muted labels over mono values — the definition list an expanded
 * row or a detail panel shows.
 */
export function FactGrid({
	className,
	children,
}: {
	className?: string;
	children: ReactNode;
}) {
	return (
		<dl
			data-slot="fact-grid"
			className={cn(
				"grid grid-cols-2 gap-x-8 gap-y-5 md:grid-cols-4",
				className,
			)}
		>
			{children}
		</dl>
	);
}

export function Fact({
	label,
	children,
	mono = true,
}: {
	label: ReactNode;
	children: ReactNode;
	mono?: boolean;
}) {
	return (
		<div className="min-w-0">
			<dt className="type-label">{label}</dt>
			<dd
				className={cn(
					"mt-1.5 truncate text-ink-1",
					mono ? "font-mono text-xs tabular-nums" : "text-sm",
				)}
			>
				{children}
			</dd>
		</div>
	);
}
