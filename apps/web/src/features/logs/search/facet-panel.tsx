import { cn } from "cn";
import {
	ChevronDownIcon,
	ChevronRightIcon,
	ListFilterIcon,
	MinusIcon,
	PlusIcon,
} from "lucide-react";
import { useId, useMemo, useState } from "react";
import { Button } from "#/components/ui/button";
import {
	Popover,
	PopoverContent,
	PopoverTrigger,
} from "#/components/ui/popover";
import {
	type FacetMode,
	type FacetStats,
	type FacetValue,
	facetTermState,
	toggleFacetTerm,
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

export interface FacetPanelProps {
	stats: Pick<FacetStats, "facets">;
	/** Changes whenever the counts do. */
	statsVersion: number;
	/** Which facets to offer, in order. */
	keys: readonly FacetKey[];
	query: string;
	onQueryChange(next: string): void;
	className?: string;
}

function valuesFor(
	stats: Pick<FacetStats, "facets">,
	key: FacetKey,
): FacetValue[] {
	const values = topValues(stats.facets[key]);
	if (key !== "level") return values;
	return values.sort(
		(a, b) => LEVEL_ORDER.indexOf(a.value) - LEVEL_ORDER.indexOf(b.value),
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
	const [all, setAll] = useState(false);
	const shown = all ? values : values.slice(0, INITIAL_ROWS);
	const Chevron = open ? ChevronDownIcon : ChevronRightIcon;

	return (
		<section className="flex flex-col gap-1" aria-label={TITLES[facet]}>
			<button
				type="button"
				aria-expanded={open}
				aria-controls={bodyId}
				onClick={() => setOpen((current) => !current)}
				className="flex items-center gap-1.5 rounded-ctl px-1 py-1 text-left font-medium text-ink-2 text-xs outline-none hover:text-ink-1 focus-visible:focus-ring"
			>
				<Chevron aria-hidden className="size-3.5 shrink-0 text-ink-3" />
				<span className="flex-1">{TITLES[facet]}</span>
				<span className="font-mono text-ink-4 tabular-nums">
					{values.length}
				</span>
			</button>
			{open ? (
				<ul id={bodyId} className="flex flex-col">
					{shown.map(({ value, count }) => {
						const state = facetTermState(query, facet, value);
						return (
							<li
								key={value}
								className="flex items-center gap-1 rounded-ctl py-px pr-0.5 pl-6 hover:bg-white/5"
							>
								<span
									className={cn(
										"min-w-0 flex-1 truncate font-mono text-xs",
										state === "exclude"
											? "text-ink-4 line-through"
											: "text-ink-2",
									)}
									title={value}
								>
									{value}
								</span>
								<span className="font-mono text-ink-4 text-xs tabular-nums">
									{count}
								</span>
								<ModeButton
									mode="include"
									pressed={state === "include"}
									facet={facet}
									value={value}
									onClick={() =>
										onQueryChange(
											toggleFacetTerm(query, facet, value, "include"),
										)
									}
								/>
								<ModeButton
									mode="exclude"
									pressed={state === "exclude"}
									facet={facet}
									value={value}
									onClick={() =>
										onQueryChange(
											toggleFacetTerm(query, facet, value, "exclude"),
										)
									}
								/>
							</li>
						);
					})}
					{values.length > INITIAL_ROWS ? (
						<li className="pl-6">
							<button
								type="button"
								onClick={() => setAll((current) => !current)}
								className="rounded-ctl px-1 py-0.5 text-ink-3 text-xs outline-none hover:text-ink-1 focus-visible:focus-ring"
							>
								{all
									? "Show fewer"
									: `Show ${values.length - INITIAL_ROWS} more`}
							</button>
						</li>
					) : null}
				</ul>
			) : null}
		</section>
	);
}

function ModeButton({
	mode,
	pressed,
	facet,
	value,
	onClick,
}: {
	mode: FacetMode;
	pressed: boolean;
	facet: FacetKey;
	value: string;
	onClick(): void;
}) {
	const Icon = mode === "include" ? PlusIcon : MinusIcon;
	return (
		<button
			type="button"
			aria-pressed={pressed}
			aria-label={`${mode === "include" ? "Include" : "Exclude"} ${facet}:${value}`}
			title={`${mode === "include" ? "Include" : "Exclude"} ${facet}:${value}`}
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

/**
 * The values seen in the buffer for each built-in facet, with counts.
 * Include and exclude edit the query text, so the box stays the one source of
 * truth and a click can be undone by typing.
 */
export function FacetPanel({
	stats,
	statsVersion,
	keys,
	query,
	onQueryChange,
	className,
}: FacetPanelProps) {
	// biome-ignore lint/correctness/useExhaustiveDependencies: statsVersion stands for the mutable counts.
	const sections = useMemo(
		() =>
			keys
				.map((facet) => ({ facet, values: valuesFor(stats, facet) }))
				.filter((section) => section.values.length > 0),
		[keys, stats, statsVersion],
	);
	return (
		<div className={cn("flex flex-col gap-2", className)}>
			{sections.length === 0 ? (
				<p className="px-1 text-ink-3 text-xs leading-snug">
					No facets yet. Levels, services and statuses show up here once the
					lines carry them.
				</p>
			) : (
				sections.map(({ facet, values }) => (
					<FacetSection
						key={facet}
						facet={facet}
						values={values}
						query={query}
						onQueryChange={onQueryChange}
					/>
				))
			)}
		</div>
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
