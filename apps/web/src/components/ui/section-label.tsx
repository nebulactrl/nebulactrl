import { cn } from "cn";
import type { ReactNode } from "react";

export interface SectionLabelProps {
	children: ReactNode;
	/** Right-aligned trailing content: a note, a segmented control, a button. */
	aside?: ReactNode;
	/** Heading level for the document outline; the look never changes. */
	as?: "h2" | "h3" | "h4" | "div";
	className?: string;
}

/** The muted sentence-case label over a group: "Deployments", "Scaling", "Source". */
export function SectionLabel({
	children,
	aside,
	as: Tag = "h3",
	className,
}: SectionLabelProps) {
	if (!aside) {
		return (
			<Tag data-slot="section-label" className={cn("type-label", className)}>
				{children}
			</Tag>
		);
	}
	return (
		<div
			data-slot="section-label"
			className={cn(
				"flex min-h-6 flex-wrap items-center gap-x-3 gap-y-2",
				className,
			)}
		>
			<Tag className="type-label">{children}</Tag>
			{/* Pushed right while it fits; on a narrow panel it drops under the
			    eyebrow, and its own items wrap rather than run past the edge. */}
			<div className="ml-auto flex max-w-full flex-wrap items-center justify-end gap-x-3 gap-y-2">
				{aside}
			</div>
		</div>
	);
}
