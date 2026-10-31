import { cn } from "cn";
import type { ReactNode } from "react";

/**
 * A read-only fact about a setting (which strategy the next deploy uses, where
 * the image is built), as a sentence. It is not a setting, so it has no row,
 * no label and no control. `id` is the anchor the settings search jumps to.
 */
export function FactLine({
	children,
	id,
	className,
}: {
	children: ReactNode;
	id?: string;
	className?: string;
}) {
	return (
		<p
			id={id}
			className={cn("m-0 text-[13px] text-ink-2 leading-[1.5]", className)}
		>
			{children}
		</p>
	);
}
