import { cn } from "cn";
import { ChevronRightIcon } from "lucide-react";
import { type ReactNode, useEffect, useRef } from "react";

/**
 * The second level of disclosure inside a settings section: the rare fields,
 * behind a native `<details>` that says how many there are. Never nested in
 * another `MoreOptions`.
 *
 * The chevron turns a quarter over 200ms ease-out; a reader who prefers
 * reduced motion gets the turn at once. `invalid` opens the group when a
 * field inside fails validation or needs attention, so a problem is never
 * hidden. The settings search opens it too (it opens any `<details>` around
 * its target).
 */
export function MoreOptions({
	count,
	label = "More options",
	defaultOpen = false,
	invalid = false,
	children,
	className,
}: {
	/** How many options are inside; shown as "More options (3)". */
	count?: number;
	/** The summary, for a group that is not "more options" (Host access). */
	label?: string;
	/** Starts open, for a group that is the point of the page for some readers. */
	defaultOpen?: boolean;
	/** Something inside has a problem: keep the group open. */
	invalid?: boolean;
	children: ReactNode;
	className?: string;
}) {
	const ref = useRef<HTMLDetailsElement>(null);
	useEffect(() => {
		if (invalid && ref.current) ref.current.open = true;
	}, [invalid]);
	return (
		<details
			ref={ref}
			open={defaultOpen}
			className={cn("group/more", className)}
		>
			<summary className="inline-flex cursor-pointer select-none list-none items-center gap-1 rounded-sm py-1 text-ink-2 text-sm outline-none marker:hidden hover:text-ink-1 focus-visible:focus-ring [&::-webkit-details-marker]:hidden">
				<ChevronRightIcon
					aria-hidden
					className="size-3.5 transition-transform duration-200 ease-out group-open/more:rotate-90 motion-reduce:transition-none"
				/>
				{label}
				{count === undefined ? "" : ` (${count})`}
			</summary>
			<div className="rule-rows mt-1 border-line-faint border-t">
				{children}
			</div>
		</details>
	);
}
