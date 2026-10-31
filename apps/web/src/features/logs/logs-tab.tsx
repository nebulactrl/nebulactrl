import { ArrowDownToLineIcon, Trash2Icon } from "lucide-react";
import { type ReactNode, useId, useState } from "react";
import type { Service } from "#/api/models";
import { type SseStatus, useLogStream } from "#/api/sse";
import { Button } from "#/components/ui/button";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import {
	hasProcesses,
	processFilterOptions,
	resolveProcessFilter,
} from "#/features/processes/model";
import { ProcessFilter } from "#/features/processes/process-filter";
import { useProcessParam } from "#/features/processes/url";
import { ComponentFilter } from "#/features/stacks/component-filter";
import { isStack, stackComponents } from "#/features/stacks/model";
import { useComponentParam, useStackNavigation } from "#/features/stacks/url";
import { CentralLogs } from "./central/central-logs";
import { DownloadLogsButton } from "./download/download-logs-button";
import { HistoryLogs } from "./history/history-logs";
import { LogsModeToggle } from "./history/logs-mode-toggle";
import { useLogsMode } from "./history/url";
import { LogView } from "./log-view";
import { LogSearchShell, NoMatches } from "./search/log-search-shell";
import { useLogSearch } from "./search/use-log-search";

const STATUS_TEXT: Record<SseStatus, string> = {
	connecting: "Connecting…",
	live: "Live",
	paused: "Reconnecting…",
	waiting: "Waiting for a free connection…",
	ended: "Stream ended",
};

const STATUS_KIND: Record<SseStatus, StatusKind> = {
	connecting: "building",
	live: "live",
	paused: "warning",
	waiting: "offline",
	ended: "offline",
};

/**
 * The service's Logs tab: the live tail, or (Live | History | Central logs)
 * what the server stored and what the team's log store holds. The choice lives
 * in the address as `?logs=history` or `?logs=central`.
 */
export function LogsTab({ service }: { service: Service }) {
	const [mode, setMode] = useLogsMode();
	const toggle = <LogsModeToggle mode={mode} onChange={setMode} central />;
	const picked = useComponentParam();
	const pickedProcess = useProcessParam();
	const { show } = useStackNavigation();
	const components = stackComponents(service);
	// A stale ?component= (renamed or removed) falls back to every component.
	const component = components.some((c) => c.name === picked)
		? picked
		: undefined;
	// Likewise a stale ?process= falls back to every process.
	const processes = processFilterOptions(service);
	const process = resolveProcessFilter(processes, pickedProcess);
	let chips: ReactNode = null;
	if (isStack(service)) {
		chips = (
			<ComponentFilter
				components={components}
				value={component}
				onChange={(next) => show({ component: next ?? null })}
			/>
		);
	} else if (hasProcesses(service)) {
		chips = (
			<ProcessFilter
				processes={processes}
				value={process}
				onChange={(next) => show({ process: next ?? null })}
			/>
		);
	}
	if (mode === "central") {
		return (
			<CentralLogs
				service={service}
				modeToggle={toggle}
				chips={chips}
				process={process}
			/>
		);
	}
	if (mode === "history") {
		return (
			<section
				data-tab="logs"
				data-service-id={service.id}
				aria-label="Logs"
				className="flex h-full min-h-[360px] flex-col gap-3 p-4"
			>
				{chips}
				<HistoryLogs
					scope={{ kind: "service", id: service.id, component, process }}
					servers={
						service.server
							? [{ id: service.server.id, name: service.server.name }]
							: []
					}
					modeToggle={toggle}
					label="Search log history"
					logLabel={`${service.name} stored logs`}
					facetKeys={["level", "stream", "replica", "status", "deployment"]}
					className="min-h-0 flex-1"
					bodyClassName="flex-1"
				/>
			</section>
		);
	}
	return (
		<LiveLogs
			service={service}
			modeToggle={toggle}
			chips={chips}
			component={component}
			process={process}
		/>
	);
}

/**
 * The service's runtime log: a search box with autocomplete and facets over a
 * virtualised live tail in an inset well, a Follow switch that also lets go
 * when you scroll up, and a result count. The section fills the tab body so
 * the viewer takes the height.
 */
function LiveLogs({
	service,
	modeToggle,
	chips,
	component,
	process,
}: {
	service: Service;
	modeToggle: ReactNode;
	chips: ReactNode;
	component: string | undefined;
	process: string | undefined;
}) {
	const [follow, setFollow] = useState(true);
	const followId = useId();
	const stream = useLogStream({
		kind: "service",
		id: service.id,
		tail: 200,
		component,
		process,
	});
	const search = useLogSearch({ lines: stream.lines });

	let empty: ReactNode = "No log output yet.";
	if (search.total > 0 && search.settled.input.trim() !== "") {
		empty = <NoMatches query={search.settled.input} onClear={search.clear} />;
	} else if (
		stream.status === "connecting" ||
		stream.status === "paused" ||
		stream.status === "waiting"
	) {
		empty = "Waiting for log output…";
	}

	return (
		<section
			data-tab="logs"
			data-service-id={service.id}
			aria-label="Logs"
			className="flex h-full min-h-[360px] flex-col gap-3 p-4"
		>
			{chips}
			<LogSearchShell
				search={search}
				label="Search logs"
				facetKeys={["level", "stream", "replica", "status"]}
				className="min-h-0 flex-1"
				bodyClassName="flex-1"
				toolbar={
					<>
						<div className="flex h-8 items-center">{modeToggle}</div>
						<div className="flex h-8 items-center gap-2 text-ink-3 text-xs">
							<Switch
								id={followId}
								size="sm"
								checked={follow}
								onCheckedChange={setFollow}
							/>
							<label htmlFor={followId} className="cursor-pointer">
								Follow
							</label>
						</div>
						<Button
							variant="secondary"
							size="sm"
							title="Clear the view (the container's logs are untouched)"
							onClick={stream.clear}
						>
							<Trash2Icon aria-hidden />
							Clear
						</Button>
						<DownloadLogsButton
							target={{ kind: "service", id: service.id }}
							component={component}
							process={process}
							label="Download"
						/>
					</>
				}
			>
				<LogView
					className="surface-well absolute inset-0 rounded-card"
					lines={search.visible}
					terms={search.terms}
					showLevel
					follow={follow}
					onLetGo={() => setFollow(false)}
					label={`${service.name} runtime logs`}
					empty={empty}
				/>
				{!follow && search.visible.length > 0 ? (
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
			</LogSearchShell>

			<footer
				className="flex items-center gap-2 text-ink-3 text-xs"
				aria-live="polite"
			>
				<StatusIcon kind={STATUS_KIND[stream.status]} label="" />
				<span>{STATUS_TEXT[stream.status]}</span>
			</footer>
		</section>
	);
}
