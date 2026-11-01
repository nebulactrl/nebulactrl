import type { ReactNode } from "react";
import { EYEBROW } from "#/features/metrics/ui/eyebrow";

/**
 * A pane's title and the one line under it, set the way every chart card and
 * ruled section of the observability pages set theirs (`ChartCard`,
 * `RuledSection`): the `type-micro` title, then a `text-ink-3` sentence.
 */
export function PaneHeader({
	title,
	subtitle,
	className,
}: {
	title: ReactNode;
	subtitle?: ReactNode;
	className?: string;
}) {
	return (
		<header className={className}>
			<div className="flex min-w-0 flex-col gap-1.5">
				<h4 className={EYEBROW}>{title}</h4>
				{subtitle ? (
					<p className="text-ink-3 text-xs leading-tight">{subtitle}</p>
				) : null}
			</div>
		</header>
	);
}
