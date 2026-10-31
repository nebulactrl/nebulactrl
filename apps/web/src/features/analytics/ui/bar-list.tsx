import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import { cn } from "cn";
import type { ReactNode } from "react";
import { compactNumber, formatMs } from "#/features/metrics/chart-format";
import { type RankedRow, rankRows } from "../lists";
import type { TopItem } from "../types";

export interface BarListProps {
	items: readonly TopItem[] | null | undefined;
	/** Cap on rows shown; the rest stay in the data. */
	limit?: number;
	/** Shown instead of the list when it has no rows. */
	empty?: ReactNode;
	/** Make rows clickable (filter by this value). */
	onSelect?: (key: string) => void;
	/** Print per-row 5xx counts and p95 (paths). */
	detail?: boolean;
	/** Label for a row whose key is the empty string. */
	blankLabel?: string;
	/** Fill tint: a token reference. Without it the fill is the lit accent track. */
	color?: string;
	className?: string;
}

function Row({
	row,
	detail,
	blankLabel,
	color,
	onSelect,
}: {
	row: RankedRow;
	detail: boolean;
	blankLabel: string;
	color: string | undefined;
	onSelect: ((key: string) => void) | undefined;
}) {
	const label = row.k === "" ? blankLabel : row.k;
	const content = (
		<>
			<span className="flex w-full min-w-0 items-center gap-2">
				<span
					title={label}
					className="min-w-0 flex-1 truncate font-mono text-ink-1 text-xs"
				>
					{label}
				</span>
				{detail && (row.s5xx ?? 0) > 0 ? (
					<span className="shrink-0 font-mono text-2xs text-status-failed-on">
						{compactNumber(row.s5xx ?? 0)} 5xx
					</span>
				) : null}
				{detail && row.avgMs ? (
					<span className="hidden shrink-0 font-mono text-2xs text-ink-4 sm:inline">
						avg {formatMs(row.avgMs)}
					</span>
				) : null}
				<span className="w-12 shrink-0 text-right font-medium font-mono text-ink-1 text-xs tabular-nums">
					{compactNumber(row.n)}
				</span>
				<span className="w-10 shrink-0 text-right font-mono text-2xs text-ink-3 tabular-nums">
					{row.pct >= 10 ? row.pct.toFixed(0) : row.pct.toFixed(1)}%
				</span>
			</span>
			<span aria-hidden className="block w-full">
				<DitherProgressBar
					inline
					hideLabel
					size="sm"
					label={label}
					value={row.width}
					color={color ?? "ink"}
				/>
			</span>
		</>
	);
	const base =
		"group/row flex w-full flex-col gap-1.5 rounded-ctl px-2 py-1.5 text-left";
	return onSelect ? (
		<button
			type="button"
			onClick={() => onSelect(row.k)}
			className={cn(
				base,
				"cursor-pointer outline-none transition-[background-color] duration-quick hover:bg-white/5 focus-visible:focus-ring",
			)}
		>
			{content}
		</button>
	) : (
		<div className={base}>{content}</div>
	);
}

/**
 * A ranked list: label, count and share in text above a lit bar in an engraved
 * groove. Bar width is relative to the biggest row (never below 2%).
 */
export function BarList({
	items,
	limit = 8,
	empty,
	onSelect,
	detail = false,
	blankLabel = "(none)",
	color,
	className,
}: BarListProps) {
	const rows = rankRows(items).slice(0, limit);
	if (rows.length === 0) {
		return (
			<p className="px-1 py-3 text-ink-3 text-xs">
				{empty ?? "Nothing in this range."}
			</p>
		);
	}
	return (
		<ul className={cn("flex flex-col gap-0.5", className)}>
			{rows.map((row) => (
				<li key={row.k}>
					<Row
						row={row}
						detail={detail}
						blankLabel={blankLabel}
						color={color}
						onSelect={onSelect}
					/>
				</li>
			))}
		</ul>
	);
}
