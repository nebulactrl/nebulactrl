import { cn } from "cn";
import { StatusIcon } from "#/components/ui/status-icon";

export interface LiveBadgeProps {
	/** The selected range refetches on the fast cadence. */
	live: boolean;
	/** The newest bucket is older than twice the step: data is lagging. */
	delayed?: boolean;
	className?: string;
}

/**
 * The "Live" cue beside a live range: a status icon and the word. A lagging
 * feed turns it into "Live · delayed" with the warning icon; the icon spins
 * only for in-flight states, so a healthy Live stays still.
 */
export function LiveBadge({
	live,
	delayed = false,
	className,
}: LiveBadgeProps) {
	if (!live) return null;
	return (
		<span
			role="status"
			className={cn(
				"inline-flex items-center gap-1.5 font-medium font-mono text-2xs text-ink-2 leading-none",
				className,
			)}
		>
			<StatusIcon kind={delayed ? "warning" : "live"} label="" />
			{delayed ? "Live · delayed" : "Live"}
		</span>
	);
}
