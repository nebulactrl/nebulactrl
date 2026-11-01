import { cn } from "cn";
import { ChevronDownIcon } from "lucide-react";

/** The chevron after a top-bar menu trigger. */
export function MenuCaret({ className }: { className?: string }) {
	return (
		<ChevronDownIcon
			aria-hidden
			className={cn("size-3.5 shrink-0 text-ink-3", className)}
		/>
	);
}
