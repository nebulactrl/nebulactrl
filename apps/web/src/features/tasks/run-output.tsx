import { cn } from "cn";
import { ArrowDownToLineIcon } from "lucide-react";
import { useId, useState } from "react";
import { useLogStream } from "#/api/sse";
import { Button } from "#/components/ui/button";
import { Switch } from "#/components/ui/switch";
import { LogView } from "#/features/logs/log-view";
import { count } from "#/lib/format";

/**
 * The output of one run: stored lines first, then live ones while it runs
 * (the stream ends on its own when the run finishes). A Follow switch keeps
 * the newest line in view and lets go when you scroll up. Mount it with
 * `key={runId}` so each run starts a fresh stream that follows again.
 */
export function RunOutput({
	runId,
	active,
	label,
	className,
}: {
	runId: string;
	/** The run has not finished: the output is still arriving. */
	active: boolean;
	label: string;
	className?: string;
}) {
	const [follow, setFollow] = useState(true);
	const followId = useId();
	const stream = useLogStream({ kind: "taskRun", id: runId });
	const waiting =
		stream.status === "connecting" ||
		stream.status === "paused" ||
		stream.status === "waiting";

	return (
		<div className={cn("flex min-h-0 flex-col gap-2", className)}>
			<div className="flex items-center gap-3 text-ink-3 text-xs">
				<span className="font-mono tabular-nums">
					{count(stream.lines.length, "line")}
				</span>
				<span aria-live="polite">
					{active ? "Live" : waiting ? "Loading…" : "Finished"}
				</span>
				<span className="ml-auto flex items-center gap-2">
					<Switch
						id={followId}
						size="sm"
						checked={follow}
						onCheckedChange={setFollow}
					/>
					<label htmlFor={followId} className="cursor-pointer">
						Follow
					</label>
				</span>
			</div>
			<div className="relative min-h-[200px] flex-1">
				<LogView
					className="surface-well absolute inset-0 rounded-card"
					lines={stream.lines}
					follow={follow}
					onLetGo={() => setFollow(false)}
					label={label}
					empty={
						waiting || active
							? "Waiting for output…"
							: "This run printed nothing."
					}
				/>
				{!follow && stream.lines.length > 0 ? (
					<Button
						variant="secondary"
						size="sm"
						className="absolute right-4 bottom-3 shadow-pop"
						onClick={() => setFollow(true)}
					>
						<ArrowDownToLineIcon aria-hidden />
						Jump to latest
					</Button>
				) : null}
			</div>
		</div>
	);
}
