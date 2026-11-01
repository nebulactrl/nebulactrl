import { Segmented } from "#/components/ui/segmented";
import { LiveBadge } from "./live-badge";

export interface RangeControlProps<T extends string> {
	value: T;
	onChange: (value: T) => void;
	ranges: readonly T[];
	live: boolean;
	delayed?: boolean;
	"aria-label"?: string;
}

/**
 * The range segmented control (sliding indicator) with the Live cue beside
 * it. Which ranges are live is the caller's call (`isLiveRange`), so the
 * same control serves resource charts and traffic.
 */
export function RangeControl<T extends string>({
	value,
	onChange,
	ranges,
	live,
	delayed,
	...props
}: RangeControlProps<T>) {
	return (
		<div className="flex flex-wrap items-center gap-3">
			<Segmented
				aria-label={props["aria-label"] ?? "Time range"}
				value={value}
				onValueChange={onChange}
				options={ranges.map((range) => ({ value: range, label: range }))}
			/>
			<LiveBadge live={live} delayed={delayed ?? false} />
		</div>
	);
}
