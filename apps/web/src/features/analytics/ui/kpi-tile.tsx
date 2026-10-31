import { DitherStatCard } from "@nebulactrl/dither/charts/stat-card";
import { useMemo } from "react";
import { KpiNumber } from "#/components/common/kpi-number";
import type { Delta } from "../delta";
import { sparkPoints } from "../series";

export interface KpiTileProps {
	label: string;
	/** Formatted number; it resolves from dither once, then pops in when this text changes. */
	text: string;
	/** Comparison against the previous period, when `compare=prev` was asked. */
	delta?: Delta | undefined;
	/** Tooltip/aria text for the delta: "vs previous 24h". */
	deltaLabel?: string | undefined;
	spark?: readonly number[] | null | undefined;
	/** A token reference for the sparkline. */
	color?: string;
	/** Under the number: "per UTC day", "p50 21 ms". */
	hint?: string;
	className?: string;
}

/**
 * One headline number on a raised card: mono eyebrow, big tabular value with
 * pop-in, a delta chip (arrow icon + percent in text, so colour is never the
 * only cue) and a dithered trend along the bottom edge. The number and delta
 * are text; only the trend is texture.
 */
export function KpiTile({
	label,
	text,
	delta,
	deltaLabel,
	spark,
	color = "var(--color-accent-ink)",
	hint,
	className,
}: KpiTileProps) {
	const trend = useMemo(
		() => (spark ? sparkPoints(spark).map((p) => p.v) : null),
		[spark],
	);
	const secondary = hint ?? (delta && deltaLabel ? deltaLabel : undefined);
	return (
		<DitherStatCard
			label={label}
			value={text}
			valueNode={<KpiNumber text={text} />}
			{...(delta
				? {
						delta: {
							text: delta.text,
							tone: delta.tone,
							...(deltaLabel ? { title: deltaLabel } : {}),
						},
					}
				: {})}
			{...(secondary ? { hint: secondary } : {})}
			trend={trend}
			color={color}
			{...(className ? { className } : {})}
		/>
	);
}
