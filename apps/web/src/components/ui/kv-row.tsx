import { cn } from "cn";
import type { ReactNode } from "react";

export interface KvRowProps {
	/** What the setting is. */
	label: ReactNode;
	/** Why it matters or what it does, in one line. */
	description?: ReactNode;
	/** Id of the control this row labels, so the label becomes its <label>. */
	htmlFor?: string;
	/** The value or the control. */
	children: ReactNode;
	className?: string;
	/** Which side carries the hairline: `bottom` (default) or `top`, for stacks that open with a rule under their heading. */
	rule?: "top" | "bottom";
}

/**
 * One settings row in a sheet: label and a line of why on the left (200px
 * at most), the control on the right. Rows stack under a `SectionLabel` and
 * divide themselves with a hairline below, as the design draws them.
 */
export function KvRow({
	label,
	description,
	htmlFor,
	children,
	className,
	rule = "bottom",
}: KvRowProps) {
	return (
		<div
			data-slot="kv-row"
			className={cn(
				"grid grid-cols-1 items-center gap-3 border-line py-3.5 sm:grid-cols-[minmax(0,200px)_minmax(0,1fr)] sm:gap-6",
				rule === "top" ? "border-t" : "border-b",
				className,
			)}
		>
			<span className="flex min-w-0 flex-col gap-1">
				{htmlFor ? (
					<label
						htmlFor={htmlFor}
						className="font-medium text-ink-1 text-sm leading-[1.2]"
					>
						{label}
					</label>
				) : (
					<span className="font-medium text-ink-1 text-sm leading-[1.2]">
						{label}
					</span>
				)}
				{description ? (
					<span className="text-ink-3 text-xs leading-[1.4]">
						{description}
					</span>
				) : null}
			</span>
			<div className="flex min-w-0 flex-wrap items-center gap-2.5">
				{children}
			</div>
		</div>
	);
}
