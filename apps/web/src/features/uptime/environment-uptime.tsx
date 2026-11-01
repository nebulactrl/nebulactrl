import { DitherUptimeBar } from "@nebulactrl/dither/charts/uptime-bar";
import { Link } from "@tanstack/react-router";
import { cn } from "cn";
import { ChevronDownIcon } from "lucide-react";
import { memo, useId, useMemo, useState } from "react";
import type { EnvironmentService } from "#/api/gen/types.gen";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { StatusIcon } from "#/components/ui/status-icon";
import { AvailabilityText } from "./availability-text";
import {
	checksStoppedAt,
	DAY_WORDS,
	foldedLabel,
	foldServices,
	formatUtcStamp,
	rowNote,
	serviceStateView,
	sortServices,
	uptimeDays,
} from "./model";
import { BAR_COLORS } from "./tones";

/** The column labels, wide layout only: the narrow one labels each figure itself. */
const HEAD = "type-micro";

/** Unfolded rows enter one stagger step apart, capped so a long list does not trickle in. */
const MAX_STAGGER_STEPS = 6;

const UptimeRow = memo(function UptimeRow({
	item,
	projectId,
	envSlug,
	reveal,
}: {
	item: EnvironmentService;
	projectId: string;
	envSlug: string;
	/** Position among the rows "Show more" just revealed; they enter in turn. */
	reveal?: number | undefined;
}) {
	const view = serviceStateView(item);
	const days = useMemo(() => uptimeDays(item.days), [item.days]);
	const note = rowNote(item);
	const stoppedAt = checksStoppedAt(item);
	return (
		<li
			className={cn(
				"relative grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 py-2.5 @min-[640px]:grid-cols-[minmax(0,15rem)_5.5rem_minmax(0,1fr)_5rem]",
				reveal !== undefined && "animate-nc-in [animation-fill-mode:backwards]",
			)}
			style={
				reveal !== undefined
					? {
							animationDelay: `calc(var(--duration-stagger) * ${Math.min(reveal, MAX_STAGGER_STEPS)})`,
						}
					: undefined
			}
		>
			<div className="flex min-w-0 flex-col gap-1.5">
				<Link
					to="/p/$projectId/$envSlug"
					params={{ projectId, envSlug }}
					search={{ service: item.serviceId, tab: "overview" }}
					className="flex min-w-0 items-center gap-2 rounded-ctl font-medium text-ink-1 text-sm leading-none outline-none after:absolute after:inset-0 hover:text-accent-ink focus-visible:focus-ring"
				>
					<StatusIcon kind={view.kind} label="" />
					<span className="truncate">{item.name}</span>
				</Link>
				<span className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1 pl-[22px] text-2xs text-ink-3 leading-none">
					{stoppedAt ? (
						<>
							<span className="shrink-0">
								No checks since {formatUtcStamp(stoppedAt)}
							</span>
							<Link
								to="/p/$projectId/$envSlug"
								params={{ projectId, envSlug }}
								search={{
									service: item.serviceId,
									tab: "settings",
									section: "operations",
								}}
								className="relative z-10 shrink-0 rounded-sm text-accent-ink underline underline-offset-2 outline-none focus-visible:focus-ring"
							>
								Monitor settings
							</Link>
						</>
					) : (
						<>
							<span className="shrink-0">{view.label}</span>
							{item.monitored && item.since ? (
								<span className="shrink-0">
									since <Relative iso={item.since} />
								</span>
							) : null}
						</>
					)}
					{item.monitored && item.lastCheckAt && !stoppedAt ? (
						<span className="shrink-0 @min-[640px]:hidden">
							· checked <Relative iso={item.lastCheckAt} />
						</span>
					) : null}
				</span>
				{note?.kind === "error" ? (
					<span
						title={note.text}
						className="truncate pl-[22px] font-mono text-2xs text-status-failed-on leading-none"
					>
						{note.text}
					</span>
				) : null}
				{note?.kind === "hint" ? (
					<span
						title="The last check failed. An outage opens after several in a row."
						className="flex items-center gap-1.5 pl-[22px] font-medium text-2xs text-status-staged-on leading-none"
					>
						<StatusIcon kind="warning" label="" />
						{note.text}
					</span>
				) : null}
			</div>
			<div className="flex flex-col items-end gap-1 @min-[640px]:items-start">
				<span className="type-micro @min-[640px]:hidden">30 days</span>
				<span className="font-mono text-ink-1 text-sm tabular-nums leading-none">
					<AvailabilityText percent={item.availability30d} />
				</span>
			</div>
			<div className="relative z-10 col-span-2 min-w-0 @min-[640px]:col-span-1">
				<DitherUptimeBar
					label={`${item.name}, last 30 days`}
					days={days}
					// The same figure as the column beside it, so the hidden table
					// does not round a different mean.
					{...(item.availability30d !== null
						? { uptime: item.availability30d / 100 }
						: {})}
					words={DAY_WORDS}
					colors={BAR_COLORS}
					legend={false}
					height={20}
					cell={2}
				/>
			</div>
			<span className="hidden text-right text-ink-3 text-xs @min-[640px]:block">
				{item.monitored && item.lastCheckAt ? (
					<Relative iso={item.lastCheckAt} />
				) : (
					"—"
				)}
			</span>
		</li>
	);
});

/**
 * One row per service of the environment: its state, 30-day availability, a
 * compact 30-day bar and the last check. Down and degraded services come
 * first. A row opens the service's inspector on its Overview. A long list
 * folds to its first rows plus any that need eyes; the rest sit behind a
 * button that says how many there are and what state they are in.
 */
export function EnvironmentUptimeList({
	items,
	projectId,
	envSlug,
}: {
	items: readonly EnvironmentService[];
	projectId: string;
	envSlug: string;
}) {
	const listId = useId();
	const [expanded, setExpanded] = useState(false);
	const sorted = useMemo(() => sortServices(items), [items]);
	const { shown, hidden } = useMemo(() => foldServices(sorted), [sorted]);
	if (sorted.length === 0) {
		return (
			<p className="surface-raised rounded-card px-4 py-3 text-ink-3 text-xs">
				No uptime yet. A service is checked once it is running.
			</p>
		);
	}
	const rows = expanded ? sorted : shown;
	return (
		<div className="@container surface-raised rounded-card px-4 py-1">
			<div className="hidden grid-cols-[minmax(0,15rem)_5.5rem_minmax(0,1fr)_5rem] gap-x-4 border-line-faint border-b py-2 @min-[640px]:grid">
				<span className={HEAD}>Service</span>
				<span className={HEAD}>30 days</span>
				<span className={HEAD}>Last 30 days, UTC</span>
				<span className={`${HEAD} text-right`}>Last check</span>
			</div>
			<ul id={listId} className="rule-rows">
				{rows.map((item) => {
					const reveal = expanded ? hidden.indexOf(item) : -1;
					return (
						<UptimeRow
							key={item.serviceId}
							item={item}
							projectId={projectId}
							envSlug={envSlug}
							reveal={reveal === -1 ? undefined : reveal}
						/>
					);
				})}
			</ul>
			{hidden.length > 0 ? (
				<div className="border-line-faint border-t py-2">
					<button
						type="button"
						aria-expanded={expanded}
						aria-controls={listId}
						onClick={() => setExpanded((open) => !open)}
						className="pressable flex w-fit cursor-pointer items-center gap-2 rounded-ctl py-1 pr-1.5 text-ink-3 text-xs leading-none outline-none hover:text-ink-1 focus-visible:focus-ring"
					>
						{/* Sits in the status icon's column, so the words line up with the names. */}
						<span
							aria-hidden
							className="flex size-3.5 items-center justify-center"
						>
							<ChevronDownIcon
								className={cn(
									"size-3.5 transition-transform duration-quick ease-out",
									expanded && "rotate-180",
								)}
							/>
						</span>
						{expanded ? "Show fewer" : foldedLabel(hidden)}
					</button>
				</div>
			) : null}
		</div>
	);
}

/** What stands in the list while it loads. */
export function EnvironmentUptimeSkeleton() {
	return (
		<div role="status" aria-label="Loading uptime">
			<Skeleton className="h-32 rounded-card" />
		</div>
	);
}

/** The list did not load; the rest of the page is unaffected. */
export function EnvironmentUptimeError({ retry }: { retry: () => void }) {
	return (
		<div className="flex flex-wrap items-center gap-3">
			<Alert variant="destructive" className="min-w-0 flex-1">
				<AlertDescription>
					Uptime did not load. The rest of the page is unaffected.
				</AlertDescription>
			</Alert>
			<Button variant="secondary" onClick={retry}>
				Try again
			</Button>
		</div>
	);
}
