import { useMemo } from "react";
import { rangeMs } from "./ranges";
import type { TimeDomain } from "./ui/time-chart";

/**
 * The x domain for a range: it ends when the data arrived, so a live chart
 * slides left on every refetch, and reaches `range` back from there. Keyed
 * on `updatedAt`, not on render time, so re-renders never move the axis.
 */
export function useChartDomain(
	range: string,
	updatedAt: number,
	stepSeconds: number,
): TimeDomain {
	return useMemo(() => {
		const to = updatedAt > 0 ? updatedAt : Date.now();
		return { from: to - rangeMs(range), to, stepSeconds };
	}, [range, updatedAt, stepSeconds]);
}
