import { useMemo } from "react";
import type { AreaRevealShapeProps } from "recharts";
import { linePath, sampleCurve } from "../../lib/chart-math";
import { MIN_DITHER_CELL, useForcedColors } from "../../lib/prefs";
import { fadeDepth, runFloor, splitRuns } from "../../lib/recharts-math";
import { AreaFill } from "./area-fill";
import type { FillVariant } from "./shared";

export interface DitherAreaShapeProps extends AreaRevealShapeProps {
	/** Ink and line colour (a CSS colour or `var(--token)`). Default: the Area's `stroke`. */
	color?: string;
	variant?: FillVariant;
	/** Dither cell in px. Default 3. */
	cell?: number;
	/** Tone right under the line. Default 0.72. */
	level?: number;
	/** Bands in a gradient. Default 7. */
	bands?: number;
	/** Longest fade in px. Default 110. */
	maxDepth?: number;
}

/**
 * A Recharts `<Area shape>` that draws the fill as dither and the line crisp
 * on top. Pass it as an element so it can use hooks:
 *
 * ```tsx
 * <Area dataKey="rps" stroke={color} shape={<DitherAreaShape color={color} />} />
 * ```
 *
 * Recharts keeps owning the scales, axes, tooltip, cursor and active dot; the
 * shape only replaces the paint. Gaps in the data split the fill into runs,
 * stacked series fill between their own upper and lower edge, and the
 * patterns are shared page-wide, so a refetch redraws path data only.
 */
export function DitherAreaShape({
	points,
	baseLine,
	type,
	stroke,
	strokeWidth = 1.5,
	color,
	variant = "gradient",
	cell = 3,
	level = 0.72,
	bands = 7,
	maxDepth = 110,
}: DitherAreaShapeProps) {
	const forced = useForcedColors();
	const ink =
		color ?? (typeof stroke === "string" ? stroke : "var(--dither-ink)");
	const curve = type === "linear" || type === "step" ? "linear" : "monotone";
	const runs = useMemo(() => {
		return splitRuns(points ?? [], baseLine).map((run) => {
			const top = sampleCurve(run.top, curve, 3);
			const bottom =
				typeof run.bottom === "number"
					? run.bottom
					: sampleCurve(run.bottom, curve, 3);
			return {
				top,
				bottom,
				floor: runFloor(run),
				depth: fadeDepth(run, maxDepth),
				d: linePath(top),
			};
		});
	}, [points, baseLine, curve, maxDepth]);
	const solid = forced || cell < MIN_DITHER_CELL;
	return (
		<g>
			{runs.map((run) => (
				<AreaFill
					key={`${run.top[0]?.x}-${run.top.length}`}
					top={run.top}
					bottom={run.bottom}
					color={ink}
					variant={variant}
					cell={cell}
					depth={run.depth}
					floor={run.floor}
					level={level}
					bands={bands}
					solid={solid}
				/>
			))}
			{runs.map((run) => (
				<path
					key={`line-${run.top[0]?.x}-${run.top.length}`}
					d={run.d}
					fill="none"
					stroke={ink}
					strokeWidth={strokeWidth}
					strokeLinejoin="round"
					strokeLinecap="round"
				/>
			))}
		</g>
	);
}
