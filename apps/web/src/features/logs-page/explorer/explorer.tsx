import { useSearch } from "@tanstack/react-router";
import {
	ArrowDownToLineIcon,
	PauseIcon,
	PlayIcon,
	RefreshCwIcon,
	Trash2Icon,
} from "lucide-react";
import {
	type ReactNode,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import type { LogFacetsPage } from "#/api/gen/types.gen";
import type { Environment, Service } from "#/api/models";
import type { LogLine, SseStatus } from "#/api/sse";
import { Loading } from "#/components/common/loading";
import {
	Alert,
	AlertAction,
	AlertDescription,
	AlertTitle,
} from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Segmented } from "#/components/ui/segmented";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { ServiceIcon } from "#/features/canvas/service-icon";
import {
	type HistoryServer,
	ServerLinks,
} from "#/features/logs/history/history-logs";
import { LogsModeToggle } from "#/features/logs/history/logs-mode-toggle";
import {
	type Failure,
	failureOf,
	HISTORY_MAX_LINES,
	joinNames,
	offlineSentence,
	type RangeSelection,
	rangeLabel,
	rangeParams,
	STAMP_LOCALE,
} from "#/features/logs/history/model";
import { RangePicker } from "#/features/logs/history/range-picker";
import {
	useHistoryRange,
	useLogsMode,
	useSearchWriter,
} from "#/features/logs/history/url";
import { LogView } from "#/features/logs/log-view";
import type { FacetStats } from "#/features/logs/search/facets";
import { parsedFor } from "#/features/logs/search/line";
import {
	LogSearchShell,
	NoMatches,
} from "#/features/logs/search/log-search-shell";
import { FACET_KEYS } from "#/features/logs/search/query";
import {
	hasIncompleteTail,
	withoutClauseAt,
} from "#/features/logs/search/suggest";
import { useLogSearch } from "#/features/logs/search/use-log-search";
import { LineDetail } from "./line-detail";
import { logsAround, requestFields } from "./request-line";
import { histogramSeries, toSearchStats } from "./server-stats";
import { ALL_SOURCES, type LogSource, useSourcesParam } from "./sources";
import { useExplorerLogs } from "./use-explorer-logs";
import { useLogFacets } from "./use-log-facets";
import { VolumeHistogram } from "./volume-histogram";

/** The explorer fills the viewport under the page header and never shrinks below a readable box. */
export const EXPLORER_HEIGHT_CLASS = "h-[max(520px,calc(100dvh-15rem))]";

/** What Live counts and charts: a rolling quarter of an hour. */
const LIVE_RANGE_TEXT = "the last 15 minutes";

/** What the footer shows for each state of the live feed: the icon beside the words. */
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

type SourceChoice = "all" | LogSource;

/** How long the caret rests before suggestions are counted over its scope. */
const SCOPE_DEBOUNCE_MS = 150;

/** Where a keydown is not for the detail pane: popovers, menus and lists close on their own Escape. */
const OVERLAYS =
	'[role="dialog"], [role="listbox"], [role="menu"], [data-radix-popper-content-wrapper]';

/** What the live footer calls the rolling window. */
const LIVE_FOOTER_RANGE = "the last 15 min";

/** The facets when the Stream facet is redundant: the source control already picks one stream. */
const KEYS_WITHOUT_STREAM = FACET_KEYS.filter((key) => key !== "stream");

const number = (n: number) => n.toLocaleString(STAMP_LOCALE);

interface RowColumns {
	/** More than one service wrote the lines in view. */
	showService: boolean;
	/** The request lines in view are for more than one host. */
	showHost: boolean;
}

/**
 * Which columns carry information: a service column over one service, or a
 * host in front of every path of one host, only takes room from the path.
 */
function rowColumns(lines: readonly LogLine[]): RowColumns {
	const services = new Set<string>();
	let host: string | undefined;
	let hostsDiffer = false;
	for (const line of lines) {
		services.add(line.serviceId ?? line.serviceName ?? "");
		if (line.stream !== "requests") continue;
		const request = requestFields(parsedFor(line));
		if (!request) continue;
		if (host === undefined) host = request.host;
		else if (host !== request.host) hostsDiffer = true;
		if (services.size > 1 && hostsDiffer) break;
	}
	return { showService: services.size > 1, showHost: hostsDiffer };
}

type CountGap = NonNullable<LogFacetsPage["unavailable"]>[number];

/** Why counts are missing, in a few words: "agent on alpha needs an update". */
function gapReason(gaps: readonly CountGap[]): string {
	const first = gaps[0];
	if (!first) return "";
	const names = joinNames([...new Set(gaps.map((gap) => gap.serverName))]);
	if (first.reason === "agent_outdated") {
		return `the agent on ${names} needs an update`;
	}
	if (first.reason === "server_offline") return `${names} is offline`;
	return first.message;
}

/** Why a failed counts request failed, in a few words. */
function failureReason(failure: Failure): string {
	if (failure.kind === "agent_outdated") return "the agent needs an update";
	if (failure.kind === "server_offline") return "a server is offline";
	return failure.message;
}

interface ServerCounts {
	stats: Pick<FacetStats, "facets" | "attrs">;
	/** New for each answer, so suggestions and facets refresh once per answer. */
	version: number;
}

const counts = new WeakMap<LogFacetsPage, ServerCounts>();
let lastVersion = 0;
function countsOf(data: LogFacetsPage): ServerCounts {
	let known = counts.get(data);
	if (!known) {
		lastVersion += 1;
		known = { stats: toSearchStats(data), version: lastVersion };
		counts.set(data, known);
	}
	return known;
}

/** `value`, once it has stayed the same for `ms`. */
function useDebounced<T>(value: T, ms: number): T {
	const [settled, setSettled] = useState(value);
	useEffect(() => {
		const timer = setTimeout(() => setSettled(value), ms);
		return () => clearTimeout(timer);
	}, [value, ms]);
	return settled;
}

/** The service icon beside a line, for the services of the environment (a removed service's line has none). */
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
function serversOf(services: readonly Service[]): HistoryServer[] {
	const byId = new Map<string, string>();
	for (const service of services) {
		if (service.server) byId.set(service.server.id, service.server.name);
	}
	return [...byId].map(([id, name]) => ({ id, name }));
}

/**
 * Where to scroll a history: to the newest line when a result arrives, back to
 * the line the reader was at when older lines are added in front of it.
 */
function useReveal(lines: readonly LogLine[], epoch: number) {
	const [reveal, setReveal] = useState<{
		id: number;
		align: "start" | "end";
		nonce: number;
	}>();
	const seen = useRef<{ epoch: number; first: number } | null>(null);
	useEffect(() => {
		const first = lines[0]?.id;
		const last = lines[lines.length - 1]?.id;
		if (first === undefined || last === undefined) return;
		const before = seen.current;
		seen.current = { epoch, first };
		if (!before || before.epoch !== epoch) {
			setReveal((r) => ({
				id: last,
				align: "end",
				nonce: (r?.nonce ?? 0) + 1,
			}));
		} else if (first < before.first) {
			setReveal((r) => ({
				id: before.first,
				align: "start",
				nonce: (r?.nonce ?? 0) + 1,
			}));
		}
	}, [lines, epoch]);
	return reveal;
}

/** The query in the address. Read once: the first request already carries it. */
function useInitialQuery(): string {
	const search = useSearch({ strict: false }) as { q?: unknown };
	return typeof search.q === "string" ? search.q : "";
}

export interface LogsExplorerProps {
	environment: Environment;
	services: readonly Service[];
	className?: string;
}

/**
 * The environment's log explorer: one search box over runtime output and the
 * requests the servers proxied, live (a rolling tail) or stored (a range), a
 * volume histogram you can drag a span out of, server-counted facets and
 * suggestions, and a detail pane for one line. The query lives in the box (and
 * `?q=`): the facet panel, the pane's Filter and Exclude buttons and the
 * histogram only edit it or the range.
 */
export function LogsExplorer({
	environment,
	services,
	className,
}: LogsExplorerProps) {
	const environmentId = environment.id;
	const [mode, setMode] = useLogsMode();
	// An environment has no central view: a shared `?logs=central` reads as live.
	const view = mode === "history" ? "history" : "live";
	const [range, setRange] = useHistoryRange();
	const { sources, setSources } = useSourcesParam();
	const writeSearch = useSearchWriter();
	const initialQuery = useInitialQuery();
	const [sent, setSent] = useState(initialQuery);

	const logs = useExplorerLogs({
		environmentId,
		sources,
		mode: view,
		range,
		query: sent,
	});
	// Blank lines are spacing the program printed; as rows they would carry
	// only a timestamp and a service name.
	const search = useLogSearch({
		lines: logs.lines,
		skipBlank: true,
		cap: HISTORY_MAX_LINES,
		epoch: logs.epoch,
		reorder: logs.reorder,
	});
	const settled = search.settled.input;
	useEffect(() => setSent(settled), [settled]);

	const facets = useLogFacets({
		environmentId,
		sources,
		range,
		query: sent,
		live: view === "live",
		paused: logs.paused,
	});
	const facetData = facets.data;
	const refreshLogs = logs.refresh;
	const refreshFacets = facets.refresh;
	// Refresh and Try again: a new "now" for a range that reaches it, and the
	// list and the counts asked again.
	const retry = useCallback(() => {
		refreshLogs();
		refreshFacets();
	}, [refreshLogs, refreshFacets]);

	// The sidebar keeps the counts of the last whole query while the query ends
	// in a clause still being typed (`@me`, `level:`) or does not parse: such a
	// query matches nothing, and the facets must not blank out mid-typing. The
	// histogram and the footer count from the same answer.
	const lastWhole = useRef<LogFacetsPage>(undefined);
	const whole =
		search.settled.errors.length === 0 && !hasIncompleteTail(settled);
	if (whole && facetData && !facets.isFetching) lastWhole.current = facetData;
	const sidebarData = whole ? facetData : (lastWhole.current ?? facetData);

	// Suggestions count over the query without the clause the caret is in, so
	// `level:error @me|` suggests what the error lines have, not what a
	// half-typed `@me` matches (nothing). While the box is not in use, or the
	// query is unchanged by the cut, the sidebar's counts serve.
	const [caret, setCaret] = useState<number | null>(null);
	const scope = useDebounced(
		caret === null ? null : withoutClauseAt(search.text, caret),
		SCOPE_DEBOUNCE_MS,
	);
	const scoped = scope !== null && scope.trim() !== sent.trim();
	const scopedFacets = useLogFacets({
		environmentId,
		sources,
		range,
		query: scope ?? "",
		live: view === "live",
		paused: logs.paused,
		enabled: scoped,
	});
	const suggestData = scoped
		? (scopedFacets.data ?? lastWhole.current ?? facetData)
		: sidebarData;

	const runtimeSearched = sources.includes("runtime");
	const requestsSearched = sources.includes("requests");
	const captureOff =
		runtimeSearched &&
		(logs.capturing === false || facetData?.capturing === false);
	// With capture off the servers keep no runtime lines to count: Live then
	// counts what the browser holds, as a tail does, and the box suggests from it.
	const countInBrowser = view === "live" && captureOff;
	// Until the first answer the facets and suggestions count the loaded lines.
	const facetSource =
		!countInBrowser && sidebarData ? countsOf(sidebarData) : undefined;
	const suggestSource =
		!countInBrowser && suggestData ? countsOf(suggestData) : undefined;

	const series = useMemo(() => histogramSeries(sidebarData), [sidebarData]);
	const facetFailure = facets.error ? failureOf(facets.error) : null;
	const gaps = sidebarData?.unavailable ?? [];
	// Servers that could not count (offline, an agent too old) leave empty
	// buckets: that is not "no lines".
	const countsGone =
		gaps.length > 0 && series.every((point) => point.count === 0);
	let histogramStatus: "ready" | "loading" | "unavailable" = "ready";
	let histogramReason = "";
	if (facetFailure) {
		histogramStatus = "unavailable";
		histogramReason = failureReason(facetFailure);
	} else if (!sidebarData) {
		histogramStatus = "loading";
	} else if (countsGone) {
		histogramStatus = "unavailable";
		histogramReason = gapReason(gaps);
	}

	// Following is automatic while the list is at the bottom. Scrolling up,
	// pressing a row-navigation key or focusing a row lets go; scrolling back
	// down, or Jump to latest, resumes.
	const [following, setFollowing] = useState(true);
	useEffect(() => {
		if (view === "live") setFollowing(true);
	}, [view]);
	const setAtBottom = logs.setAtBottom;
	useEffect(() => setAtBottom(following), [following, setAtBottom]);
	const reveal = useReveal(logs.lines, logs.epoch);
	const serviceIcon = useServiceIcon(services);
	const servers = serversOf(services);
	const columns = useMemo(() => rowColumns(search.visible), [search.visible]);

	const [selected, setSelected] = useState<LogLine | null>(null);
	const bodyRef = useRef<HTMLDivElement>(null);
	const closeDetail = useCallback(() => {
		// Focus inside the pane (or nowhere) goes back to the log, not to the page.
		const active = document.activeElement;
		if (
			!active ||
			active === document.body ||
			active.closest('[aria-label="Log line detail"]')
		) {
			bodyRef.current?.querySelector<HTMLElement>('[role="log"]')?.focus();
		}
		setSelected(null);
	}, []);
	useEffect(() => {
		if (!selected) return;
		const onKey = (event: KeyboardEvent) => {
			const target = event.target;
			const typing =
				target instanceof HTMLInputElement ||
				target instanceof HTMLTextAreaElement;
			// Escape that closes a popover, a menu or a list is theirs, not the pane's.
			const overlaid =
				target instanceof Element && target.closest(OVERLAYS) !== null;
			if (
				event.key !== "Escape" ||
				event.defaultPrevented ||
				typing ||
				overlaid
			) {
				return;
			}
			closeDetail();
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [selected, closeDetail]);

	const chooseSpan = (span: { from: string; to: string }) => {
		const custom: RangeSelection = { kind: "custom", ...span };
		// One write, so the mode and the range land in the same address.
		writeSearch({ logs: "history", ...rangeParams(custom) });
	};

	// History, 30 s either side of the request, for its service (and replica),
	// over every source; the pane stays closed.
	const showAround = (line: LogLine) => {
		const around = logsAround(line);
		if (!around) return;
		const custom: RangeSelection = {
			kind: "custom",
			from: around.from,
			to: around.to,
		};
		writeSearch({
			logs: "history",
			...rangeParams(custom),
			sources: undefined,
			q: around.query,
		});
		closeDetail();
	};

	const rangeText =
		view === "live"
			? LIVE_RANGE_TEXT
			: range.kind === "preset"
				? `the ${rangeLabel(range).toLowerCase()}`
				: rangeLabel(range);

	const sourceChoice: SourceChoice =
		sources.length === ALL_SOURCES.length ? "all" : (sources[0] ?? "all");
	const toolbar = (
		<>
			<div className="flex h-8 items-center">
				<Segmented<SourceChoice>
					aria-label="Sources to search"
					font="sans"
					density="regular"
					value={sourceChoice}
					onValueChange={(next) =>
						setSources(next === "all" ? ALL_SOURCES : [next])
					}
					options={[
						{ value: "all", label: "All" },
						{ value: "runtime", label: "Runtime" },
						{ value: "requests", label: "Requests" },
					]}
				/>
			</div>
			<div className="flex h-8 items-center">
				<LogsModeToggle mode={view} onChange={setMode} />
			</div>
			{view === "history" ? (
				<>
					<RangePicker
						value={range}
						onChange={(next) => {
							// Choosing the range already shown asks for a fresh "now".
							if (JSON.stringify(next) === JSON.stringify(range)) retry();
							setRange(next);
						}}
					/>
					<Button variant="secondary" onClick={retry}>
						<RefreshCwIcon />
						Refresh
					</Button>
				</>
			) : (
				<>
					<span className="flex h-8 items-center text-ink-3 text-xs">
						Last 15 min
					</span>
					<Button
						variant="secondary"
						onClick={() => logs.setPaused(!logs.paused)}
						aria-pressed={logs.paused}
					>
						{logs.paused ? <PlayIcon /> : <PauseIcon />}
						{logs.paused ? "Resume" : "Pause"}
					</Button>
					<Button variant="ghost" onClick={logs.clear}>
						<Trash2Icon />
						Clear
					</Button>
				</>
			)}
		</>
	);

	const failure = logs.error ? failureOf(logs.error) : null;
	const serverNames = servers.map((server) => server.name);
	const silentById = new Map<string, string>();
	for (const server of logs.unavailable) {
		silentById.set(server.serverId, server.serverName ?? server.serverId);
	}
	for (const gap of gaps) {
		silentById.set(gap.serverId, gap.serverName || gap.serverId);
	}
	const silent = [...silentById].map(([id, name]) => ({ id, name }));
	const tryAgain = (
		<AlertAction>
			<Button variant="secondary" size="sm" onClick={retry}>
				Try again
			</Button>
		</AlertAction>
	);
	const notices: ReactNode[] = [];
	if (failure?.kind === "server_offline") {
		notices.push(
			<Alert key="offline" variant="warning">
				<AlertTitle>Server offline</AlertTitle>
				<AlertDescription>
					{serverNames.length > 0
						? offlineSentence(serverNames)
						: failure.message}
				</AlertDescription>
				{tryAgain}
			</Alert>,
		);
	} else if (failure?.kind === "agent_outdated") {
		notices.push(
			<Alert key="outdated" variant="warning">
				<AlertTitle>Agent needs an update</AlertTitle>
				<AlertDescription>
					{serverNames.length > 0
						? `The agent on ${joinNames(serverNames)} is too old to read logs. Update it from the server's page.`
						: failure.message}
				</AlertDescription>
				{tryAgain}
			</Alert>,
		);
	} else if (failure) {
		notices.push(
			<Alert key="error" variant="destructive">
				<AlertTitle>Logs did not load</AlertTitle>
				<AlertDescription>{failure.message}</AlertDescription>
				{tryAgain}
			</Alert>,
		);
	} else if (facetFailure) {
		// The list loaded; only the counts did not.
		const outdated = facetFailure.kind === "agent_outdated";
		const offline = facetFailure.kind === "server_offline";
		notices.push(
			<Alert
				key="counts"
				variant={outdated || offline ? "warning" : "destructive"}
			>
				<AlertTitle>
					{outdated || offline ? "Counts unavailable" : "Counts did not load"}
				</AlertTitle>
				<AlertDescription>
					{outdated
						? `The agent on ${serverNames.length > 0 ? joinNames(serverNames) : "a server"} is too old to count logs. Update it from the server's page.`
						: offline
							? "A server is offline, so its lines cannot be counted. The counts return when it reconnects."
							: facetFailure.message}
				</AlertDescription>
				{tryAgain}
			</Alert>,
		);
	}
	if (silent.length > 0) {
		notices.push(
			<Alert key="unavailable" variant="warning">
				<AlertTitle>Some servers did not answer</AlertTitle>
				<AlertDescription>
					Their lines are missing from this view:{" "}
					<ServerLinks servers={silent} />
				</AlertDescription>
			</Alert>,
		);
	}
	// Request logs read as off only for a server that answered: with a server
	// that has not answered, or nothing deployed yet, the other notices apply.
	const deployed = services.some(
		(service) =>
			service.currentDeploymentId != null && service.desiredState !== "stopped",
	);
	if (
		requestsSearched &&
		facetData?.requestsCapturing === false &&
		silent.length === 0 &&
		deployed
	) {
		notices.push(
			<Alert key="requests-off" variant="info">
				<AlertTitle>
					Request logs are off on this environment's servers
				</AlertTitle>
				<AlertDescription>
					Turn them on in the logs section of the server settings
					{servers.length > 0 ? (
						<>
							: <ServerLinks servers={servers} />
						</>
					) : null}
					.
				</AlertDescription>
			</Alert>,
		);
	}
	if (captureOff) {
		notices.push(
			<Alert key="capture" variant="warning">
				<AlertTitle>Log capture is off</AlertTitle>
				<AlertDescription>
					Log capture is switched off on at least one server of this
					environment. Turn it on in the server's settings.
					{servers.length > 0 ? (
						<>
							{" "}
							<ServerLinks servers={servers} />
						</>
					) : null}
				</AlertDescription>
			</Alert>,
		);
	}
	// While the last clause is still being typed the box's own hint says what is
	// missing; the banner is for a settled query with parts the server ignored.
	if (facetData?.queryErrors?.length && !hasIncompleteTail(settled)) {
		notices.push(
			<Alert key="query" variant="info">
				<AlertTitle>Part of the search was ignored</AlertTitle>
				<AlertDescription>{facetData.queryErrors.join(" ")}</AlertDescription>
			</Alert>,
		);
	}

	let empty: ReactNode;
	if (logs.loading) {
		empty = (
			<Loading label="Loading logs" className="h-auto min-h-0 justify-start" />
		);
	} else if (failure) {
		empty = "No lines to show.";
	} else if (
		settled.trim() !== "" &&
		(view === "history" || search.total > 0)
	) {
		empty = (
			<NoMatches
				query={settled}
				onClear={search.clear}
				where={view === "history" ? " in this range" : ""}
			/>
		);
	} else if (view === "history") {
		empty = "No lines in this range.";
	} else {
		empty =
			"Waiting for output. Lines appear here as your services write them.";
	}

	const olderStrip =
		view === "history" && (logs.partial || logs.hasOlder) ? (
			<div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-line-faint border-b px-3 py-1.5 text-ink-3 text-xs">
				{logs.partial ? (
					<span>Searched part of the range; scroll up for more</span>
				) : null}
				{logs.hasOlder ? (
					<Button variant="secondary" size="sm" onClick={logs.fetchOlder}>
						Load older lines
					</Button>
				) : null}
			</div>
		) : null;

	// The footer counts from the sidebar's answer, so the two agree. Live says
	// how many lines the window holds and how many of them the list shows.
	const shown = search.visible.length;
	const matched = countInBrowser ? undefined : sidebarData?.matched;
	const scanned = sidebarData?.partial
		? `, counted over the newest ${number(sidebarData.scanned)} scanned`
		: "";
	let tally: string;
	if (matched === undefined) {
		tally = `${number(search.total)} ${search.total === 1 ? "line" : "lines"} loaded · ${number(shown)} shown`;
	} else if (view === "live") {
		// The counts trail the stream by up to their refresh: the list can be ahead.
		const inWindow = Math.max(matched, shown);
		tally = `${number(inWindow)} in ${LIVE_FOOTER_RANGE}${scanned}${shown < inWindow ? ` · showing the newest ${number(shown)}` : ""}`;
	} else {
		tally = `${number(matched)} ${matched === 1 ? "line matches" : "lines match"} in ${rangeText}${scanned} · ${number(shown)} shown`;
	}
	const footer = (
		<span className="tabular-nums">
			{view === "live" ? (
				<>
					<span
						data-testid="stream-status"
						className="inline-flex items-center gap-1.5 font-mono"
					>
						<StatusIcon
							kind={logs.paused ? "stopped" : STREAM_KIND[logs.status]}
							label=""
						/>
						{logs.paused ? "Paused" : STATUS_TEXT[logs.status]}
					</span>
					{" · "}
				</>
			) : null}
			{tally}
			{runtimeSearched ? (
				<span className="text-ink-4">
					{" "}
					· Runtime output has secret values masked.
				</span>
			) : null}
		</span>
	);

	const jumpLabel =
		logs.newCount > 0
			? `${number(logs.newCount)} new ${logs.newCount === 1 ? "line" : "lines"} · Jump to latest`
			: "Jump to latest";
	const around = selected ? logsAround(selected) : null;

	return (
		<LogSearchShell
			search={search}
			label="Search environment logs"
			placeholder="Search logs: level:error @path:/api/* status:5* …"
			facetKeys={sourceChoice === "all" ? FACET_KEYS : KEYS_WITHOUT_STREAM}
			statusClasses={requestsSearched}
			requestFacets={requestsSearched}
			{...(facetSource ? { facetSource } : {})}
			{...(suggestSource ? { suggestSource } : {})}
			onCaretChange={setCaret}
			className={className ?? EXPLORER_HEIGHT_CLASS}
			bodyClassName="flex-1"
			toolbar={toolbar}
			footer={footer}
			beforeBody={
				<VolumeHistogram
					series={series}
					rangeText={rangeText}
					onSelectSpan={chooseSpan}
					status={histogramStatus}
					{...(histogramReason ? { reason: histogramReason } : {})}
				/>
			}
		>
			<div ref={bodyRef} className="absolute inset-0 flex gap-3">
				<div className="flex min-h-0 min-w-0 flex-1 flex-col gap-2">
					{notices}
					<div className="surface-well relative flex min-h-0 flex-1 flex-col overflow-hidden rounded-card">
						{olderStrip}
						<LogView
							className="min-h-0 flex-1"
							lines={search.visible}
							terms={search.terms}
							showLevel
							showService={columns.showService}
							showHost={columns.showHost}
							serviceIcon={serviceIcon}
							showDate={view === "history"}
							follow={view === "live" && following}
							onLetGo={() => setFollowing(false)}
							onAtBottomChange={(atBottom) => {
								if (atBottom && view === "live") setFollowing(true);
							}}
							{...(view === "history" && reveal ? { reveal } : {})}
							selected={selected}
							onSelect={setSelected}
							label="Environment logs"
							empty={empty}
						/>
						{view === "live" && !following ? (
							<div className="pointer-events-none absolute inset-x-0 bottom-3 flex justify-center">
								<button
									type="button"
									onClick={() => setFollowing(true)}
									className="surface-pop pointer-events-auto inline-flex h-7 items-center gap-1.5 rounded-full px-3 text-ink-1 text-xs tabular-nums outline-none hover:bg-white/10 focus-visible:focus-ring motion-safe:transition-[opacity,translate] motion-safe:duration-150 motion-safe:ease-out starting:translate-y-2 starting:opacity-0"
								>
									<ArrowDownToLineIcon aria-hidden className="size-3.5" />
									{jumpLabel}
								</button>
							</div>
						) : null}
					</div>
				</div>
				{selected ? (
					<LineDetail
						line={selected}
						query={search.text}
						onQueryChange={search.setText}
						onClose={closeDetail}
						{...(around ? { onShowAround: () => showAround(selected) } : {})}
						className="max-w-[70%]"
					/>
				) : null}
			</div>
		</LogSearchShell>
	);
}
