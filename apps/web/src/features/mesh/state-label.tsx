import { cn } from "cn";
import { StatusIcon } from "#/components/ui/status-icon";
import type { Chip } from "./model";

/** A status icon and its word: the way every mesh state is shown. */
export function StateLabel({
	chip,
	className,
}: {
	chip: Chip;
	className?: string;
}) {
	return (
		<span
			className={cn(
				"inline-flex items-center gap-1.5 font-medium text-ink-1 text-xs",
				className,
			)}
		>
			<StatusIcon kind={chip.kind} label="" />
			{chip.label}
		</span>
	);
}
