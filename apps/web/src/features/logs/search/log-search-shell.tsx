import { cn } from "cn";
import {
	type ReactNode,
	type RefObject,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { Button } from "#/components/ui/button";
import { count } from "#/lib/format";
import type { BufferLine } from "./buffer";
import { FacetPanel, FacetsPopover } from "./facet-panel";
import type { FacetStats } from "./facets";
import type { FacetKey } from "./query";
import { searchScopeNote } from "./scope-note";
import { SearchInput } from "./search-input";
import type { LogSearch } from "./use-log-search";

/** The facet panel docks beside the log from this width; narrower, it is a popover. */
const DOCK_FACETS_AT = 900;

const DEFAULT_PLACEHOLDER =
	'Search logs: error  "exact phrase"  level:error  -service:web  @status:[400 TO 499]';

function useElementWidth(ref: RefObject<HTMLElement | null>): number {
	const [width, setWidth] = useState(0);
	useLayoutEffect(() => {
		const element = ref.current;
		if (!element) return;
		setWidth(element.clientWidth);
		const observer = new ResizeObserver(([entry]) => {
			if (entry) setWidth(entry.contentRect.width);
		});
		observer.observe(element);
		return () => observer.disconnect();
	}, [ref]);
	return width;
}

/** What a log view says when the buffer has lines and the query matches none. */
export function NoMatches({
	query,
	onClear,
	where = "",
}: {
	query: string;
	onClear(): void;
	/** Where it looked, after the query: " in this range". */
	where?: string;
}) {
	return (
		<span className="flex flex-wrap items-center gap-x-3 gap-y-2">
			<span>
				No lines match{" "}
				<code className="type-mono text-ink-1">{query.trim()}</code>
				{where}.
			</span>
			<Button variant="secondary" size="sm" onClick={onClear}>
				Clear search
			</Button>
		</span>
	);
}

export interface LogSearchShellProps<L extends BufferLine> {
	search: LogSearch<L>;
	/** The facets this view offers. */
	facetKeys: readonly FacetKey[];
	label: string;
	/** Buttons beside the box: Follow, Pause, Clear, a source choice. */
	toolbar?: ReactNode;
	/** The log window. It fills the space beside the facets. */
	children: ReactNode;
	/** Sizing of the area under the toolbar (the Logs tab fills its tab; the Logs page gives it the rest of its page). */
	bodyClassName?: string;
	className?: string;
	/**
	 * Replaces the result count and its "Searching the last N lines" sentence
	 * (the live tail's footer), for a view that says something else, such as
	 * how far back the stored history goes.
	 */
	footer?: ReactNode;
	/** The search box's hint text. */
	placeholder?: string;
	/**
	 * Counts for the facets and the suggestions that replace the buffer's, for
	 * a view whose server counted the whole range. `version` changes whenever
	 * they do.
	 */
	facetSource?: {
		stats: Pick<FacetStats, "facets" | "attrs">;
		version: number;
	};
	/**
	 * Counts for the search box's suggestions alone, when they are over another
	 * query than the facets' (the query without the clause being typed).
	 * Defaults to `facetSource`.
	 */
	suggestSource?: {
		stats: Pick<FacetStats, "facets" | "attrs">;
		version: number;
	};
	/** Where the caret is while the box is focused and suggesting; null otherwise. */
	onCaretChange?: (caret: number | null) => void;
	/** Full width between the search row and the body: a chart over the log. */
	beforeBody?: ReactNode;
	/** Group the status facet by class (2xx, 3xx…): the lines are HTTP requests. */
	statusClasses?: boolean;
	/** Offer the "Requests" facet group: kind, method, path and duration ranges. */
	requestFacets?: boolean;
}

/**
 * The search box, the facets and the result count around a log window, shared
 * by a service's Logs tab and the environment's Logs page. The query's state
 * lives in `useLogSearch`; this lays it out.
 */
export function LogSearchShell<L extends BufferLine>({
	search,
	facetKeys,
	label,
	toolbar,
	children,
	bodyClassName,
	className,
	footer,
	placeholder = DEFAULT_PLACEHOLDER,
	facetSource,
	suggestSource,
	onCaretChange,
	beforeBody,
	statusClasses = false,
	requestFacets = false,
}: LogSearchShellProps<L>) {
	const ref = useRef<HTMLDivElement>(null);
	const docked = useElementWidth(ref) >= DOCK_FACETS_AT;
	const firstError = search.settled.errors[0];
	const searching = search.settled.input.trim() !== "";
	// The server's counts, when the view has them, stand in for the buffer's.
	const stats = facetSource?.stats ?? search.stats;
	const statsVersion = facetSource?.version ?? search.statsVersion;
	// The suggestions count over their own scope when the view has one.
	const suggestStats = suggestSource?.stats ?? stats;
	const suggestVersion = suggestSource?.version ?? statsVersion;
	const facetProps = {
		stats,
		statsVersion,
		keys: facetKeys,
		statusClasses,
		requests: requestFacets,
		query: search.text,
		onQueryChange: search.setText,
	};

	return (
		<div ref={ref} className={cn("flex min-h-0 flex-col gap-3", className)}>
			<div className="flex flex-wrap items-start gap-x-3 gap-y-2">
				<SearchInput
					className="min-w-[220px] flex-1"
					label={label}
					placeholder={placeholder}
					value={search.text}
					onChange={search.setText}
					parsed={search.parsed}
					error={firstError?.message}
					stats={suggestStats}
					statsVersion={suggestVersion}
					{...(onCaretChange ? { onCaretChange } : {})}
					recent={search.recent}
					onSubmit={search.commit}
				/>
				{docked ? null : <FacetsPopover {...facetProps} />}
				{toolbar}
			</div>
			<p role="status" aria-live="polite" className="sr-only">
				{search.announcement}
			</p>
			{beforeBody}
			<div className={cn("flex min-h-0 gap-3", bodyClassName)}>
				{docked ? (
					<FacetPanel
						{...facetProps}
						className="surface-well w-[220px] shrink-0 overflow-auto overscroll-contain rounded-card p-2"
					/>
				) : null}
				<div className="relative min-h-0 min-w-0 flex-1">{children}</div>
			</div>
			{footer !== undefined ? (
				<p className="text-ink-3 text-xs">{footer}</p>
			) : (
				<p className="font-mono text-ink-3 text-xs tabular-nums">
					{searching
						? `${search.visible.length} of ${count(search.total, "line")}`
						: count(search.total, "line")}
					<span className="font-sans text-ink-4">
						{" "}
						· {searchScopeNote(search.total)}
					</span>
				</p>
			)}
		</div>
	);
}
