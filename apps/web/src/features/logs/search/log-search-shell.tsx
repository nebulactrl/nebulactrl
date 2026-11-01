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
import type { FacetKey } from "./query";
import { searchScopeNote } from "./scope-note";
import { SearchInput } from "./search-input";
import type { LogSearch } from "./use-log-search";

/** The facet panel docks beside the log from this width; narrower, it is a popover. */
const DOCK_FACETS_AT = 900;

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
	/** Buttons beside the box: Follow, Pause, Clear. */
	toolbar?: ReactNode;
	/** The log window. It fills the space beside the facets. */
	children: ReactNode;
	/** Sizing of the area under the toolbar (the Logs tab fills its tab, Observability has a height). */
	bodyClassName?: string;
	className?: string;
	/**
	 * Replaces the result count and its "Searching the last N lines" sentence
	 * (the live tail's footer), for a view that says something else, such as
	 * how far back the stored history goes.
	 */
	footer?: ReactNode;
}

/**
 * The search box, the facets and the result count around a log window, shared
 * by the Logs tab and the Observability page. The query's state lives in
 * `useLogSearch`; this lays it out.
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
}: LogSearchShellProps<L>) {
	const ref = useRef<HTMLDivElement>(null);
	const docked = useElementWidth(ref) >= DOCK_FACETS_AT;
	const firstError = search.settled.errors[0];
	const searching = search.settled.input.trim() !== "";
	const facetProps = {
		stats: search.stats,
		statsVersion: search.statsVersion,
		keys: facetKeys,
		query: search.text,
		onQueryChange: search.setText,
	};

	return (
		<div ref={ref} className={cn("flex min-h-0 flex-col gap-3", className)}>
			<div className="flex flex-wrap items-start gap-x-3 gap-y-2">
				<SearchInput
					className="min-w-[220px] flex-1"
					label={label}
					placeholder='Search logs: error  "exact phrase"  level:error  -service:web  @status:[400 TO 499]'
					value={search.text}
					onChange={search.setText}
					parsed={search.parsed}
					error={firstError?.message}
					stats={search.stats}
					statsVersion={search.statsVersion}
					recent={search.recent}
					onSubmit={search.commit}
				/>
				{docked ? null : <FacetsPopover {...facetProps} />}
				{toolbar}
			</div>
			<p role="status" aria-live="polite" className="sr-only">
				{search.announcement}
			</p>
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
