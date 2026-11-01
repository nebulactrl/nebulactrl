import {
	type CSSProperties,
	createElement,
	type JSX,
	type ReactNode,
	useEffect,
	useMemo,
	useSyncExternalStore,
} from "react";
import {
	type BayerSize,
	type DitherMatrix,
	inkedCells,
	matrixTileSide,
	type TileSide,
	tileRanks,
} from "./bayer";
import { ditherColorVar } from "./colors";

/**
 * Static dithers without a canvas: SVG `<pattern>` fills for charts and CSS
 * masks for blocks and bars. Everything here is pure string building, cached
 * by a small key (tone is snapped to the matrix's own steps, so a chart with
 * a thousand distinct tones still produces at most n² + 1 tiles per matrix).
 *
 * Colours are written into `style="fill:…"`, never a presentation attribute,
 * so `var(--dither-ink)` and `color-mix()` resolve through the cascade.
 */

/** Dither cell edge in CSS px when a fill does not say (matches `--dither-cell`). */
export const DEFAULT_CELL = 3;
const DEFAULT_N: BayerSize = 4;
/** Radius of a "dot" cell, in cells. Just under half so neighbours never touch. */
const DOT_RADIUS = 0.44;

export interface DitherFill {
	/** Share of cells inked, 0..1. */
	tone: number;
	/** CSS colour of the ink: `var(--dither-ink)`, `#fff`, `color-mix(…)`. */
	color: string;
	/** Cell edge in px. Default 3. */
	cell?: number;
	/** Matrix size. Default 4. Ignored when `matrix` is set. */
	n?: BayerSize;
	/** Threshold matrix. `"blue-noise"` uses a compact 16×16 tile. Wins over `n`. */
	matrix?: DitherMatrix;
	/** Square cells (crisp), round dots (halftone) or diagonal hatch lines whose weight follows the tone. Default square. */
	shape?: "square" | "dot" | "hatch";
}

interface Normalised {
	count: number;
	color: string;
	cell: number;
	n: TileSide;
	shape: "square" | "dot" | "hatch";
}

function normalise(fill: DitherFill): Normalised {
	const n: TileSide = fill.matrix
		? matrixTileSide(fill.matrix)
		: (fill.n ?? DEFAULT_N);
	return {
		count: inkedCells(fill.tone, n),
		color: fill.color,
		cell: Math.max(1, fill.cell ?? DEFAULT_CELL),
		n,
		shape: fill.shape ?? "square",
	};
}

const tilePaths = new Map<string, string>();

/**
 * One SVG path for the inked cells of an n×n tile, in cell units. Cells with
 * rank below `count` are inked, so tile(k) ⊂ tile(k+1): stacking a sparser
 * pattern under a denser one never changes the dense result.
 */
export function ditherTilePath(
	count: number,
	n: TileSide = DEFAULT_N,
	shape: "square" | "dot" | "hatch" = "square",
): string {
	const key = `${n}:${count}:${shape}`;
	const hit = tilePaths.get(key);
	if (hit !== undefined) return hit;
	const ranks = tileRanks(n);
	let path = "";
	for (let y = 0; y < n; y += 1) {
		let start = -1;
		for (let x = 0; x <= n; x += 1) {
			const inked =
				x < n &&
				(shape === "hatch"
					? count > 0 && (x + y) % n < Math.max(1, Math.round(count / n))
					: (ranks[y * n + x] ?? n * n) < count);
			if (shape === "dot") {
				if (inked) {
					const r = DOT_RADIUS;
					path += `M${x + 0.5 - r} ${y + 0.5}a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0 ${-2 * r} 0z`;
				}
			} else if (inked && start === -1) {
				start = x;
			} else if (!inked && start !== -1) {
				path += `M${start} ${y}h${x - start}v1h${start - x}z`;
				start = -1;
			}
		}
	}
	tilePaths.set(key, path);
	return path;
}

/** A stable short id for a fill, as it is registered in `<defs>`. */
export function ditherFillId(fill: DitherFill): string {
	const f = normalise(fill);
	const text = `${f.count}|${f.n}|${f.cell}|${f.shape}|${f.color}`;
	let hash = 2166136261;
	for (let i = 0; i < text.length; i += 1) {
		hash = Math.imul(hash ^ text.charCodeAt(i), 16777619);
	}
	return `nc-dither-${(hash >>> 0).toString(36)}`;
}

const escapeStyle = (value: string) =>
	value.replace(/["<>&]/g, (c) => `&#${c.charCodeAt(0)};`);

const markupCache = new Map<string, string>();

/**
 * Standalone `<pattern>` markup for a string-built `<defs>` (exported SVG
 * files, imperative DOM hosts). React trees use `DitherPatternDefs`, which
 * renders the same pattern as elements. The id is `ditherFillId(fill)`;
 * reference it as `url(#id)`.
 */
export function ditherPatternSvg(fill: DitherFill): string {
	const f = normalise(fill);
	const id = ditherFillId(fill);
	const hit = markupCache.get(id);
	if (hit !== undefined) return hit;
	const tile = f.n * f.cell;
	const ink = `style="fill:${escapeStyle(f.color)}"`;
	let body = "";
	if (f.count >= f.n * f.n && f.shape !== "dot") {
		body = `<rect width="${tile}" height="${tile}" ${ink}/>`;
	} else if (f.count > 0) {
		body = `<path d="${ditherTilePath(f.count, f.n, f.shape)}" transform="scale(${f.cell})" ${ink}/>`;
	}
	const crisp = f.shape !== "dot" ? ' shape-rendering="crispEdges"' : "";
	const markup = `<pattern id="${id}" width="${tile}" height="${tile}" patternUnits="userSpaceOnUse"${crisp}>${body}</pattern>`;
	markupCache.set(id, markup);
	return markup;
}

/** The `<pattern>` for a fill as a React element, keyed by its id. */
function ditherPatternElement(fill: DitherFill): JSX.Element {
	const f = normalise(fill);
	const id = ditherFillId(fill);
	const tile = f.n * f.cell;
	const style = { fill: f.color };
	let body: JSX.Element | null = null;
	if (f.count >= f.n * f.n && f.shape !== "dot") {
		body = createElement("rect", { width: tile, height: tile, style });
	} else if (f.count > 0) {
		body = createElement("path", {
			d: ditherTilePath(f.count, f.n, f.shape),
			transform: `scale(${f.cell})`,
			style,
		});
	}
	return createElement(
		"pattern",
		{
			key: id,
			id,
			width: tile,
			height: tile,
			patternUnits: "userSpaceOnUse",
			...(f.shape !== "dot" ? { shapeRendering: "crispEdges" } : {}),
		},
		body,
	);
}

const maskCache = new Map<string, string>();

/**
 * A `data:image/svg+xml` image of one tile, inked cells opaque: use it as a
 * CSS `mask-image` on an element painted in the ink colour. The image is
 * exactly `n * cell` px square, so `mask-size: ${n * cell}px` tiles it.
 */
export function ditherMaskDataUri(fill: Omit<DitherFill, "color">): string {
	const f = normalise({ ...fill, color: "" });
	const key = `${f.count}|${f.n}|${f.cell}|${f.shape}`;
	const hit = maskCache.get(key);
	if (hit !== undefined) return hit;
	const tile = f.n * f.cell;
	const shapeRendering =
		f.shape !== "dot" ? ' shape-rendering="crispEdges"' : "";
	const body =
		f.count >= f.n * f.n && f.shape !== "dot"
			? `<rect width="${f.n}" height="${f.n}"/>`
			: f.count > 0
				? `<path d="${ditherTilePath(f.count, f.n, f.shape)}"/>`
				: "";
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${tile}" height="${tile}" viewBox="0 0 ${f.n} ${f.n}"${shapeRendering}>${body}</svg>`;
	const uri = `data:image/svg+xml,${encodeURIComponent(svg)}`;
	maskCache.set(key, uri);
	return uri;
}

export type DitherMaskOptions = Pick<
	DitherFill,
	"cell" | "n" | "matrix" | "shape"
>;

/**
 * CSS for a block masked to one dither tone; paint it with `background-color`.
 * Takes a fill (`{ tone, cell }`) or just the tone: `ditherMaskStyle(0.25, { cell: 4 })`.
 */
export function ditherMaskStyle(
	fill: Omit<DitherFill, "color"> | number,
	options: DitherMaskOptions = {},
): CSSProperties {
	if (typeof fill === "number")
		return ditherMaskStyle({ tone: fill, ...options });
	const f = normalise({ ...fill, color: "" });
	const image = `url("${ditherMaskDataUri(fill)}")`;
	const size = `${f.n * f.cell}px`;
	return {
		WebkitMaskImage: image,
		WebkitMaskRepeat: "repeat",
		WebkitMaskSize: size,
		maskImage: image,
		maskRepeat: "repeat",
		maskSize: size,
	};
}

/**
 * Tone bands for a "densest at the line" fill: `steps` tones from `from`
 * (at the line) to `to` (far from it), evenly spaced. One step returns
 * `[from]`.
 */
export function ditherGradientStops(
	from: number,
	to: number,
	steps: number,
): number[] {
	const count = Math.max(1, Math.floor(steps));
	if (count === 1) return [from];
	return Array.from(
		{ length: count },
		(_, i) => from + ((to - from) * i) / (count - 1),
	);
}

/* ---------- ramp: a whole gradient in one mask ---------- */

/** The side of a box where a ramp starts (its `from` tone). */
export type RampEdge = "top" | "bottom" | "left" | "right";

export interface DitherRampOptions {
	/** The side of the box where the ramp is at `from`. */
	edge: RampEdge;
	/**
	 * The box's size along the ramp. A number is CSS px and snaps every band
	 * boundary to a whole cell row. Any string (`"30%"`, `"10rem"`) is taken as
	 * the element's own length: bands then sit at shares of the box and the
	 * boundaries fall where they fall.
	 */
	length: number | string;
	/** Tone (0..1) at the edge. */
	from: number;
	/** Tone (0..1) at the far end of the last band. */
	to: number;
	/** Bands along the ramp, evenly spaced from `from` to `to`. Default 16. */
	steps?: number;
	/** Cell edge in px. Default 3. */
	cell?: number;
	/** Matrix size. Default 4. Ignored when `matrix` is set. */
	n?: BayerSize;
	/** Threshold matrix. Wins over `n`. */
	matrix?: DitherMatrix;
	shape?: "square" | "dot" | "hatch";
}

/** One band of a ramp: inked cells of its tile and its extent along the axis. */
export interface RampBand {
	/** Inked cells of the matrix tile; 0 paints nothing. */
	count: number;
	/** Extent from the box's top or left: px for a numeric length, percent of the box otherwise. */
	lo: number;
	hi: number;
}

const RAMP_STEPS = 16;

/**
 * The bands of a ramp, nearest the edge first. Every band shares one tile
 * origin (the box's top-left), and each tile's inked cells are a subset of
 * the next denser one, so a boundary never moves a cell, it only adds cells.
 * With a numeric length each boundary sits on a whole cell row.
 */
export function ditherRampBands(options: DitherRampOptions): RampBand[] {
	const n: TileSide = options.matrix
		? matrixTileSide(options.matrix)
		: (options.n ?? DEFAULT_N);
	const cell = Math.max(1, options.cell ?? DEFAULT_CELL);
	const steps = Math.max(1, Math.floor(options.steps ?? RAMP_STEPS));
	const px = typeof options.length === "number";
	const span = px ? Math.max(0, options.length as number) : 100;
	const fromStart = options.edge === "top" || options.edge === "left";
	// Where a share `f` of the way in from the edge sits, from the box's top/left.
	const place = (f: number) => {
		const p = fromStart ? f * span : span - f * span;
		if (!px || f <= 0 || f >= 1) return p;
		return Math.min(span, Math.round(p / cell) * cell);
	};
	const bands: RampBand[] = [];
	for (let k = 0; k < steps; k += 1) {
		const tone =
			steps === 1
				? options.from
				: options.from + ((options.to - options.from) * k) / (steps - 1);
		const a = place(k / steps);
		const b = place((k + 1) / steps);
		const lo = Math.min(a, b);
		const hi = Math.max(a, b);
		if (hi <= lo) continue;
		bands.push({
			count: inkedCells(Math.min(1, Math.max(0, tone)), n),
			lo,
			hi,
		});
	}
	return bands;
}

const rampCache = new Map<string, string>();
const trim = (value: number) => `${Math.round(value * 1000) / 1000}`;

/**
 * A `data:image/svg+xml` mask for a whole gradient: one tile wide across the
 * ramp and as long as the ramp, bands of rising threshold drawn from one
 * pattern origin. Inked cells are opaque. Pair it with `ditherRampStyle`,
 * which sizes and repeats it; the box must be as long as `length`.
 */
export function ditherRamp(options: DitherRampOptions): string {
	const bands = ditherRampBands(options);
	const n: TileSide = options.matrix
		? matrixTileSide(options.matrix)
		: (options.n ?? DEFAULT_N);
	const cell = Math.max(1, options.cell ?? DEFAULT_CELL);
	const shape = options.shape ?? "square";
	const px = typeof options.length === "number";
	const vertical = options.edge === "top" || options.edge === "bottom";
	const key = `${options.edge}|${px ? options.length : `s${options.length}`}|${n}|${cell}|${shape}|${bands.map((b) => `${b.count}:${trim(b.lo)}:${trim(b.hi)}`).join(",")}`;
	const hit = rampCache.get(key);
	if (hit !== undefined) return hit;

	// Top-to-bottom, with neighbours of equal density joined into one rect.
	const runs: RampBand[] = [];
	for (const band of [...bands].sort((a, b) => a.lo - b.lo)) {
		if (band.count <= 0) continue;
		const last = runs[runs.length - 1];
		if (last && last.count === band.count && last.hi === band.lo)
			last.hi = band.hi;
		else runs.push({ ...band });
	}
	const tile = n * cell;
	const solid = n * n;
	const ids = new Map<number, string>();
	let rects = "";
	const unit = px ? "" : "%";
	for (const run of runs) {
		let fill = "#000";
		if (run.count < solid || shape === "dot") {
			let id = ids.get(run.count);
			if (id === undefined) {
				id = `r${ids.size}`;
				ids.set(run.count, id);
			}
			fill = `url(#${id})`;
		}
		const at = `${trim(run.lo)}${unit}`;
		const extent = `${trim(run.hi - run.lo)}${unit}`;
		rects += vertical
			? `<rect y="${at}" width="100%" height="${extent}" fill="${fill}"/>`
			: `<rect x="${at}" width="${extent}" height="100%" fill="${fill}"/>`;
	}
	let defs = "";
	for (const [count, id] of ids) {
		defs += `<pattern id="${id}" width="${tile}" height="${tile}" patternUnits="userSpaceOnUse"><path transform="scale(${cell})" d="${ditherTilePath(count, n, shape)}" fill="#000"/></pattern>`;
	}
	const along = px ? `${options.length}` : "100%";
	const [w, h] = vertical ? [`${tile}`, along] : [along, `${tile}`];
	const crisp = shape !== "dot" ? ' shape-rendering="crispEdges"' : "";
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}"${crisp}><defs>${defs}</defs>${rects}</svg>`;
	const uri = `data:image/svg+xml,${encodeURIComponent(svg)}`;
	rampCache.set(key, uri);
	return uri;
}

/** The mask layers of a ramp as plain CSS values. */
export function ditherRampMask(options: DitherRampOptions): {
	image: string;
	size: string;
	repeat: string;
	position: string;
} {
	const n: TileSide = options.matrix
		? matrixTileSide(options.matrix)
		: (options.n ?? DEFAULT_N);
	const tile = `${n * Math.max(1, options.cell ?? DEFAULT_CELL)}px`;
	const along =
		typeof options.length === "number" ? `${options.length}px` : "100%";
	const vertical = options.edge === "top" || options.edge === "bottom";
	const far = options.edge === "bottom" || options.edge === "right";
	return {
		image: `url("${ditherRamp(options)}")`,
		size: vertical ? `${tile} ${along}` : `${along} ${tile}`,
		repeat: vertical ? "repeat-x" : "repeat-y",
		// Hold the image to the edge it starts from.
		position: vertical ? `0 ${far ? "100%" : "0"}` : `${far ? "100%" : "0"} 0`,
	};
}

/**
 * CSS for a block masked by a ramp: one mask image and no children. Paint it
 * with `background-color`; size the block to `length` along the ramp.
 */
export function ditherRampStyle(options: DitherRampOptions): CSSProperties {
	const m = ditherRampMask(options);
	return {
		WebkitMaskImage: m.image,
		WebkitMaskRepeat: m.repeat,
		WebkitMaskSize: m.size,
		WebkitMaskPosition: m.position,
		maskImage: m.image,
		maskRepeat: m.repeat,
		maskSize: m.size,
		maskPosition: m.position,
	};
}

/* ---------- registry: patterns shared across a page ---------- */

const registry = new Map<string, DitherFill>();
const listeners = new Set<() => void>();
let version = 0;
let scheduled = false;
let providers = 0;
let host: SVGSVGElement | null = null;

function syncHost() {
	if (!host) return;
	const defs = host.querySelector("defs");
	if (defs) {
		defs.innerHTML = [...registry.values()].map(ditherPatternSvg).join("");
	}
}

function schedule() {
	if (scheduled) return;
	scheduled = true;
	queueMicrotask(() => {
		scheduled = false;
		version += 1;
		syncHost();
		for (const listener of listeners) listener();
	});
}

function register(fill: DitherFill): string {
	const id = ditherFillId(fill);
	if (!registry.has(id)) {
		registry.set(id, fill);
		schedule();
	}
	// A foreign node on <body>, created at render time so the very first paint
	// already finds its patterns (a passive effect could land after it).
	ensureHost();
	return id;
}

const NS = "http://www.w3.org/2000/svg";

function ensureHost() {
	if (host || providers > 0 || typeof document === "undefined") return;
	host = document.createElementNS(NS, "svg");
	host.setAttribute("aria-hidden", "true");
	host.setAttribute("width", "0");
	host.setAttribute("height", "0");
	host.style.position = "absolute";
	host.style.pointerEvents = "none";
	host.appendChild(document.createElementNS(NS, "defs"));
	document.body.appendChild(host);
	syncHost();
}

function removeHost() {
	host?.remove();
	host = null;
}

const subscribe = (listener: () => void) => {
	listeners.add(listener);
	return () => {
		listeners.delete(listener);
	};
};
const getVersion = () => version;

/**
 * Renders the page's registered patterns once, in a hidden zero-size `<svg>`
 * (not `display:none`: browsers skip patterns in a non-rendered tree).
 * Optional: without it `useDitherFillUrl` mounts an equivalent host itself.
 */
export function DitherDefs({
	children,
}: {
	children?: ReactNode;
}): JSX.Element {
	useSyncExternalStore(subscribe, getVersion, getVersion);
	useEffect(() => {
		providers += 1;
		removeHost();
		return () => {
			providers -= 1;
			if (providers === 0 && registry.size > 0) ensureHost();
		};
	}, []);
	return createElement(
		"svg",
		{
			"aria-hidden": true,
			width: 0,
			height: 0,
			style: { position: "absolute", pointerEvents: "none" },
		},
		createElement(
			"defs",
			null,
			[...registry.values()].map(ditherPatternElement),
		),
		children,
	);
}

/**
 * `url(#nc-dither-…)` for a fill, registered once for the whole page. The
 * id depends only on the fill's values, so re-renders with an equal fill
 * return the same string and touch nothing.
 */
export function useDitherFillUrl(fill: DitherFill): string {
	// `register` is idempotent and keyed by the fill's values, so an equal
	// fill built every render resolves to the same id and touches nothing.
	return `url(#${register(fill)})`;
}

/**
 * `useDitherFillUrl` for several fills at once (the bands of a gradient
 * fill): one memo, one registration pass. `fills` must be stable by value;
 * the memo is keyed on their ids.
 */
export function useDitherFillUrls(fills: readonly DitherFill[]): string[] {
	const key = fills.map(ditherFillId).join(",");
	// biome-ignore lint/correctness/useExhaustiveDependencies: keyed by the fills' ids; equal ids mean equal fills
	return useMemo(() => fills.map((fill) => `url(#${register(fill)})`), [key]);
}

/**
 * Inline `<defs>` for a chart that owns its `<svg>`: one element holding
 * every pattern the chart needs. Pass a stable `fills` array (memoise it) so
 * the markup string is built once.
 */
export function DitherPatternDefs({
	fills,
}: {
	fills: readonly DitherFill[];
}): JSX.Element {
	const patterns = useMemo(() => fills.map(ditherPatternElement), [fills]);
	return createElement("defs", null, patterns);
}

export interface DitherPatternProps {
	/** The id to reference as `fill="url(#id)"`. */
	id: string;
	/** Share of cells inked, 0..1. */
	level: number;
	/** Cell edge in SVG user units. Default 3. */
	cell?: number;
	/** Threshold matrix. Default `"bayer4"`. */
	matrix?: DitherMatrix;
	/** Ink colour: a CSS colour or a name (`"blue"`). Default `--dither-ink`. */
	color?: string;
	/** Colour behind the ink. Default none (transparent). */
	background?: string;
	shape?: DitherFill["shape"];
}

/**
 * An SVG `<pattern>` for a dither fill, to place in `<defs>` and reference from
 * any shape: `<rect fill="url(#half)" />`. Renders on the server and follows
 * the theme through CSS variables.
 */
export function DitherPattern({
	id,
	level,
	cell,
	matrix = "bayer4",
	color = "ink",
	background,
	shape,
}: DitherPatternProps): JSX.Element {
	const f = normalise({
		tone: level,
		color: ditherColorVar(color),
		matrix,
		...(cell !== undefined ? { cell } : {}),
		...(shape !== undefined ? { shape } : {}),
	});
	const tile = f.n * f.cell;
	const style = { fill: f.color };
	let ink: JSX.Element | null = null;
	if (f.count >= f.n * f.n && f.shape !== "dot") {
		ink = createElement("rect", { width: tile, height: tile, style });
	} else if (f.count > 0) {
		ink = createElement("path", {
			d: ditherTilePath(f.count, f.n, f.shape),
			transform: `scale(${f.cell})`,
			style,
		});
	}
	return createElement(
		"pattern",
		{
			id,
			width: tile,
			height: tile,
			patternUnits: "userSpaceOnUse",
			...(f.shape !== "dot" ? { shapeRendering: "crispEdges" } : {}),
		},
		background
			? createElement("rect", {
					width: tile,
					height: tile,
					style: { fill: ditherColorVar(background) },
				})
			: null,
		ink,
	);
}
