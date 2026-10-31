import { cn } from "cn";
import type { Service } from "#/api/models";
import { useLogStream } from "#/api/sse";
import { logLevel } from "./model";

const RECENT_LINES = 5;

const LEVEL_CLASS = {
	info: "text-info",
	warn: "text-warn",
	error: "text-bad",
} as const;

function clock(iso: string | undefined): string {
	if (!iso) return "";
	const date = new Date(iso);
	return Number.isNaN(date.getTime())
		? ""
		: date.toLocaleTimeString([], { hour12: false });
}

/**
 * The last five runtime lines in an inset well, fed by
 * the same service log stream as the Logs tab. Nothing is invented: a service
 * with no output says so.
 */
export function RecentLogs({ service }: { service: Service }) {
	const stream = useLogStream({
		kind: "service",
		id: service.id,
		tail: RECENT_LINES,
		maxLines: RECENT_LINES,
		enabled: service.currentDeploymentId !== null,
	});

	const lines = stream.lines.slice(-RECENT_LINES);
	let empty = "No log output yet.";
	if (service.currentDeploymentId === null) {
		empty = "Logs appear once the service is deployed.";
	} else if (
		stream.status === "connecting" ||
		stream.status === "paused" ||
		stream.status === "waiting"
	) {
		empty = "Waiting for log output…";
	}

	return (
		<div
			role="log"
			aria-label="Recent log lines"
			className="surface-well overflow-hidden rounded-card px-3 py-2.5 font-mono text-[11px] text-ink-3 leading-[1.55]"
		>
			{lines.length === 0 ? (
				<p className="font-sans text-ink-4 text-xs">{empty}</p>
			) : (
				lines.map((entry) => {
					const level = logLevel(entry.line);
					return (
						<div key={entry.id} className="flex gap-2 whitespace-nowrap">
							{entry.ts ? (
								<span className="shrink-0 text-ink-4">{clock(entry.ts)}</span>
							) : null}
							<span
								title={entry.line}
								className={cn("truncate", level && LEVEL_CLASS[level])}
							>
								{entry.line}
							</span>
						</div>
					);
				})
			)}
		</div>
	);
}
