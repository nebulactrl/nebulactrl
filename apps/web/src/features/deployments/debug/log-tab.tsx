import { cn } from "cn";
import { ArrowDownToLineIcon, SearchIcon } from "lucide-react";
import { useId, useMemo, useState } from "react";
import type { LogStream, SseStatus } from "#/api/sse";
import { Button } from "#/components/ui/button";
import { Input } from "#/components/ui/input";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import type { LogLevel } from "#/features/logs/level";
import { LogView } from "#/features/logs/log-view";
import {
	DEBUG_LEVELS,
	DEBUG_STREAMS,
	filterDebugLines,
	highlightWords,
	NO_FILTER,
	streamCounts,
	toggled,
} from "./debug-lines";

const STREAM_LABEL: Record<string, string> = {
	build: "Build",
	deploy: "Deploy",
	runtime: "Runtime",
	debug: "Debug",
};

const LEVEL_LABEL: Record<string, string> = {
	error: "Error",
	warn: "Warn",
	info: "Info",
	debug: "Debug",
};

/**
 * The canvas's filter chip (features/canvas/filter-chips): a 24px pill, the
 * pressed one tinted with the accent. The count is a separate quiet number and
 * is dimmed at zero, where the chip stays selectable.
 */
function FilterChip({
	pressed,
	count,
	onClick,
	children,
}: {
	pressed: boolean;
	count?: number;
	onClick(): void;
	children: string;
}) {
	return (
		<button
			type="button"
			aria-pressed={pressed}
			onClick={onClick}
			className={cn(
				"flex h-6 shrink-0 cursor-pointer items-center gap-1.5 rounded-full border px-2.5 font-medium text-[12px] leading-none transition-[color,background-color,border-color,scale] duration-(--press-duration) ease-out focus-visible:focus-ring not-disabled:active:scale-(--press-scale)",
				pressed
					? "border-primary-hairline bg-[color-mix(in_oklab,var(--color-surface-float),var(--color-accent-ink)_16%)] text-foreground"
					: "border-line bg-surface-float text-muted-foreground hover:border-line-strong hover:text-foreground",
				count === 0 && "opacity-60",
			)}
		>
			{children}
			{count === undefined ? null : (
				<span className="font-normal text-ink-3 tabular-nums">{count}</span>
			)}
		</button>
	);
}

const STATUS_TEXT: Record<SseStatus, string> = {
	connecting: "Connecting…",
	live: "Following live",
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
 * Every stream of the deployment in one log: build, deploy, runtime and the
 * debug stream the ordinary log hides. Filter by stream and level, search by
 * words. It follows live while the deployment runs; the stream itself is the
 * panel's (the tab holds two live streams at most), so switching tabs keeps it.
 */
export function LogTab({
	stream,
	running,
}: {
	stream: LogStream;
	running: boolean;
}) {
	const [streams, setStreams] = useState<string[]>([...DEBUG_STREAMS]);
	const [levels, setLevels] = useState<string[]>([...DEBUG_LEVELS]);
	const [query, setQuery] = useState("");
	const [follow, setFollow] = useState(true);
	const followId = useId();
	const searchId = useId();

	const counts = useMemo(() => streamCounts(stream.lines), [stream.lines]);
	const visible = useMemo(
		() =>
			filterDebugLines(stream.lines, {
				streams: new Set(streams),
				levels: new Set(levels as LogLevel[]),
				query,
			}),
		[stream.lines, streams, levels, query],
	);
	const terms = useMemo(() => highlightWords(query), [query]);
	const filtered =
		streams.length < NO_FILTER.streams.size ||
		levels.length < NO_FILTER.levels.size ||
		query.trim() !== "";
	const waiting =
		running ||
		stream.status === "connecting" ||
		stream.status === "waiting" ||
		stream.status === "paused";

	return (
		<section
			aria-label="Debug log"
			className="flex h-full min-h-[360px] flex-col gap-3 p-4"
		>
			<div className="flex flex-wrap items-center gap-x-4 gap-y-2">
				<div className="relative min-w-[200px] flex-1">
					<SearchIcon
						aria-hidden
						className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-ink-3"
					/>
					<Input
						id={searchId}
						type="search"
						value={query}
						onChange={(event) => setQuery(event.target.value)}
						placeholder="Search the log (all words must match)"
						aria-label="Search the debug log"
						className="h-8 pl-8"
					/>
				</div>
				<fieldset className="m-0 flex min-w-0 flex-wrap items-center gap-1.5 border-0 p-0">
					<legend className="sr-only">Streams</legend>
					{DEBUG_STREAMS.map((name) => (
						<FilterChip
							key={name}
							pressed={streams.includes(name)}
							count={counts[name] ?? 0}
							onClick={() => setStreams([...toggled(new Set(streams), name)])}
						>
							{STREAM_LABEL[name] ?? name}
						</FilterChip>
					))}
				</fieldset>
				<span aria-hidden className="h-4 w-px shrink-0 bg-border" />
				<fieldset className="m-0 flex min-w-0 flex-wrap items-center gap-1.5 border-0 p-0">
					<legend className="sr-only">Levels</legend>
					{DEBUG_LEVELS.map((name) => (
						<FilterChip
							key={name}
							pressed={levels.includes(name)}
							onClick={() => setLevels([...toggled(new Set(levels), name)])}
						>
							{LEVEL_LABEL[name] ?? name}
						</FilterChip>
					))}
				</fieldset>
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
			</div>

			<div className="relative min-h-0 flex-1">
				<LogView
					className="surface-well absolute inset-0 rounded-card"
					lines={visible}
					terms={terms}
					follow={follow && running}
					onLetGo={() => setFollow(false)}
					label="Deployment debug log"
					showStream
					showLevel
					empty={
						filtered && stream.lines.length > 0 ? (
							<span>
								No line matches the filters.{" "}
								<Button
									variant="link"
									onClick={() => {
										setStreams([...DEBUG_STREAMS]);
										setLevels([...DEBUG_LEVELS]);
										setQuery("");
									}}
								>
									Clear filters
								</Button>
							</span>
						) : waiting ? (
							"Waiting for the first log line…"
						) : (
							"No log lines were recorded for this deployment."
						)
					}
				/>
				{!follow && running && visible.length > 0 ? (
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

			<footer
				className={cn("flex items-center gap-2 text-ink-3 text-xs")}
				aria-live="polite"
			>
				<StatusIcon kind={STATUS_KIND[stream.status]} label="" />
				<span>{STATUS_TEXT[stream.status]}</span>
				<span className="ml-auto tabular-nums">
					{visible.length === stream.lines.length
						? `${stream.lines.length} lines`
						: `${visible.length} of ${stream.lines.length} lines`}
				</span>
			</footer>
		</section>
	);
}
