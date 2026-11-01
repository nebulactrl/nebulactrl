import { cn } from "cn";
import type * as React from "react";

/**
 * The centred card on sign-in and onboarding: the modal material (the top of
 * the ladder) at panel radius, over the nebula shader. Opaque, no blur; it
 * lands with the shared 160ms rise and stays still for reduced motion.
 */
export function PanelCard({
	className,
	...props
}: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="panel-card"
			className={cn(
				"surface-modal flex flex-col gap-4 rounded-panel p-8 text-ink-1 motion-safe:animate-nc-in",
				className,
			)}
			{...props}
		/>
	);
}
