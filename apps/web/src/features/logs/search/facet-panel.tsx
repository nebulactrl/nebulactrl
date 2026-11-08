import { cn } from "cn";
import {
	CheckIcon,
	ChevronDownIcon,
	ChevronRightIcon,
	ListFilterIcon,
	MinusIcon,
	PlusIcon,
} from "lucide-react";
import { type ReactNode, useId, useMemo, useState } from "react";
import { Button } from "#/components/ui/button";
import { statusKindOfTone } from "#/components/ui/lamp";
import {
	Popover,
	PopoverContent,
	PopoverTrigger,
} from "#/components/ui/popover";
import { StatusIcon } from "#/components/ui/status-icon";
import { statusTone } from "#/features/logs-page/explorer/request-line";
import {
	escapeControls,
	SafeText,
} from "#/features/logs-page/explorer/safe-text";
import {
	type FacetMode,
	type FacetStats,
	type FacetValue,
	facetTermState,
	rawTermActive,
	toggleFacetTerm,
	toggleRawTerm,
	topValues,
} from "./facets";
import type { FacetKey } from "./query";

const TITLES: Record<FacetKey, string> = {
	level: "Level",
	service: "Service",
	stream: "Stream",
	replica: "Replica",
	status: "Status",
	deployment: "Deployment",
};

/** Levels read worst first, whatever the counts. */
const LEVEL_ORDER = ["error", "warn", "info", "debug"];

const INITIAL_ROWS = 8;
/** Request attributes list fewer values: the sidebar is narrow and paths are long. */
const REQUEST_ROWS = 5;

/** The quick duration ranges, as the query writes them. */
const DURATION_RANGES = [
	{ label: "< 100 ms", term: "@duration_ms:<100" },
	{ label: "100 ms – 1 s", term: "@duration_ms:[100 TO 1000]" },
	{ label: "> 1 s", term: "@duration_ms:>1000" },
] as const;
const DURATION_TERMS = DURATION_RANGES.map((range) => range.term);

/** The attributes of a request line the "Requests" group lists the values of. */
const REQUEST_ATTRS = [
	{ path: "kind", title: "Kind" },
	{ path: "method", title: "Method" },
	{ path: "path", title: "Path" },
] as const;

type Counts = Pick<FacetStats, "facets"> & Partial<Pick<FacetStats, "attrs">>;

export interface FacetPanelProps {
	stats: Counts;
	/** Changes whenever the counts do. */
	statsVersion: number;
	/** Which facets to offer, in order. */
	keys: readonly FacetKey[];
	query: string;
	onQueryChange(next: string): void;
	/**
	 * Group the status facet by class (2xx, 3xx, 4xx, 5xx), the exact codes one
	 * disclosure away. For a view whose lines are HTTP requests.
	 */
	statusClasses?: boolean;
	/** A "Requests" group: kind, method, path and duration ranges of request lines. */
	requests?: boolean;
	className?: string;
}

function valuesFor(stats: Counts, key: FacetKey): FacetValue[] {
	const values = topValues(stats.facets[key]);
	if (key !== "level") return values;
	return values.sort(
		(a, b) => LEVEL_ORDER.indexOf(a.value) - LEVEL_ORDER.indexOf(b.value),
	);
}

interface StatusClass {
	/** "2xx", or "Other" for a value that is no HTTP status. */
	label: string;
	/** The query's wildcard for the class: "2*". Absent for "Other". */
	pattern?: string;
	count: number;
	codes: FacetValue[];
}

/** Status values summed by class, 1xx to 5xx in order, anything else last. */
function statusClassesOf(values: readonly FacetValue[]): StatusClass[] {
	const byClass = new Map<string, StatusClass>();
	for (const entry of values) {
		const code = /^[1-5]\d\d$/.test(entry.value) ? entry.value : null;
		const label = code ? `${code[0]}xx` : "Other";
		let group = byClass.get(label);
		if (!group) {
			group = { label, count: 0, codes: [] };
			if (code) group.pattern = `${code[0]}*`;
			byClass.set(label, group);
		}
		group.count += entry.count;
		group.codes.push(entry);
	}
	for (const group of byClass.values()) {
		group.codes.sort((a, b) =>
			a.value.localeCompare(b.value, undefined, { numeric: true }),
		);
	}
	return [...byClass.values()].sort((a, b) => {
		if (!a.pattern || !b.pattern) return a.pattern ? -1 : b.pattern ? 1 : 0;
		return a.label.localeCompare(b.label);
	});
}

function ModeButton({
	mode,
	pressed,
	term,
	onClick,
}: {
	mode: FacetMode;
	pressed: boolean;
	/** What it adds, as the query writes it: `level:error`. */
	term: string;
	onClick(): void;
}) {
	const Icon = mode === "include" ? PlusIcon : MinusIcon;
	const label = `${mode === "include" ? "Include" : "Exclude"} ${escapeControls(term)}`;
	return (
		<button
			type="button"
			aria-pressed={pressed}
			aria-label={label}
			title={label}
			onClick={onClick}
			className={cn(
				"grid size-5 shrink-0 place-items-center rounded-tip text-ink-3 outline-none hover:bg-white/10 hover:text-ink-1 focus-visible:focus-ring",
				pressed && "bg-accent-wash text-accent-ink ring-1 ring-accent-ink/60",
			)}
		>
			<Icon aria-hidden className="size-3" />
		</button>
	);
}

/** One value of a facet: its text, its count, and Include and Exclude. */
function ValueRow({
	facet,
	value,
	count,
	query,
	onQueryChange,
	wildcard = false,
	children,
	className,
}: {
	facet: string;
	value: string;
	count: number;
	query: string;
	onQueryChange(next: string): void;
	/** `value` carries a `*` that is to stay a wildcard in the query. */
	wildcard?: boolean;
	/** What stands for the value; it is shown as text when left out. */
	children?: ReactNode;
	className?: string;
}) {
	const state = facetTermState(query, facet, value);
	const term = `${facet}:${value}`;
	return (
		<li
			className={cn(
				"flex items-center gap-1 rounded-ctl py-px pr-0.5 pl-6 hover:bg-white/5",
				className,
			)}
		>
			<span
				className={cn(
					"flex min-w-0 flex-1 items-center gap-1.5 font-mono text-xs",
					state === "exclude" ? "text-ink-4 line-through" : "text-ink-2",
				)}
			>
				{children ?? (
					<span className="truncate" title={escapeControls(value)}>
						<SafeText text={value} />
					</span>
				)}
			</span>
			<span className="font-mono text-ink-4 text-xs tabular-nums">{count}</span>
			<ModeButton
				mode="include"
				pressed={state === "include"}
				term={term}
				onClick={() =>
					onQueryChange(
						toggleFacetTerm(query, facet, value, "include", { wildcard }),
					)
				}
			/>
			<ModeButton
				mode="exclude"
				pressed={state === "exclude"}
				term={term}
				onClick={() =>
					onQueryChange(
						toggleFacetTerm(query, facet, value, "exclude", { wildcard }),
					)
				}
			/>
		</li>
	);
}

/** Values with "Show N more" after `initial` of them. */
function ValueList({
	facet,
	values,
	initial,
	query,
	onQueryChange,
	id,
}: {
	facet: string;
	values: readonly FacetValue[];
	initial: number;
	query: string;
	onQueryChange(next: string): void;
	id?: string;
}) {
	const [all, setAll] = useState(false);
	const shown = all ? values : values.slice(0, initial);
	return (
		<ul id={id} className="flex flex-col">
			{shown.map(({ value, count }) => (
				<ValueRow
					key={value}
					facet={facet}
					value={value}
					count={count}
					query={query}
					onQueryChange={onQueryChange}
				/>
			))}
			{values.length > initial ? (
				<li className="pl-6">
					<button
						type="button"
						onClick={() => setAll((current) => !current)}
						className="rounded-ctl px-1 py-0.5 text-ink-3 text-xs outline-none hover:text-ink-1 focus-visible:focus-ring"
					>
						{all ? "Show fewer" : `Show ${values.length - initial} more`}
					</button>
				</li>
			) : null}
		</ul>
	);
}

/** The collapsible header of a section: its title and how many values it lists. */
function SectionHeader({
	title,
	count,
	open,
	bodyId,
	onToggle,
}: {
	title: string;
	count: number;
	open: boolean;
	bodyId: string;
	onToggle(): void;
}) {
	const Chevron = open ? ChevronDownIcon : ChevronRightIcon;
	return (
		<button
			type="button"
			aria-expanded={open}
			aria-controls={bodyId}
			onClick={onToggle}
			className="flex items-center gap-1.5 rounded-ctl px-1 py-1 text-left font-medium text-ink-2 text-xs outline-none hover:text-ink-1 focus-visible:focus-ring"
		>
			<Chevron aria-hidden className="size-3.5 shrink-0 text-ink-3" />
			<span className="flex-1">{title}</span>
			<span className="font-mono text-ink-4 tabular-nums">{count}</span>
		</button>
	);
}

function FacetSection({
	facet,
	values,
	query,
	onQueryChange,
}: {
	facet: FacetKey;
	values: FacetValue[];
	query: string;
	onQueryChange(next: string): void;
}) {
	const bodyId = useId();
	const [open, setOpen] = useState(true);
	return (
		<section className="flex flex-col gap-1" aria-label={TITLES[facet]}>
			<SectionHeader
				title={TITLES[facet]}
				count={values.length}
				open={open}
				bodyId={bodyId}
				onToggle={() => setOpen((current) => !current)}
			/>
			{open ? (
				<ValueList
					id={bodyId}
					facet={facet}
					values={values}
					initial={INITIAL_ROWS}
					query={query}
					onQueryChange={onQueryChange}
				/>
			) : null}
		</section>
	);
}

/** One class of status codes: a status icon and the class text, then the counts and buttons. */
function StatusClassRow({
	group,
	query,
	onQueryChange,
}: {
	group: StatusClass;
	query: string;
	onQueryChange(next: string): void;
}) {
	const codesId = useId();
	const [open, setOpen] = useState(false);
	const Chevron = open ? ChevronDownIcon : ChevronRightIcon;
	const first = group.codes[0];
	const tone = first && group.pattern ? statusTone(Number(first.value)) : "off";
	const disclosure = (
		<>
			<button
				type="button"
				aria-expanded={open}
				aria-controls={codesId}
				aria-label={`${open ? "Hide" : "Show"} ${group.label} codes`}
				title={`${open ? "Hide" : "Show"} ${group.label} codes`}
				onClick={() => setOpen((current) => !current)}
				className="-ml-5 grid size-4 shrink-0 place-items-center rounded-tip text-ink-3 outline-none hover:text-ink-1 focus-visible:focus-ring"
			>
				<Chevron aria-hidden className="size-3" />
			</button>
			<StatusIcon kind={statusKindOfTone(tone)} label="" />
			<span className="text-ink-1">{group.label}</span>
		</>
	);
	return (
		<>
			{group.pattern ? (
				<ValueRow
					facet="status"
					value={group.pattern}
					count={group.count}
					query={query}
					onQueryChange={onQueryChange}
					wildcard
				>
					{disclosure}
				</ValueRow>
			) : (
				<li className="flex items-center gap-1 rounded-ctl py-px pr-0.5 pl-6 hover:bg-white/5">
					<span className="flex min-w-0 flex-1 items-center gap-1.5 font-mono text-ink-2 text-xs">
						{disclosure}
					</span>
					<span className="font-mono text-ink-4 text-xs tabular-nums">
						{group.count}
					</span>
				</li>
			)}
			{open ? (
				<li className="pl-3">
					<ul id={codesId} className="flex flex-col">
						{group.codes.map(({ value, count }) => (
							<ValueRow
								key={value}
								facet="status"
								value={value}
								count={count}
								query={query}
								onQueryChange={onQueryChange}
							/>
						))}
					</ul>
				</li>
			) : null}
		</>
	);
}

/** Status by class, the exact codes behind each class's disclosure. */
function StatusSection({
	values,
	query,
	onQueryChange,
}: {
	values: FacetValue[];
	query: string;
	onQueryChange(next: string): void;
}) {
	const bodyId = useId();
	const [open, setOpen] = useState(true);
	const groups = useMemo(() => statusClassesOf(values), [values]);
	return (
		<section className="flex flex-col gap-1" aria-label={TITLES.status}>
			<SectionHeader
				title={TITLES.status}
				count={groups.length}
				open={open}
				bodyId={bodyId}
				onToggle={() => setOpen((current) => !current)}
			/>
			{open ? (
				<ul id={bodyId} className="flex flex-col">
					{groups.map((group) => (
						<StatusClassRow
							key={group.label}
							group={group}
							query={query}
							onQueryChange={onQueryChange}
						/>
					))}
				</ul>
			) : null}
		</section>
	);
}

/** Kind, method and path of the requests, and three quick duration ranges. */
function RequestsSection({
	stats,
	query,
	onQueryChange,
}: {
	stats: Counts;
	query: string;
	onQueryChange(next: string): void;
}) {
	const [open, setOpen] = useState(true);
	const bodyId = useId();
	const lists = REQUEST_ATTRS.map(({ path, title }) => ({
		path,
		title,
		values: topValues(stats.attrs?.get(path)?.values ?? new Map()),
	})).filter((list) => list.values.length > 0);
	const hasDuration = stats.attrs?.has("duration_ms") === true;
	if (lists.length === 0 && !hasDuration) return null;
	return (
		<section className="flex flex-col gap-1" aria-label="Requests">
			<SectionHeader
				title="Requests"
				count={lists.length + (hasDuration ? 1 : 0)}
				open={open}
				bodyId={bodyId}
				onToggle={() => setOpen((current) => !current)}
			/>
			{open ? (
				<div id={bodyId} className="flex flex-col gap-1.5">
					{lists.map(({ path, title, values }) => (
						<section key={path} aria-label={title}>
							<h3 className="type-micro pl-6">{title}</h3>
							<ValueList
								facet={`@${path}`}
								values={values}
								initial={REQUEST_ROWS}
								query={query}
								onQueryChange={onQueryChange}
							/>
						</section>
					))}
					{hasDuration ? (
						<section aria-label="Duration">
							<h3 className="type-micro pl-6">Duration</h3>
							<ul className="flex flex-col">
								{DURATION_RANGES.map(({ label, term }) => {
									const pressed = rawTermActive(query, term);
									return (
										<li key={term} className="pl-6">
											<button
												type="button"
												aria-pressed={pressed}
												title={`Filter ${term}`}
												onClick={() =>
													onQueryChange(
														toggleRawTerm(query, term, DURATION_TERMS),
													)
												}
												className={cn(
													"flex w-full items-center gap-1.5 rounded-ctl px-1 py-0.5 text-left font-mono text-ink-2 text-xs outline-none hover:bg-white/5 hover:text-ink-1 focus-visible:focus-ring",
													pressed &&
														"bg-accent-wash text-accent-ink ring-1 ring-accent-ink/60",
												)}
											>
												<span className="flex-1">{label}</span>
												{pressed ? (
													<CheckIcon aria-hidden className="size-3" />
												) : null}
											</button>
										</li>
									);
								})}
							</ul>
						</section>
					) : null}
				</div>
			) : null}
		</section>
	);
}

type Section =
	| { type: "values"; facet: FacetKey; values: FacetValue[] }
	| { type: "status"; values: FacetValue[] }
	| { type: "requests" };

/**
 * The values seen for each built-in facet, with counts (the buffer's, or the
 * server's for the whole range). Include and exclude edit the query text, so
 * the box stays the one source of truth and a click can be undone by typing.
 */
export function FacetPanel({
	stats,
	statsVersion,
	keys,
	query,
	onQueryChange,
	statusClasses = false,
	requests = false,
	className,
}: FacetPanelProps) {
	// biome-ignore lint/correctness/useExhaustiveDependencies: statsVersion stands for the mutable counts.
	const sections = useMemo(() => {
		const out: Section[] = [];
		for (const facet of keys) {
			const values = valuesFor(stats, facet);
			if (values.length === 0) continue;
			out.push(
				facet === "status" && statusClasses
					? { type: "status", values }
					: { type: "values", facet, values },
			);
		}
		if (requests) {
			// After Status when it is listed, else last.
			const at = out.findIndex((section) => section.type === "status");
			out.splice(at === -1 ? out.length : at + 1, 0, { type: "requests" });
		}
		return out;
	}, [keys, stats, statsVersion, statusClasses, requests]);
	const requestsListed = requests && hasRequestFacets(stats);
	const visible = sections.filter(
		(section) => section.type !== "requests" || requestsListed,
	);
	return (
		<div className={cn("flex flex-col gap-2", className)}>
			{visible.length === 0 ? (
				<p className="px-1 text-ink-3 text-xs leading-snug">
					No facets yet. Levels, services and statuses show up here once the
					lines carry them.
				</p>
			) : (
				visible.map((section) => {
					switch (section.type) {
						case "values":
							return (
								<FacetSection
									key={section.facet}
									facet={section.facet}
									values={section.values}
									query={query}
									onQueryChange={onQueryChange}
								/>
							);
						case "status":
							return (
								<StatusSection
									key="status"
									values={section.values}
									query={query}
									onQueryChange={onQueryChange}
								/>
							);
						default:
							return (
								<RequestsSection
									key="requests"
									stats={stats}
									query={query}
									onQueryChange={onQueryChange}
								/>
							);
					}
				})
			)}
		</div>
	);
}

/** Whether the counts have any request attribute to list. */
function hasRequestFacets(stats: Counts): boolean {
	const attrs = stats.attrs;
	if (!attrs) return false;
	return (
		attrs.has("duration_ms") ||
		REQUEST_ATTRS.some(({ path }) => (attrs.get(path)?.values.size ?? 0) > 0)
	);
}

/** The same panel in a popover, for a view too narrow to dock it. */
export function FacetsPopover(props: Omit<FacetPanelProps, "className">) {
	return (
		<Popover>
			<PopoverTrigger
				render={
					<Button variant="secondary" size="sm">
						<ListFilterIcon aria-hidden />
						Facets
					</Button>
				}
			/>
			<PopoverContent
				align="end"
				className="max-h-[min(60vh,420px)] w-[280px] overflow-auto overscroll-contain"
			>
				<FacetPanel {...props} />
			</PopoverContent>
		</Popover>
	);
}
