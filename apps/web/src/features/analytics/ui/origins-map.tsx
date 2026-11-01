import { DitherDotMap } from "@nebulactrl/dither/backgrounds/dot-map";
import {
	boundsAspect,
	type MapBounds,
} from "@nebulactrl/dither/backgrounds/geo";
import { cn } from "cn";
import { Maximize2Icon, MinusIcon, PlusIcon } from "lucide-react";
import { useReducedMotion } from "motion/react";
import {
	type CSSProperties,
	memo,
	useEffect,
	useId,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Kbd } from "#/components/ui/kbd";
import { Segmented } from "#/components/ui/segmented";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import { compactNumber } from "#/features/metrics/chart-format";
import {
	type LabelMarker,
	labelCap,
	placeLabels,
	type Size,
} from "../geo/labels";
import {
	canZoom,
	fitBounds,
	formatShare,
	type GeoPlace,
	MAX_MARKERS,
	REGIONS,
	type RegionId,
	zoomBounds,
} from "../geo/model";
import {
	fillAspect,
	fitProjection,
	inProjection,
	type Projection,
	projectPoint,
	sameBounds,
	transformCss,
	viewTransform,
} from "../geo/projection";
import {
	enterFrom,
	KEY_ZOOM,
	zoomModifier,
	zoomRange,
	zoomWindow,
} from "../geo/view";
import { PaneHeader } from "./pane-header";
import { useMapView } from "./use-map-view";

/** Land in a dim grey so the accent markers and their halos carry the map. */
const LAND = "--color-line-strong";

/** Pixels from one dot to the next: the card's small map, and the large view's. */
export const CARD_PITCH = 3;
export const DIALOG_PITCH = 4;

/**
 * The shape (width / height) the large view's box takes beside the lists, and
 * the one it prefers below 900px, where it shrinks to a region's own shape
 * rather than leave bands the grid cannot fill (`fillAspect`).
 */
export const DIALOG_ASPECT = 1.6;

/** A pan or zoom of the map: 300ms on the app's in-out curve (`--ease-pulse`). */
const MOVE = { duration: 300, easing: "cubic-bezier(0.77, 0, 0.175, 1)" };

/** Where a hand left the map, drawn again: a 150ms fade (`--duration-quick`) over the layer that was moved. */
const FADE = { duration: 150, easing: "ease-out" };

/** A label chip: IBM Plex Mono at 11px advances 6.6px a character, plus 6px of padding each side. */
const CHIP_HEIGHT = 18;
const measureChip = (label: string) => ({
	width: Math.ceil(label.length * 6.6) + 12,
	height: CHIP_HEIGHT,
});

/** A dot is this share of its cell, as the dot map's shader draws it. */
const DOT_SHARE = 0.74;

const NO_KEYS: readonly string[] = [];

const canAnimate =
	typeof Element !== "undefined" &&
	typeof Element.prototype.animate === "function";

function placeSummary(place: GeoPlace) {
	return `${place.label}: ${compactNumber(place.requests)} requests, ${formatShare(place.share)}`;
}

/** The map box's size in CSS pixels, for fitting the grid and laying out labels. */
function useBoxSize() {
	const ref = useRef<HTMLDivElement>(null);
	const [size, setSize] = useState({ width: 0, height: 0 });
	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		const ro = new ResizeObserver(([entry]) => {
			if (!entry) return;
			const { width, height } = entry.contentRect;
			setSize((s) =>
				s.width === width && s.height === height ? s : { width, height },
			);
		});
		ro.observe(el);
		return () => ro.disconnect();
	}, []);
	return [ref, size] as const;
}

/** One drawing of the world: a projection's dots and the markers lit on them. */
const DotLayer = memo(function DotLayer({
	projection,
	markers,
}: {
	projection: Projection;
	markers: readonly { at: GeoPlace["at"]; label: string; weight: number }[];
}) {
	const { bounds } = projection;
	// The halo follows the view, so a zoomed-in map keeps visible glows.
	const halo = Math.min(14, Math.max(1.5, (bounds.north - bounds.south) / 7));
	return (
		<DitherDotMap
			animate={false}
			markers={markers}
			columns={projection.columns}
			halo={halo}
			color={LAND}
			accent="ink"
			bounds={bounds}
			labels={false}
			title="Where requests come from"
			className="absolute inset-0"
		/>
	);
});

/**
 * A place lit on the map by a hovered list row: the dots around it turn to
 * the accent ink in two steps, every dot of the grid close in and every
 * other dot (one in four) further out. A denser tone, never a glow: the
 * pattern is the dot map's own grid, laid over it with a mask.
 */
function Highlight({ x, y, pitch }: { x: number; y: number; pitch: number }) {
	const dot = pitch * DOT_SHARE;
	const inset = (pitch - dot) / 2;
	const ring = (step: number, radius: number) => {
		const cell = pitch * step;
		const span = Math.ceil((radius * 2) / cell + 2) * cell;
		const left = Math.floor((x - radius) / cell) * cell;
		const top = Math.floor((y - radius) / cell) * cell;
		const stripe = (axis: "90deg" | "180deg") =>
			`linear-gradient(${axis}, transparent ${inset}px, #000 ${inset}px ${inset + dot}px, transparent ${inset + dot}px)`;
		const style: CSSProperties = {
			left,
			top,
			width: span,
			height: span,
			background: "var(--color-accent-ink)",
			clipPath: `circle(${radius}px at ${x - left}px ${y - top}px)`,
			maskImage: `${stripe("90deg")}, ${stripe("180deg")}`,
			maskSize: `${cell}px 100%, 100% ${cell}px`,
			maskRepeat: "repeat-x, repeat-y",
			maskComposite: "intersect",
			WebkitMaskComposite: "source-in",
		};
		return <span aria-hidden className="absolute" style={style} />;
	};
	return (
		<>
			{ring(2, pitch * 6)}
			{ring(1, pitch * 3)}
		</>
	);
}

interface Layer {
	id: number;
	/** The bounds the caller asked for; `projection.bounds` is those widened to the box. */
	request: MapBounds;
	projection: Projection;
	/**
	 * Drawn where a hand left the map: it fades in over the layer it replaces,
	 * which stays as the hand left it (moved by the view) until the fade ends.
	 */
	settle?: boolean;
}

interface LayerState {
	layers: Layer[];
	seq: number;
}

/**
 * How a new projection replaces the old: `glide` pans and zooms between the
 * two (a region or a button), `settle` fades in over a moved layer (a hand
 * let go) and `swap` replaces it at once.
 */
type Change = "glide" | "settle" | "swap";

/** The layers after the box or the request changed to `target`. */
function advance(
	state: LayerState,
	request: MapBounds,
	target: Projection,
	change: Change,
): LayerState {
	const last = state.layers.at(-1);
	if (
		last &&
		(change === "settle" ||
			(change === "glide" && !sameBounds(last.request, request)))
	) {
		const id = state.seq + 1;
		return {
			layers: [
				last,
				{
					id,
					request,
					projection: target,
					...(change === "settle" ? { settle: true } : {}),
				},
			],
			seq: id,
		};
	}
	if (last) {
		return {
			layers: [
				...state.layers.slice(0, -1),
				{ id: last.id, request, projection: target },
			],
			seq: state.seq,
		};
	}
	const id = state.seq + 1;
	return { layers: [{ id, request, projection: target }], seq: id };
}

const sameProjection = (a: Projection, b: Projection) =>
	a.columns === b.columns &&
	a.rows === b.rows &&
	a.x === b.x &&
	a.y === b.y &&
	sameBounds(a.bounds, b.bounds);

/** Where a hand left the map: the projection it shows, for the box and pitch it was made in. */
interface Manual {
	projection: Projection;
	box: Size;
	pitch: number;
	/** Whether the redraw fades in (a pointer or wheel) or replaces at once (keys, reduced motion). */
	fade: boolean;
}

/** The modifier name for the hint and the help text. */
const modifier = zoomModifier(
	typeof navigator === "undefined" ? "" : navigator.platform,
);

/** The hint about the zoom modifier shows once per session. */
const HINT_KEY = "nebula:map-zoom-hint";
const HINT_MS = 1200;
let hinted = false;

function takeHint(): boolean {
	if (hinted) return false;
	try {
		if (window.sessionStorage.getItem(HINT_KEY)) {
			hinted = true;
			return false;
		}
		window.sessionStorage.setItem(HINT_KEY, "1");
	} catch {
		// Storage is blocked: once per page load is as close as it gets.
	}
	hinted = true;
	return true;
}

export interface OriginsMapProps {
	places: readonly GeoPlace[];
	bounds?: MapBounds;
	/** CSS pixels from one dot to the next: the grid has as many dots as fit the box. */
	pitch?: number;
	/** Keys of the places to light up (a hovered list row). */
	highlight?: readonly string[];
	/**
	 * Fill the box the parent gives (a sized, positioned parent) instead of
	 * taking the region's own shape from the width.
	 */
	fill?: boolean;
	/** Pan and zoom to a new region over 300ms. Off, a new region replaces the old at once. */
	glide?: boolean;
	/**
	 * Take the wheel (with ⌘ or Ctrl, or a trackpad pinch), drag, touch and
	 * keys. Off, the map is a picture: an empty state's backdrop.
	 */
	interactive?: boolean;
	/** A hand moved the map and let go: the bounds it shows now. */
	onViewChange?: (bounds: MapBounds) => void;
	/** `0` was pressed: the parent's own fitted view. Without it the map returns to `bounds`. */
	onFit?: () => void;
	/**
	 * Zoom out, zoom in and fit buttons in the map's corner, for hands without
	 * a wheel or a keyboard. The dialog has its own beside the map.
	 */
	controls?: boolean;
	/** Merged over the box's own style (a filled box takes its shape from here). */
	style?: CSSProperties;
	className?: string;
}

/**
 * The dot map: as many square dots as the box holds, the busiest places lit
 * in the accent colour and named beside their markers where there is room,
 * and one focusable hotspot per place whose tooltip names it and its count
 * (the hotspot is the place's accessible name, so a label left off the map
 * is still read out and shown on hover and focus).
 *
 * A new region pans and zooms: the old drawing and the new one cross-fade
 * while each moves into the other's framing, on `transform` and `opacity`
 * only. Labels wait for the move to end.
 *
 * A hand moves the map directly: ⌘ or Ctrl with the wheel, or a pinch,
 * zooms about the pointer; a drag pans (past 4px, so a click on a marker
 * still clicks), with momentum and a rubber band at the edge; the arrow
 * keys, + - and 0 do the same from the keyboard. The layer follows the
 * input on `transform` with no transition; 100ms after the last movement
 * the dots are drawn again at the new bounds and the labels are placed.
 */
export function OriginsMap({
	places,
	bounds = REGIONS[0].bounds,
	pitch = CARD_PITCH,
	highlight = NO_KEYS,
	fill = false,
	glide = true,
	interactive = true,
	onViewChange,
	onFit,
	controls = false,
	style,
	className,
}: OriginsMapProps) {
	const shown = useMemo(() => places.slice(0, MAX_MARKERS), [places]);
	const markers = useMemo(
		() => shown.map((p) => ({ at: p.at, label: p.label, weight: p.weight })),
		[shown],
	);
	const [boxRef, box] = useBoxSize();
	const reduced = useReducedMotion() ?? false;
	const helpId = useId();

	// Where a hand left the map; a new region from the caller takes over from it.
	const [manual, setManual] = useState<Manual | null>(null);
	const [asked, setAsked] = useState(bounds);
	if (!sameBounds(asked, bounds)) {
		setAsked(bounds);
		if (manual && !sameBounds(manual.projection.bounds, bounds)) {
			setManual(null);
		}
	}
	// `0` jumps to the fitted view: no glide for that one change.
	const [jump, setJump] = useState(false);
	const move = glide && canAnimate && !reduced && !jump;

	const request = manual?.projection.bounds ?? bounds;
	const target = useMemo(
		() =>
			manual && manual.box === box && manual.pitch === pitch
				? manual.projection
				: fitProjection(request, box, pitch),
		[manual, request, box, pitch],
	);
	const [state, setState] = useState<LayerState>({ layers: [], seq: 0 });
	const last = state.layers.at(-1);
	// Keep the layers in step with the box and the region while rendering, so
	// no frame draws the old projection in the new box.
	if (target && !(last && sameProjection(last.projection, target))) {
		const change: Change =
			manual && target === manual.projection
				? manual.fade
					? "settle"
					: "swap"
				: move
					? "glide"
					: "swap";
		setState(advance(state, request, target, change));
	}
	if (jump && target && last && sameProjection(last.projection, target)) {
		setJump(false);
	}
	const moving = state.layers.length > 1;
	const current = last?.projection;
	// Touch pans with one finger only once the map is zoomed in; before that the page scrolls.
	const zoomedIn = current ? zoomRange(current.bounds).min < 0.999 : false;

	const layerEls = useRef(new Map<number, HTMLDivElement>());
	const overlayRef = useRef<HTMLDivElement>(null);
	const byKey = useMemo(() => new Map(shown.map((p) => [p.key, p])), [shown]);
	// What the effects and the gesture handlers read at the moment they run.
	const latest = useRef({ layers: state.layers, byKey, box, pitch });
	latest.current = { layers: state.layers, byKey, box, pitch };

	const [hint, setHint] = useState(false);
	const hintTimer = useRef(0);
	useEffect(() => () => window.clearTimeout(hintTimer.current), []);

	const view = useMapView({
		boxRef,
		targets: () => {
			const layer = latest.current.layers.at(-1);
			const el = layer ? layerEls.current.get(layer.id) : undefined;
			return [el, overlayRef.current].filter((n) => n != null);
		},
		projection: current,
		enabled: interactive && current !== undefined,
		reduced,
		touchPan: zoomedIn,
		grab: () => {
			const layers = latest.current.layers;
			const entering = layers.length > 1 ? layers.at(-1) : undefined;
			const el = entering ? layerEls.current.get(entering.id) : undefined;
			if (!el) return null;
			// Stop the glide where it is: the frame on screen becomes the view.
			const m = new DOMMatrixReadOnly(getComputedStyle(el).transform);
			const moved = [
				...layerEls.current.values(),
				...(overlayRef.current?.querySelectorAll<HTMLElement>("[data-place]") ??
					[]),
			];
			for (const node of moved) {
				for (const a of node.getAnimations()) a.cancel();
			}
			setState((s) =>
				s.layers.length > 1 ? { ...s, layers: s.layers.slice(-1) } : s,
			);
			return { x: m.e, y: m.f, k: m.a };
		},
		commit: (next, fade) => {
			const base = latest.current.layers.at(-1)?.projection;
			if (!base) return;
			setManual({
				projection: { ...base, bounds: next },
				box: latest.current.box,
				pitch: latest.current.pitch,
				fade: fade && canAnimate,
			});
			onViewChange?.(next);
		},
		fit: () => {
			setJump(true);
			setManual(null);
			onFit?.();
		},
		scrolled: () => {
			if (!takeHint()) return;
			setHint(true);
			hintTimer.current = window.setTimeout(() => setHint(false), HINT_MS);
		},
	});

	const pair = moving
		? `${state.layers.at(-2)?.id}>${state.layers.at(-1)?.id}`
		: "";
	// biome-ignore lint/correctness/useExhaustiveDependencies: `pair` is the whole of what starts a move; everything else is read from `latest`.
	useLayoutEffect(() => {
		if (!pair) return;
		const [from, to] = latest.current.layers.slice(-2);
		if (!from || !to) return;
		const entering = layerEls.current.get(to.id);
		const leaving = layerEls.current.get(from.id);
		if (!entering) return;
		// What the hand had made of the old layer when this began, if it had.
		const was = view.release();
		const animations: Animation[] = [];
		const finished = (a: Animation) =>
			a.finished.then(
				() =>
					setState((s) =>
						s.layers.length > 1 && s.layers.at(-1)?.id === to.id
							? { ...s, layers: s.layers.slice(-1) }
							: s,
					),
				() => {},
			);
		if (to.settle) {
			// The old layer keeps the hand's view; the crisp one fades in over it.
			const fade = entering.animate([{ opacity: 0 }, { opacity: 1 }], FADE);
			animations.push(fade);
			finished(fade);
			return () => {
				for (const a of animations) a.cancel();
			};
		}
		const enter = entering.animate(
			[
				{
					transform: transformCss(
						enterFrom(
							was,
							viewTransform(from.projection, to.projection),
							from.projection,
							to.projection,
						),
					),
					opacity: 0,
				},
				{ transform: "none", opacity: 1 },
			],
			MOVE,
		);
		animations.push(enter);
		if (leaving) {
			animations.push(
				leaving.animate(
					[
						{
							transform: transformCss({
								x: was.x,
								y: was.y,
								scaleX: was.k,
								scaleY: was.k,
							}),
						},
						{
							transform: transformCss(
								viewTransform(to.projection, from.projection),
							),
						},
					],
					{ ...MOVE, fill: "forwards" },
				),
			);
		}
		// The hotspots are invisible, but their focus ring travels with the map.
		for (const el of overlayRef.current?.querySelectorAll<HTMLElement>(
			"[data-place]",
		) ?? []) {
			const place = latest.current.byKey.get(el.dataset.place ?? "");
			if (!place) continue;
			const before = projectPoint(from.projection, place.at);
			const after = projectPoint(to.projection, place.at);
			const dx = from.projection.x + before.x - (to.projection.x + after.x);
			const dy = from.projection.y + before.y - (to.projection.y + after.y);
			animations.push(
				el.animate(
					[{ transform: `translate(${dx}px, ${dy}px)` }, { transform: "none" }],
					MOVE,
				),
			);
		}
		finished(enter);
		return () => {
			for (const a of animations) a.cancel();
		};
	}, [pair]);

	// A new projection is drawn at rest: whatever the hand had put on the layer
	// and the hotspots is spent (the layer a fade replaces keeps its own).
	// biome-ignore lint/correctness/useExhaustiveDependencies: runs when the drawn projection changes; the rest is read from `latest`.
	useLayoutEffect(() => {
		view.release();
		const layer = latest.current.layers.at(-1);
		const el = layer ? layerEls.current.get(layer.id) : undefined;
		if (el) el.style.transform = "";
		if (overlayRef.current) overlayRef.current.style.transform = "";
	}, [current]);

	const lit = useMemo(() => new Set(highlight), [highlight]);
	const spots = useMemo(
		() =>
			current
				? shown
						.filter((p) => inProjection(current, p.at))
						.map((place) => ({
							place,
							...projectPoint(current, place.at),
						}))
				: [],
		[shown, current],
	);
	// Placed for the projection last drawn: a hand moving the map does not
	// re-place them each frame, the redraw after it does.
	const labels = useMemo(() => {
		if (!current) return { placed: [], hidden: [] };
		const latSpan = current.bounds.north - current.bounds.south;
		const points: LabelMarker[] = spots.map(({ place, x, y }) => ({
			key: place.key,
			label: place.label,
			count: place.requests,
			x,
			y,
		}));
		return placeLabels(
			points,
			{ width: current.width, height: current.height },
			measureChip,
			{ max: labelCap(latSpan, current) },
		);
	}, [spots, current]);

	return (
		<div
			ref={boxRef}
			{...(interactive
				? {
						role: "application",
						tabIndex: 0,
						"aria-roledescription": "interactive map",
						"aria-label": "Map of where requests come from",
						"aria-describedby": helpId,
					}
				: {})}
			className={cn(
				"group/map relative select-none overflow-hidden",
				interactive && "cursor-grab data-[dragging]:cursor-grabbing",
				interactive && (zoomedIn ? "touch-none" : "touch-pan-x touch-pan-y"),
				className,
			)}
			style={fill ? style : { aspectRatio: boundsAspect(bounds), ...style }}
		>
			{state.layers.map((layer) => (
				<div
					key={layer.id}
					ref={(el) => {
						if (el) layerEls.current.set(layer.id, el);
						else layerEls.current.delete(layer.id);
					}}
					aria-hidden={layer === last ? undefined : true}
					className="absolute"
					style={{
						left: layer.projection.x,
						top: layer.projection.y,
						width: layer.projection.width,
						height: layer.projection.height,
						transformOrigin: "0 0",
					}}
				>
					<DotLayer projection={layer.projection} markers={markers} />
					{layer === last
						? spots
								.filter(({ place }) => lit.has(place.key))
								.map(({ place, x, y }) => (
									<Highlight key={place.key} x={x} y={y} pitch={pitch} />
								))
						: null}
				</div>
			))}
			{current ? (
				<div
					ref={overlayRef}
					className="pointer-events-none absolute"
					style={{
						left: current.x,
						top: current.y,
						width: current.width,
						height: current.height,
						transformOrigin: "0 0",
					}}
				>
					{spots.map(({ place, x, y }) => (
						<Tooltip key={place.key}>
							<TooltipTrigger
								render={
									<button
										type="button"
										data-place={place.key}
										aria-label={placeSummary(place)}
										className="pointer-events-auto absolute -mt-3 -ml-3 size-6 cursor-[inherit] rounded-full outline-none focus-visible:ring-2 focus-visible:ring-accent-ink group-data-[gesture]/map:pointer-events-none"
										style={{ left: x, top: y }}
									/>
								}
							/>
							<TooltipContent>
								<span className="font-mono">{place.label}</span>
								<span className="font-mono text-ink-3 tabular-nums">
									{compactNumber(place.requests)} · {formatShare(place.share)}
								</span>
							</TooltipContent>
						</Tooltip>
					))}
					<div
						aria-hidden
						className={cn(
							"transition-opacity duration-quick ease-out group-data-[gesture]/map:opacity-0",
							moving && "opacity-0",
						)}
					>
						{labels.placed.map((label) => (
							<span
								key={label.key}
								className="surface-pop pointer-events-none absolute flex items-center whitespace-nowrap rounded-tip px-1.5 font-mono text-[11px] text-ink-1 leading-none"
								style={{
									left: label.left,
									top: label.top,
									width: label.width,
									height: label.height,
								}}
							>
								{label.label}
							</span>
						))}
					</div>
				</div>
			) : null}
			{interactive ? (
				<>
					<span id={helpId} className="sr-only">
						{`Interactive map. Drag to pan. Hold ${modifier.name} and scroll, or pinch, to zoom. With the map focused, the arrow keys pan, plus and minus zoom, and 0 fits the map to the data.`}
					</span>
					<div
						aria-hidden
						className={cn(
							"pointer-events-none absolute inset-x-0 bottom-3 flex justify-center transition-opacity duration-quick ease-out",
							hint ? "opacity-100" : "opacity-0",
						)}
					>
						<span className="surface-pop flex items-center gap-1.5 rounded-tip px-2 py-1.5 font-medium text-ink-1 text-xs leading-[1.4]">
							Hold <Kbd variant="key">{modifier.glyph}</Kbd> to zoom
						</span>
					</div>
				</>
			) : null}
			{interactive && controls ? (
				<div className="absolute top-2 right-2 flex gap-1">
					<Button
						variant="secondary"
						size="icon-sm"
						aria-label="Zoom out"
						disabled={!current || zoomRange(current.bounds).min >= 0.999}
						onClick={() => view.zoomBy(1 / KEY_ZOOM)}
					>
						<MinusIcon />
					</Button>
					<Button
						variant="secondary"
						size="icon-sm"
						aria-label="Zoom in"
						disabled={!current || zoomRange(current.bounds).max <= 1.001}
						onClick={() => view.zoomBy(KEY_ZOOM)}
					>
						<PlusIcon />
					</Button>
					<Button
						variant="secondary"
						size="icon-sm"
						aria-label="Fit to data"
						disabled={!manual}
						onClick={() => view.fit()}
					>
						<Maximize2Icon />
					</Button>
				</div>
			) : null}
		</div>
	);
}

type View = RegionId | "fit" | "custom";

const VIEW_OPTIONS: { value: View; label: string }[] = [
	{ value: "fit", label: "Fit to data" },
	...REGIONS.map((r) => ({ value: r.id as View, label: r.label })),
];

export interface OriginsDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	places: readonly GeoPlace[];
	/** The ranked lists and the attribution, rendered beside the map. */
	lists: React.ReactNode;
	/** One quiet line under the map and the lists: the data credit. */
	footer?: React.ReactNode;
	/** Keys of the places a hovered list row lights on the map. */
	highlight?: readonly string[];
	subtitle: string;
}

/**
 * The large view: a dense map that fills its column and pans and zooms to a
 * region, to the located places or step by step, with every place named on
 * hover and focus and the busiest named on the map. Two columns from 900px
 * (the map, then the lists); the lists go under the map below that.
 */
export function OriginsDialog({
	open,
	onOpenChange,
	places,
	lists,
	highlight,
	subtitle,
	footer,
}: OriginsDialogProps) {
	const [view, setView] = useState<View>("fit");
	const [custom, setCustom] = useState<MapBounds>(REGIONS[0].bounds);
	// A key press jumps to the new view; only a pointer glides.
	const [byKeyboard, setByKeyboard] = useState(false);
	const fitted = useMemo(
		() => fitBounds(places.slice(0, MAX_MARKERS)),
		[places],
	);
	const bounds =
		view === "custom"
			? custom
			: view === "fit"
				? fitted
				: (REGIONS.find((r) => r.id === view)?.bounds ?? REGIONS[0].bounds);
	const zoom = canZoom(bounds);
	const step = (factor: number) => {
		setCustom(
			view === "custom"
				? zoomWindow(bounds, factor)
				: zoomBounds(bounds, factor),
		);
		setView("custom");
	};
	const regionName =
		view === "fit"
			? "Fit to data"
			: view === "custom"
				? "Zoomed"
				: (REGIONS.find((r) => r.id === view)?.label ?? "World");

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent width={1180} top={6} showCloseButton>
				<DialogHeader>
					<DialogTitle>Where requests come from</DialogTitle>
					<p className="text-ink-3 text-xs">{subtitle}</p>
				</DialogHeader>
				<DialogBody className="grid grid-cols-1 gap-x-6 gap-y-5 min-[900px]:grid-cols-[minmax(0,1fr)_320px]">
					<div className="flex min-w-0 flex-col gap-3">
						<PaneHeader
							title="Map"
							subtitle={`${regionName} · ${places.length} ${places.length === 1 ? "place" : "places"} located`}
						/>
						<div
							className="flex flex-wrap items-center gap-2"
							onKeyDownCapture={() => setByKeyboard(true)}
							onPointerDownCapture={() => setByKeyboard(false)}
						>
							<Segmented<View>
								aria-label="Map region"
								font="sans"
								value={view}
								onValueChange={setView}
								options={VIEW_OPTIONS}
							/>
							<span className="ml-auto flex gap-1">
								<Button
									variant="secondary"
									size="icon-sm"
									aria-label="Zoom out"
									disabled={!zoom.out}
									onClick={() => step(1.6)}
								>
									<MinusIcon />
								</Button>
								<Button
									variant="secondary"
									size="icon-sm"
									aria-label="Zoom in"
									disabled={!zoom.in}
									onClick={() => step(1 / 1.6)}
								>
									<PlusIcon />
								</Button>
							</span>
						</div>
						<OriginsMap
							fill
							places={places}
							bounds={bounds}
							pitch={DIALOG_PITCH}
							glide={!byKeyboard}
							onViewChange={(next) => {
								setCustom(next);
								setView("custom");
							}}
							onFit={() => {
								setByKeyboard(true);
								setView("fit");
							}}
							{...(highlight ? { highlight } : {})}
							style={
								{
									"--map-aspect": fillAspect(DIALOG_ASPECT, bounds),
								} as CSSProperties
							}
							className="aspect-(--map-aspect) max-h-[calc(100dvh-20rem)] w-full min-[900px]:aspect-[1.6] min-[900px]:min-h-64"
						/>
					</div>
					{/* The lists scroll inside the map's height: they never set it. */}
					<div className="relative min-w-0 min-[900px]:min-h-0">
						<div className="flex flex-col gap-5 min-[900px]:absolute min-[900px]:inset-0 min-[900px]:overflow-y-auto min-[900px]:pr-1">
							{lists}
						</div>
					</div>
					{footer ? (
						<div className="min-w-0 min-[900px]:col-span-2">{footer}</div>
					) : null}
				</DialogBody>
			</DialogContent>
		</Dialog>
	);
}
