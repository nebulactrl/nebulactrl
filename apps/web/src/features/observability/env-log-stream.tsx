import { cn } from "cn";
import {
	ArrowDownToLineIcon,
	PauseIcon,
	PlayIcon,
	Trash2Icon,
} from "lucide-react";
import { type ReactNode, useCallback, useMemo, useState } from "react";
import type { Service } from "#/api/models";
import { type LogLine, type SseStatus, useLogStream } from "#/api/sse";
import { Button } from "#/components/ui/button";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { Toggle } from "#/components/ui/toggle";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { HistoryLogs } from "#/features/logs/history/history-logs";
import { LogsModeToggle } from "#/features/logs/history/logs-mode-toggle";
import { useLogsMode } from "#/features/logs/history/url";
import { LogView } from "#/features/logs/log-view";
import {
	LogSearchShell,
	NoMatches,
} from "#/features/logs/search/log-search-shell";
import { useLogSearch } from "#/features/logs/search/use-log-search";

/** What the footer shows for each stream state: the icon beside the words. */
const STREAM_KIND: Record<SseStatus, StatusKind> = {
	connecting: "building",
	live: "live",
	paused: "warning",
	waiting: "stopped",
	ended: "stopped",
};

const STATUS_TEXT: Record<SseStatus, string> = {
	connecting: "Connecting…",
	live: "Live",
	paused: "Reconnecting…",
	waiting: "Waiting for a free connection…",
	ended: "Stream ended",
};

export interface EnvLogStreamProps {
	environmentId: string;
	services: readonly Service[];
	className?: string;
}

/**
 * The service icon beside a line of the merged log, for the services of the
 * environment (a line from a removed service has none).
 */
function useServiceIcon(services: readonly Service[]) {
	const serviceOf = useMemo(
		() => new Map(services.map((service) => [service.id, service])),
		[services],
	);
	return useCallback(
		(line: LogLine): ReactNode => {
			const owner = serviceOf.get(line.serviceId ?? "");
			return owner ? (
				<ServiceIcon service={owner} size={14} className="shrink-0" />
			) : null;
		},
		[serviceOf],
	);
}

/** The servers the environment's services run on, once each. */
function serversOf(
	services: readonly Service[],
): { id: string; name: string }[] {
	const byId = new Map<string, string>();
	for (const service of services) {
		if (service.server) byId.set(service.server.id, service.server.name);
	}
	return [...byId].map(([id, name]) => ({ id, name }));
}

/**
 * The environment's merged runtime log, live (the default) or, with
 * `?logs=history`, what the servers stored. Both share the search box, the
 * facets and the virtualised list.
 */
export function EnvLogStream(props: EnvLogStreamProps) {
	const [mode, setMode] = useLogsMode();
	// An environment has no central view: a shared `?logs=central` reads as live.
	const toggle = (
		<LogsModeToggle
			mode={mode === "central" ? "live" : mode}
			onChange={setMode}
		/>
	);
	const serviceIcon = useServiceIcon(props.services);
	if (mode === "history") {
		return (
			<div className={cn("flex min-h-0 flex-col gap-3", props.className)}>
				<HistoryLogs
					scope={{ kind: "environment", id: props.environmentId }}
					servers={serversOf(props.services)}
					modeToggle={toggle}
					label="Search environment log history"
					logLabel="Environment stored logs"
					facetKeys={[
						"level",
						"service",
						"stream",
						"replica",
						"status",
						"deployment",
					]}
					skipBlank
					showService
					serviceIcon={serviceIcon}
					bodyClassName="h-[min(60vh,480px)] min-h-[320px]"
				/>
				<p className="text-ink-4 text-xs leading-snug">
					Runtime output only (stdout and stderr), secret values masked. HTTP
					access lines are not stored.
				</p>
			</div>
		);
	}
	return (
		<LiveEnvLog {...props} serviceIcon={serviceIcon} modeToggle={toggle} />
	);
}

/**
 * The live tail: one SSE stream for every service, a search box with
 * autocomplete and facets (service, level, stream, replica, status) over a
 * virtualised list, follow and pause. A chatty service never costs a DOM node
 * per line.
 */
function LiveEnvLog({
	environmentId,
	className,
	serviceIcon,
	modeToggle,
}: EnvLogStreamProps & {
	serviceIcon: (line: LogLine) => ReactNode;
	modeToggle: ReactNode;
}) {
	const [follow, setFollow] = useState(true);
	const [streaming, setStreaming] = useState(true);
	const stream = useLogStream({
		kind: "environment",
		id: environmentId,
		tail: 200,
		enabled: streaming,
		maxLines: 5000,
	});
	// Blank lines are spacing the program printed; as rows they would carry
	// only a timestamp and a service name.
	const search = useLogSearch({
		lines: stream.lines,
		skipBlank: true,
		epoch: stream.epoch,
	});

	let empty: ReactNode =
		"Waiting for output. Lines appear here as your services write them.";
	if (search.total > 0 && search.settled.input.trim() !== "") {
		empty = <NoMatches query={search.settled.input} onClear={search.clear} />;
	} else if (stream.status !== "live") {
		empty = STATUS_TEXT[stream.status];
	}

	return (
		<div className={cn("flex min-h-0 flex-col gap-3", className)}>
			<LogSearchShell
				search={search}
				label="Search environment logs"
				facetKeys={["level", "service", "stream", "replica", "status"]}
				bodyClassName="h-[min(60vh,480px)] min-h-[320px]"
				toolbar={
					<>
						<div className="flex h-8 items-center">{modeToggle}</div>
						<Toggle
							variant="outline"
							pressed={follow}
							onPressedChange={setFollow}
							className="h-8 gap-1.5 text-xs"
						>
							<ArrowDownToLineIcon />
							Follow
						</Toggle>
						<Button
							variant="secondary"
							onClick={() => setStreaming((on) => !on)}
							aria-pressed={!streaming}
						>
							{streaming ? <PauseIcon /> : <PlayIcon />}
							{streaming ? "Pause" : "Resume"}
						</Button>
						<Button variant="ghost" onClick={stream.clear}>
							<Trash2Icon />
							Clear
						</Button>
					</>
				}
			>
				<LogView
					className="surface-well absolute inset-0 rounded-card"
					lines={search.visible}
					terms={search.terms}
					showLevel
					showService
					serviceIcon={serviceIcon}
					follow={follow}
					onLetGo={() => setFollow(false)}
					label="Environment logs"
					empty={empty}
				/>
			</LogSearchShell>
			<p className="flex items-center gap-2 font-mono text-2xs text-ink-3">
				<StatusIcon
					kind={streaming ? STREAM_KIND[stream.status] : "stopped"}
					label=""
				/>
				{streaming ? STATUS_TEXT[stream.status] : "Paused"}
			</p>
			<p className="text-ink-4 text-xs leading-snug">
				Runtime output only (stdout and stderr), secret values masked. HTTP
				access lines are not stored.
			</p>
		</div>
	);
}
