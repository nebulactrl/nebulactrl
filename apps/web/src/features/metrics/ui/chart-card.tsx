import { cn } from "cn";
import type { ReactNode } from "react";
import { EYEBROW } from "./eyebrow";

export interface ChartCardProps {
	title: ReactNode;
	/** One line under the title: the unit, the scope, the caveat. */
	subtitle?: ReactNode;
	/** Right of the title: legend chips, a unit toggle. */
	actions?: ReactNode;
	children: ReactNode;
	className?: string;
	/** Stale data stays visible but dimmed. */
	dimmed?: boolean;
}

/** The raised card every chart and ranked list sits in: eyebrow title row, then the plot. */
export function ChartCard({
	title,
	subtitle,
	actions,
	children,
	className,
	dimmed = false,
}: ChartCardProps) {
	return (
		<section
			className={cn(
				"surface-raised flex min-w-0 flex-col gap-3 rounded-card p-4 transition-opacity duration-quick",
				dimmed && "opacity-60",
				className,
			)}
		>
			<header className="flex flex-wrap items-start justify-between gap-x-3 gap-y-2">
				<div className="flex min-w-0 flex-col gap-1.5">
					<h3 className={EYEBROW}>{title}</h3>
					{subtitle ? (
						<p className="text-ink-3 text-xs leading-tight">{subtitle}</p>
					) : null}
				</div>
				{actions ? (
					<div className="flex shrink-0 flex-wrap items-center gap-2">
						{actions}
					</div>
				) : null}
			</header>
			{children}
		</section>
	);
}
