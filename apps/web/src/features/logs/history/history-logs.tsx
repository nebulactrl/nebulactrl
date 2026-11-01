import { Link, useSearch } from "@tanstack/react-router";
import { cn } from "cn";
import { RefreshCwIcon } from "lucide-react";
import { type ReactNode, useEffect, useRef, useState } from "react";
import type { LogLine } from "#/api/sse";
import { Loading } from "#/components/common/loading";
import {
	Alert,
	AlertAction,
	AlertDescription,
	AlertTitle,
} from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { DownloadLogsButton } from "../download/download-logs-button";
import { LogView } from "../log-view";
import { LogSearchShell, NoMatches } from "../search/log-search-shell";
import type { FacetKey } from "../search/query";
import { useLogSearch } from "../search/use-log-search";
import {
	emptyKind,
	failureOf,
	footerText,
	HISTORY_MAX_LINES,
	joinNames,
	offlineSentence,
	resolveRange,
	summarizeUnavailable,
} from "./model";
import { RangePicker } from "./range-picker";
import { useHistoryRange } from "./url";
import { type HistoryScope, useHistoryLogs } from "./use-history-logs";

export interface HistoryServer {
	id: string;
	name: string;
}

export interface HistoryLogsProps {
	scope: HistoryScope;
	/** The servers the log lives on: named when one is offline, linked when capture is off. */
	servers: readonly HistoryServer[];
	/** The Live | History switch, first in the toolbar. */
	modeToggle: ReactNode;
	/** The search box's accessible name. */
	label: string;
	/** The log window's accessible name. */
	logLabel: string;
	facetKeys: readonly FacetKey[];
	skipBlank?: boolean;
	/** A service column, for a log that merges several services. */
	showService?: boolean;
	serviceIcon?: (line: LogLine) => ReactNode;
	className?: string;
	bodyClassName?: string;
}

function ServerLinks({ servers }: { servers: readonly HistoryServer[] }) {
	return servers.map((server, index) => (
		<span key={server.id}>
			{index > 0 ? ", " : null}
			<Link
				to="/servers/$serverId/settings/$section"
				params={{ serverId: server.id, section: "logs-and-disk" }}
				className="rounded-sm text-accent-ink underline underline-offset-2 outline-none focus-visible:focus-ring"
			>
				{server.name}
			</Link>
		</span>
	));
}

/** The query in the address. Read once: the first request already carries it. */
function useInitialQuery(): string {
	const search = useSearch({ strict: false }) as { q?: unknown };
	return typeof search.q === "string" ? search.q : "";
}

/**
 * The stored runtime log of a service or an environment in the same search
 * shell and `LogView` as the live tail: the newest page at the bottom, older
 * pages on demand at the top, a time range, and a sentence and an icon for
 * every state the history can be in (loading, empty, no match, server offline,
 * capture off, old agent, partial search, error). The query is sent to the
 * server once it settles; the local matcher still runs over the loaded lines,
 * for the highlights and the facet counts.
 */
export function HistoryLogs({
	scope,
	servers,
	modeToggle,
	label,
	logLabel,
	facetKeys,
	skipBlank = false,
	showService = false,
	serviceIcon,
	className,
	bodyClassName,
}: HistoryLogsProps) {
	const [range, setRange] = useHistoryRange();
	const initialQuery = useInitialQuery();
	const [sent, setSent] = useState(initialQuery);
	const history = useHistoryLogs({ scope, range, query: sent });
	const search = useLogSearch({
		lines: history.lines,
		skipBlank,
		cap: HISTORY_MAX_LINES,
	});
	const settled = search.settled.input;
	useEffect(() => setSent(settled), [settled]);

	// Where to scroll: to the newest line when a result arrives, back to the
	// line the reader was at when older lines are added in front of it.
	const [reveal, setReveal] = useState<{
		id: number;
		align: "start" | "end";
		nonce: number;
	}>();
	const seen = useRef<{ epoch: number; first: number } | null>(null);
	const { lines, epoch } = history;
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

	const failure = history.error ? failureOf(history.error) : null;
	const serverNames = servers.map((server) => server.name);
	const unavailable = summarizeUnavailable(history.unavailable);
	const next = history.continuation;

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
			</Alert>,
		);
	} else if (failure?.kind === "agent_outdated") {
		notices.push(
			<Alert key="outdated" variant="warning">
				<AlertTitle>Agent needs an update</AlertTitle>
				<AlertDescription>
					{serverNames.length > 0
						? `The agent on ${joinNames(serverNames)} is too old to read history. Update it from the server's page.`
						: failure.message}
				</AlertDescription>
			</Alert>,
		);
	} else if (failure) {
		notices.push(
			<Alert key="error" variant="destructive">
				<AlertTitle>History did not load</AlertTitle>
				<AlertDescription>{failure.message}</AlertDescription>
				<AlertAction>
					<Button variant="secondary" size="sm" onClick={history.refresh}>
						Try again
					</Button>
				</AlertAction>
			</Alert>,
		);
	}
	if (unavailable) {
		notices.push(
			<Alert key="unavailable" variant="warning">
				<AlertTitle>Some services are missing</AlertTitle>
				<AlertDescription>
					<p>{unavailable.title}</p>
					<ul className="m-0 flex list-none flex-col gap-0.5 p-0">
						{unavailable.items.map((item) => (
							<li key={item.serviceId}>
								<span className="text-ink-1">{item.serviceName}</span>
								<span className="text-ink-4"> on {item.serverName}: </span>
								{item.message}
							</li>
						))}
					</ul>
				</AlertDescription>
			</Alert>,
		);
	}
	if (history.capturing === false) {
		notices.push(
			<Alert key="capture" variant="warning">
				<AlertTitle>Log capture is off</AlertTitle>
				<AlertDescription>
					{scope.kind === "service"
						? "Log capture is switched off on this server. Turn it on in the server's settings."
						: "Log capture is switched off on at least one server of this environment. Turn it on in the server's settings."}
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
	if (history.queryErrors.length > 0) {
		notices.push(
			<Alert key="query" variant="info">
				<AlertTitle>Part of the search was ignored</AlertTitle>
				<AlertDescription>{history.queryErrors.join(" ")}</AlertDescription>
			</Alert>,
		);
	}

	const keepSearching = (
		<Button
			variant="secondary"
			size="sm"
			disabled={history.loadingOlder}
			onClick={history.loadOlder}
		>
			{history.loadingOlder ? <Spinner /> : null}
			Keep searching
		</Button>
	);

	let empty: ReactNode;
	if (history.loading) {
		empty = (
			<Loading
				label="Loading history"
				className="h-auto min-h-0 justify-start"
			/>
		);
	} else if (failure) {
		empty = "No lines to show.";
	} else if (next.kind === "partial") {
		empty = (
			<span className="flex flex-wrap items-center gap-x-3 gap-y-2">
				<span>Searched part of the log and found no lines yet.</span>
				{keepSearching}
			</span>
		);
	} else if (emptyKind(settled) === "no-match") {
		empty = (
			<NoMatches
				query={settled}
				onClear={search.clear}
				where=" in this range"
			/>
		);
	} else {
		empty = "No lines in this range.";
	}

	let strip: ReactNode = null;
	if (next.kind === "more") {
		strip = (
			<Button
				variant="secondary"
				size="sm"
				disabled={history.loadingOlder}
				onClick={history.loadOlder}
			>
				{history.loadingOlder ? <Spinner /> : null}
				Load older lines
			</Button>
		);
	} else if (next.kind === "partial" && history.lines.length > 0) {
		strip = (
			<>
				<span>Searched part of the log.</span>
				{keepSearching}
			</>
		);
	} else if (next.kind === "full") {
		strip = (
			<span>
				This view holds {HISTORY_MAX_LINES.toLocaleString()} lines. Narrow the
				range or the search to see older ones.
			</span>
		);
	} else if (next.kind === "end" && history.lines.length > 0) {
		strip = <span>This is the start of the log in this range.</span>;
	}

	const footer = history.loading
		? ""
		: `${footerText({
				lines: history.lines.length,
				oldest: history.oldest,
				scope: scope.kind,
				continuation: next.kind,
			})}${history.stale ? " Updating…" : ""}`;

	return (
		<LogSearchShell
			search={search}
			label={label}
			facetKeys={facetKeys}
			{...(className ? { className } : {})}
			{...(bodyClassName ? { bodyClassName } : {})}
			footer={footer}
			toolbar={
				<>
					<div className="flex h-8 items-center">{modeToggle}</div>
					<RangePicker value={range} onChange={setRange} />
					{scope.kind === "service" ? (
						<DownloadLogsButton
							target={{ kind: "service", id: scope.id }}
							component={scope.component}
							process={scope.process}
							query={sent}
							range={() => {
								const r = resolveRange(range, Date.now());
								return { from: r.from, to: r.to };
							}}
						/>
					) : null}
					<Button
						variant="secondary"
						size="sm"
						title="Look for lines newer than the last load"
						disabled={history.fetching}
						aria-busy={history.fetching}
						onClick={history.refresh}
					>
						<RefreshCwIcon aria-hidden />
						Refresh
					</Button>
				</>
			}
		>
			<div className="absolute inset-0 flex flex-col gap-2">
				{notices}
				<div
					className={cn(
						"surface-well flex min-h-0 flex-1 flex-col overflow-hidden rounded-card transition-opacity duration-quick ease-out motion-reduce:transition-none",
						history.stale && "opacity-60",
					)}
				>
					{strip ? (
						<div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-line-faint border-b px-3 py-1.5 text-ink-3 text-xs">
							{strip}
						</div>
					) : null}
					<LogView
						className="min-h-0 flex-1"
						lines={search.visible}
						terms={search.terms}
						showLevel
						showService={showService}
						{...(serviceIcon ? { serviceIcon } : {})}
						showDate
						follow={false}
						onLetGo={() => undefined}
						{...(reveal ? { reveal } : {})}
						label={logLabel}
						empty={empty}
					/>
				</div>
			</div>
		</LogSearchShell>
	);
}
