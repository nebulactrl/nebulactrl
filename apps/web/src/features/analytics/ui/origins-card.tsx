import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import { Link } from "@tanstack/react-router";
import { cn } from "cn";
import { ChevronDownIcon, GlobeIcon, Maximize2Icon } from "lucide-react";
import {
	type ReactNode,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { Button } from "#/components/ui/button";
import { compactNumber } from "#/features/metrics/chart-format";
import { ChartCard } from "#/features/metrics/ui/chart-card";
import {
	fitBounds,
	formatShare,
	type GeoPlace,
	geoPlaces,
	hasOrigins,
	REGIONS,
} from "../geo/model";
import { foldRows, type OriginRow } from "../geo/rows";
import type { TrafficGeo, TrafficReport } from "../types";
import { OriginsDialog, OriginsMap } from "./origins-map";
import { PaneHeader } from "./pane-header";

const NO_KEYS: readonly string[] = [];

export interface OriginsCardProps {
	report: Pick<TrafficReport, "geo" | "geoReason">;
	/** The server whose traffic settings the empty states link to; without one, the servers list. */
	serverId?: string | undefined;
	className?: string;
}

const OFF_COPY = {
	geo_disabled:
		"Geolocation is off. Turn it on in the server's traffic settings to see which countries requests come from.",
	traffic_disabled:
		"Traffic analytics is off, so there are no origins. Turn it on in the server's settings.",
} as const;

function SettingsLink({
	serverId,
	label,
}: {
	serverId: string | undefined;
	label: string;
}) {
	return (
		<Button
			variant="secondary"
			size="sm"
			nativeButton={false}
			render={
				serverId ? (
					<Link
						to="/servers/$serverId/settings/$section"
						params={{ serverId, section: "monitoring" }}
					/>
				) : (
					<Link to="/servers" />
				)
			}
		>
			{label}
		</Button>
	);
}

/**
 * The data licence's credit: a quiet one-line footnote at the card's bottom
 * edge, still a real link (the licence asks for one on every view that shows
 * the data). `ink-3` is set on the link itself so the global accent link
 * colour does not win: 7.1:1 on the card (`surface-raised`), 5.5:1 on the
 * dialog's `surface-pop`. No underline until hover (fine pointers only) or keyboard focus,
 * and the global 2px focus ring shows on focus.
 */
function Attribution({ geo }: { geo: TrafficGeo }) {
	const credits = geo.attribution ?? [];
	if (credits.length === 0) return null;
	return (
		<p className="text-2xs text-ink-3 leading-snug">
			{credits.map((a, i) => (
				<span key={a.source}>
					{i > 0 ? " · " : null}
					<a
						href={a.url}
						target="_blank"
						rel="noreferrer noopener"
						className="rounded-xs text-ink-3 no-underline hover:underline focus-visible:underline"
					>
						{a.text}
					</a>
				</span>
			))}
		</p>
	);
}

interface Lit {
	id: string;
	keys: readonly string[];
}

interface Highlighter {
	/** The row under the pointer or focus, and the row pinned by a click. */
	hover: Lit | null;
	pin: Lit | null;
	onHover: (lit: Lit | null) => void;
	onPin: (lit: Lit) => void;
}

function OriginList({
	listId,
	title,
	noun,
	otherLabel,
	rows,
	highlighter,
}: {
	listId: string;
	title: string;
	/** What the rows are, plural: "countries". */
	noun: string;
	otherLabel: string;
	rows: readonly OriginRow[];
	highlighter: Highlighter;
}) {
	const [expanded, setExpanded] = useState(false);
	const folded = foldRows(rows, otherLabel, expanded);
	const shown: (OriginRow & { count?: number })[] = folded.other
		? [...folded.rows, folded.other]
		: folded.rows;
	const max = shown[0]?.share ?? 0;
	const { hover, pin, onHover, onPin } = highlighter;
	return (
		<div className="flex min-w-0 flex-col gap-2">
			<PaneHeader title={title} subtitle={`${rows.length} ${noun}`} />
			<ul className="flex flex-col">
				{shown.map((row) => {
					const lit = { id: `${listId}:${row.key}`, keys: row.placeKeys };
					const other = folded.other?.key === row.key && row.count;
					return (
						<li key={row.key}>
							<button
								type="button"
								aria-pressed={pin?.id === lit.id}
								title={other ? `${row.count} ${noun} under 1%` : row.label}
								onPointerEnter={() => onHover(lit)}
								onPointerLeave={() => onHover(null)}
								onFocus={() => onHover(lit)}
								onBlur={() => onHover(null)}
								onClick={() => onPin(lit)}
								className={cn(
									"flex w-full cursor-pointer flex-col gap-1.5 rounded-ctl px-2 py-1.5 text-left outline-none transition-[background-color] duration-quick hover:bg-white/5 focus-visible:focus-ring aria-pressed:bg-white/5",
									hover?.id === lit.id && "bg-white/5",
								)}
							>
								<span className="flex w-full min-w-0 items-center gap-2">
									<span
										className={cn(
											"min-w-0 flex-1 truncate font-mono text-xs",
											other ? "text-ink-3" : "text-ink-1",
										)}
									>
										{row.label}
									</span>
									<span className="w-12 shrink-0 text-right font-medium font-mono text-ink-1 text-xs tabular-nums">
										{compactNumber(row.requests)}
									</span>
									<span className="w-10 shrink-0 text-right font-mono text-2xs text-ink-3 tabular-nums">
										{formatShare(row.share)}
									</span>
								</span>
								<span aria-hidden className="block w-full">
									<DitherProgressBar
										inline
										hideLabel
										size="sm"
										label={row.label}
										value={
											max > 0
												? Math.min(Math.max((row.share / max) * 100, 2), 100)
												: 0
										}
										color="ink"
									/>
								</span>
							</button>
						</li>
					);
				})}
			</ul>
			{folded.other || expanded ? (
				<button
					type="button"
					aria-expanded={expanded}
					onClick={() => setExpanded((v) => !v)}
					className="pressable flex w-fit cursor-pointer items-center gap-1 rounded-ctl px-2 py-1 text-ink-3 text-xs outline-none hover:text-ink-1 focus-visible:focus-ring"
				>
					{expanded ? "Show fewer" : `Show all ${rows.length} ${noun}`}
					<ChevronDownIcon
						aria-hidden
						className={cn(
							"size-3 transition-transform duration-quick ease-out",
							expanded && "rotate-180",
						)}
					/>
				</button>
			) : null}
		</div>
	);
}

/**
 * The empty states keep the map: the world stays on screen, unlit, with one
 * sentence and the next step. Turned-off geolocation dims the map further so
 * "nothing yet" and "off" never look alike. The sentence lies over the map
 * where the card is wide; on a phone it sits under it, so the map and the
 * card's title stay in view.
 */
function OriginsEmpty({
	title,
	text,
	action,
	serverId,
	attribution,
	off,
	className,
}: {
	title: string;
	text: string;
	/** The settings button's label: where the next step is. */
	action: string;
	serverId: string | undefined;
	attribution?: ReactNode;
	off: boolean;
	className?: string;
}) {
	return (
		<ChartCard title={title} {...(className ? { className } : {})}>
			<div className="relative flex flex-col gap-3">
				<OriginsMap
					places={[]}
					interactive={false}
					className={off ? "opacity-35" : "opacity-60"}
				/>
				<div className="flex justify-center @md:absolute @md:inset-x-0 @md:bottom-3 @md:px-3">
					<div className="surface-float flex max-w-[560px] flex-wrap items-center gap-x-3 gap-y-2 rounded-card px-3.5 py-2.5">
						<GlobeIcon aria-hidden className="size-4 shrink-0 text-ink-3" />
						<p className="min-w-0 flex-1 basis-60 text-ink-2 text-xs leading-snug">
							{text}
						</p>
						<SettingsLink serverId={serverId} label={action} />
					</div>
				</div>
			</div>
			{attribution}
		</ChartCard>
	);
}

/** Under this width (CSS pixels) the inline map frames the data instead of the world. */
const COMPACT_PX = 400;
/** The tightest the compact map frames the data, in degrees of latitude: enough land around one place to read it. */
const COMPACT_MIN_SPAN = 40;

/**
 * The card's map. In a narrow column the whole world is a thin strip, so it
 * frames the located places instead (the fit button and the zoom buttons
 * reach the rest); wide, it shows the world.
 */
function InlineMap({
	places,
	highlight,
}: {
	places: readonly GeoPlace[];
	highlight: readonly string[];
}) {
	const ref = useRef<HTMLDivElement>(null);
	const [compact, setCompact] = useState(false);
	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		const ro = new ResizeObserver(([entry]) => {
			if (entry) setCompact(entry.contentRect.width < COMPACT_PX);
		});
		ro.observe(el);
		return () => ro.disconnect();
	}, []);
	const bounds = useMemo(
		() =>
			compact ? fitBounds(places, 0.25, COMPACT_MIN_SPAN) : REGIONS[0].bounds,
		[compact, places],
	);
	return (
		<div ref={ref} className="min-w-0 self-center">
			<OriginsMap
				controls
				places={places}
				bounds={bounds}
				highlight={highlight}
			/>
		</div>
	);
}

/**
 * "Where requests come from": a dot map with the busiest places marked (sized
 * and lit by share) beside the top countries and cities. Counts come from the
 * agent's local GeoIP lookup; client IPs never leave the server.
 */
export function OriginsCard({ report, serverId, className }: OriginsCardProps) {
	const geo = report.geo;
	const title = "Where requests come from";
	const [expanded, setExpanded] = useState(false);
	const [hover, setHover] = useState<Lit | null>(null);
	const [pin, setPin] = useState<Lit | null>(null);
	const places = useMemo(() => (geo ? geoPlaces(geo) : []), [geo]);

	if (!geo) {
		const copy = OFF_COPY[report.geoReason ?? "geo_disabled"];
		return (
			<OriginsEmpty
				title={title}
				text={copy}
				action="Open traffic settings"
				serverId={serverId}
				off
				{...(className ? { className } : {})}
			/>
		);
	}

	if (!hasOrigins(geo)) {
		return (
			<OriginsEmpty
				title={title}
				text="No located requests yet. Origins appear once the GeoIP database has downloaded and public requests arrive."
				action="Check the GeoIP database"
				serverId={serverId}
				attribution={<Attribution geo={geo} />}
				off={false}
				{...(className ? { className } : {})}
			/>
		);
	}

	// A country row lights every place of that country, a city row its own.
	const countries: OriginRow[] = (geo.countries ?? []).map((c) => ({
		key: c.iso2,
		label: c.name,
		requests: c.requests,
		share: c.share,
		placeKeys: places.filter((p) => p.country === c.iso2).map((p) => p.key),
	}));
	const cities: OriginRow[] =
		geo.level === "city"
			? (geo.cities ?? []).map((c) => {
					const key = `${c.name}|${c.country}|${c.lat}|${c.lon}`;
					return {
						key,
						label: `${c.name}, ${c.country}`,
						requests: c.requests,
						share: c.share,
						placeKeys: [key],
					};
				})
			: [];
	const unknown = geo.unknownRequests;
	const subtitle = `${compactNumber(geo.locatedRequests)} requests located${
		unknown > 0
			? ` · ${compactNumber(unknown)} from unlisted or private addresses`
			: ""
	} · ${geo.level} level`;
	const highlighter: Highlighter = {
		hover,
		pin,
		onHover: setHover,
		onPin: (lit) => setPin((p) => (p?.id === lit.id ? null : lit)),
	};
	const highlight = (hover ?? pin)?.keys ?? NO_KEYS;
	const lists = (
		<>
			<OriginList
				listId="countries"
				title="Top countries"
				noun="countries"
				otherLabel="Other countries"
				rows={countries}
				highlighter={highlighter}
			/>
			{cities.length > 0 ? (
				<OriginList
					listId="cities"
					title="Top cities"
					noun="cities"
					otherLabel="Other cities"
					rows={cities}
					highlighter={highlighter}
				/>
			) : null}
		</>
	);

	return (
		<ChartCard
			title={title}
			subtitle={subtitle}
			actions={
				<Button
					variant="ghost"
					size="icon-sm"
					aria-label="Expand the map"
					onClick={() => setExpanded(true)}
				>
					<Maximize2Icon />
				</Button>
			}
			{...(className ? { className } : {})}
		>
			<div className="grid gap-4 @2xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
				<InlineMap places={places} highlight={highlight} />
				<div className="flex min-w-0 flex-col gap-4">{lists}</div>
			</div>
			<Attribution geo={geo} />
			<OriginsDialog
				open={expanded}
				onOpenChange={setExpanded}
				places={places}
				highlight={highlight}
				subtitle={subtitle}
				lists={lists}
				footer={<Attribution geo={geo} />}
			/>
		</ChartCard>
	);
}
