import { cn } from "cn";
import type { ReactNode } from "react";
import { KpiNumber } from "#/components/common/kpi-number";
import { ToneIcon } from "#/components/common/tone-icon";
import { EYEBROW } from "./eyebrow";

export interface MiniStatProps {
	label: string;
	/** The formatted number; it resolves from dither once, then pops in when this text changes. */
	text: string;
	/** Small line under the number: "of 512 MiB", "peak 41%". */
	hint?: ReactNode;
	/** `warning` marks the tile with the warning icon and wash and tints the number. */
	tone?: "default" | "warning";
	className?: string;
}

/** A "now" tile: raised card, mono eyebrow, big tabular number with pop-in, hint line. */
export function MiniStat({
	label,
	text,
	hint,
	tone = "default",
	className,
}: MiniStatProps) {
	return (
		<div
			data-tone={tone === "warning" ? "warning" : undefined}
			className={cn(
				"surface-raised flex min-w-0 flex-col gap-2.5 rounded-card px-3.5 py-3",
				tone === "warning" && "tone-callout relative",
				className,
			)}
		>
			<span className="flex min-w-0 items-center gap-1.5">
				{tone === "warning" ? <ToneIcon tone="warning" /> : null}
				<span className={cn(EYEBROW, "truncate")}>{label}</span>
			</span>
			<span
				className={cn(
					"type-metric truncate text-ink-1",
					tone === "warning" && "text-status-staged-on",
				)}
			>
				<KpiNumber text={text} />
			</span>
			{hint ? (
				<span className="truncate text-ink-3 text-xs leading-none">{hint}</span>
			) : null}
		</div>
	);
}
