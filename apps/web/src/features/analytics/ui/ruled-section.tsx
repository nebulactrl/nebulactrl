import { cn } from "cn";
import type { ReactNode } from "react";
import { EYEBROW } from "#/features/metrics/ui/eyebrow";

export interface RuledSectionProps {
	title: ReactNode;
	/** One line under the title: the scope or the caveat. */
	subtitle?: ReactNode;
	/** Right of the title: a segmented control. */
	actions?: ReactNode;
	children: ReactNode;
	className?: string;
}

/**
 * A titled block of ranked rows under a hairline rule. Cards are for metric
 * tiles and charts; a ranked list sits on the page like a table does, so it
 * does not get a frame of its own.
 */
export function RuledSection({
	title,
	subtitle,
	actions,
	children,
	className,
}: RuledSectionProps) {
	return (
		<section
			className={cn(
				"flex min-w-0 flex-col gap-2 border-line-faint border-t pt-4",
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
