import { DitherHeatmap } from "@nebulactrl/dither/charts/heatmap";
import { useMemo } from "react";
import { heatmapMax } from "../lists";

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"] as const;
const HOURS = Array.from({ length: 24 }, (_, hour) =>
	String(hour).padStart(2, "0"),
);

export interface ActiveHoursProps {
	/** 7 weekdays (Sunday first) × 24 UTC hours of requests. */
	grid: readonly (readonly number[] | null)[];
}

/**
 * Hour × weekday grid of when traffic arrives, in UTC. Each square steps from
 * a few dots to solid with the request count relative to the busiest cell;
 * every cell also carries its exact count in a readout, and a "less … more"
 * ramp explains the scale.
 */
export function ActiveHours({ grid }: ActiveHoursProps) {
	const max = heatmapMax(grid);
	const values = useMemo(
		() =>
			WEEKDAYS.map((_, day) => HOURS.map((_, hour) => grid[day]?.[hour] ?? 0)),
		[grid],
	);
	return (
		<div className="flex flex-col gap-2">
			<DitherHeatmap
				layout="matrix"
				label="Requests by weekday and UTC hour"
				rows={WEEKDAYS}
				columns={HOURS}
				values={values}
				max={max}
				unit="requests"
				color="ink"
				cellSize={16}
			/>
			<p className="text-right font-mono text-2xs text-ink-4">
				busiest hour {max.toLocaleString()} requests
			</p>
		</div>
	);
}
