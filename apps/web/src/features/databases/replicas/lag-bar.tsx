import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import { cn } from "cn";
import type { ReplicaStateView } from "#/api/gen/types.gen";
import { lagPercent, lagText } from "./model";

/**
 * A replica's lag: the dithered bar of the app tier (calm, static) filled
 * toward the lagging line, with the figures as solid text beside it. A reading
 * that was never taken draws no bar, only the sentence; a stale one is dimmed.
 * The bar is data, never a loader: nothing here moves while there is no reading.
 */
export function LagBar({
	lag,
	stale,
	className,
}: {
	lag: Pick<ReplicaStateView, "lagBytes" | "replayLagMs">;
	stale: boolean;
	className?: string;
}) {
	const text = lagText(lag);
	if (lag.lagBytes == null) {
		return <span className={cn("text-2xs text-ink-3", className)}>{text}</span>;
	}
	return (
		<span className={cn("flex min-w-0 items-center gap-2.5", className)}>
			<span className="block min-w-0 flex-1">
				<DitherProgressBar
					inline
					hideLabel
					size="sm"
					label="Replication lag"
					value={lagPercent(lag.lagBytes)}
					valueText={text}
					warnAt={25}
					badAt={100}
					stale={stale}
				/>
			</span>
			<span
				className={cn(
					"w-44 shrink-0 text-right font-mono text-2xs tabular-nums",
					stale ? "text-ink-3" : "text-ink-2",
				)}
			>
				{text}
			</span>
		</span>
	);
}
