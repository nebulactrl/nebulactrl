import { cn } from "cn";
import { CheckIcon } from "lucide-react";
import type { ReactNode } from "react";
import { ToneIcon } from "#/components/common/tone-icon";

export type SaveBarState = "saved" | "dirty" | "invalid" | "failed";

/**
 * The text at the start of a save bar: "All changes saved", "Unsaved changes",
 * a validation hint or the save error. Text plus an optional small icon (a
 * check when saved, a tone icon for a problem). No band, no wash: the bar is a
 * plain `surface-float` strip and the words carry the state.
 */
export function SaveBarStatus({
	state,
	children,
}: {
	state: SaveBarState;
	children: ReactNode;
}) {
	return (
		<span
			className={cn(
				"mr-auto flex min-w-0 items-center gap-2 text-xs",
				state === "failed" ? "text-status-failed-on" : "text-ink-2",
			)}
		>
			{state === "saved" ? (
				<CheckIcon aria-hidden className="size-3.5 shrink-0 text-status-live" />
			) : state === "failed" ? (
				<ToneIcon tone="danger" />
			) : state === "invalid" ? (
				<ToneIcon tone="warning" />
			) : null}
			<span className="min-w-0">{children}</span>
		</span>
	);
}
