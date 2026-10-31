/**
 * Pure geometry for the dithered charts: scales, ticks, monotone sampling,
 * band polygons and arcs. No DOM, no React; everything is covered by
 * `chart-math.test.ts`.
 */

export interface Pt {
	x: number;
	y: number;
}

/** Round to 0.1 px: sub-pixel precision is noise in a path string. */
const r1 = (value: number) => Math.round(value * 10) / 10;

/* ---------- scales and ticks ---------- */

/** A linear map from `domain` to `range`. A zero-width domain maps to the range's midpoint. */
export function linear(
	domain: readonly [number, number],
	range: readonly [number, number],
): (value: number) => number {
	const [d0, d1] = domain;
	const [r0, r1v] = range;
	const span = d1 - d0;
	if (span === 0) return () => (r0 + r1v) / 2;
	return (value) => r0 + ((value - d0) / span) * (r1v - r0);
}

/** A 1/2/5 × 10ⁿ step that gives about `target` intervals across `span`. */
export function niceStep(span: number, target: number): number {
	if (!(span > 0) || target < 1) return 1;
	const raw = span / target;
	const power = 10 ** Math.floor(Math.log10(raw));
	const frac = raw / power;
	const nice = frac < 1.5 ? 1 : frac < 3.5 ? 2 : frac < 7.5 ? 5 : 10;
	return nice * power;
}

/** Round ticks covering [min, max]; the first is ≤ min and the last ≥ max. */
export function niceTicks(min: number, max: number, target = 4): number[] {
	if (!Number.isFinite(min) || !Number.isFinite(max)) return [0, 1];
	if (max < min) [min, max] = [max, min];
	if (max === min) {
		const pad = max === 0 ? 1 : Math.abs(max) * 0.1;
		min -= pad;
		max += pad;
	}
	const step = niceStep(max - min, target);
	const start = Math.floor(min / step) * step;
	const end = Math.ceil(max / step) * step;
	const ticks: number[] = [];
	for (let value = start; value <= end + step / 2; value += step) {
		ticks.push(Math.round(value / step) * step);
	}
	return ticks;
}

const COMPACT = new Intl.NumberFormat("en", {
	notation: "compact",
	maximumFractionDigits: 1,
});

/** 1200 → "1.2K". The default axis and readout format. */
export function compact(value: number): string {
	return COMPACT.format(value);
}

export const clamp = (value: number, min: number, max: number) =>
	Math.min(max, Math.max(min, value));

/* ---------- curves ---------- */

const sign = (value: number) => (value < 0 ? -1 : 1);

/**
 * Tangents of a monotone-X cubic (Fritsch–Carlson): the curve passes through
 * every point and never overshoots a data value between two points.
 */
function monotoneTangents(pts: readonly Pt[]): number[] {
	const n = pts.length;
	const secants: number[] = [];
	for (let i = 0; i < n - 1; i += 1) {
		const a = pts[i];
		const b = pts[i + 1];
		if (!a || !b) continue;
		secants.push((b.y - a.y) / (b.x - a.x));
	}
	const tangents: number[] = new Array<number>(n).fill(0);
	for (let i = 1; i < n - 1; i += 1) {
		const s0 = secants[i - 1] ?? 0;
		const s1 = secants[i] ?? 0;
		const prev = pts[i - 1];
		const here = pts[i];
		const next = pts[i + 1];
		if (!prev || !here || !next || s0 * s1 <= 0) {
			tangents[i] = 0;
			continue;
		}
		const h0 = here.x - prev.x;
		const h1 = next.x - here.x;
		const p = (s0 * h1 + s1 * h0) / (h0 + h1);
		tangents[i] =
			(sign(s0) + sign(s1)) *
			Math.min(Math.abs(s0), Math.abs(s1), 0.5 * Math.abs(p));
	}
	if (n >= 2) {
		const first = secants[0] ?? 0;
		const last = secants[n - 2] ?? 0;
		tangents[0] = (3 * first - (tangents[1] ?? 0)) / 2;
		tangents[n - 1] = (3 * last - (tangents[n - 2] ?? 0)) / 2;
		// Same clamp as the interior: an end tangent may not overshoot its segment.
		if (first === 0) tangents[0] = 0;
		if (last === 0) tangents[n - 1] = 0;
	}
	return tangents;
}

/** Drop points whose x does not advance: a chart line is a function of x. */
function strictlyIncreasing(pts: readonly Pt[]): Pt[] {
	const out: Pt[] = [];
	for (const p of pts) {
		const last = out.at(-1);
		if (!last || p.x > last.x) out.push(p);
	}
	return out;
}

/**
 * A polyline that follows the curve through `pts`, a point about every
 * `step` px. Data points are always included, so the line never misses a
 * peak. `"linear"` returns the points themselves.
 */
export function sampleCurve(
	pts: readonly Pt[],
	curve: "monotone" | "linear" = "monotone",
	step = 3,
): Pt[] {
	const clean = strictlyIncreasing(pts);
	if (curve === "linear" || clean.length < 3) return clean;
	const tangents = monotoneTangents(clean);
	const out: Pt[] = [];
	for (let i = 0; i < clean.length - 1; i += 1) {
		const a = clean[i];
		const b = clean[i + 1];
		if (!a || !b) continue;
		const h = b.x - a.x;
		const m0 = tangents[i] ?? 0;
		const m1 = tangents[i + 1] ?? 0;
		const pieces = Math.max(1, Math.ceil(h / step));
		for (let k = 0; k < pieces; k += 1) {
			const u = k / pieces;
			const u2 = u * u;
			const u3 = u2 * u;
			out.push({
				x: a.x + u * h,
				y:
					(2 * u3 - 3 * u2 + 1) * a.y +
					(u3 - 2 * u2 + u) * h * m0 +
					(-2 * u3 + 3 * u2) * b.y +
					(u3 - u2) * h * m1,
			});
		}
	}
	const last = clean.at(-1);
	if (last) out.push(last);
	return out;
}

/** `M x y L x y …` for a polyline. Empty input gives an empty string. */
export function linePath(pts: readonly Pt[]): string {
	let d = "";
	for (let i = 0; i < pts.length; i += 1) {
		const p = pts[i];
		if (p) d += `${i === 0 ? "M" : "L"}${r1(p.x)} ${r1(p.y)}`;
	}
	return d;
}

/**
 * The closed region between two polylines sampled at the same x positions:
 * `top` forward, `bottom` back. `bottom` may be a constant y (a baseline).
 */
export function regionPath(
	top: readonly Pt[],
	bottom: readonly Pt[] | number,
): string {
	if (top.length === 0) return "";
	const lower: Pt[] =
		typeof bottom === "number"
			? [...top].reverse().map((p) => ({ x: p.x, y: bottom }))
			: [...bottom].reverse();
	return `${linePath(top)}${lower
		.map((p) => `L${r1(p.x)} ${r1(p.y)}`)
		.join("")}Z`;
}

export interface Band {
	/** Closed polygon, SVG user units. */
	path: string;
	/** Tone of this band, 0..1. */
	tone: number;
}

/**
 * Horizontal slices under a line, for a fill that is densest at the line:
 * band k covers the strip from `depth·k/bands` to `depth·(k+1)/bands` below
 * the curve (the last one runs on to `floor`). Clip the bands to the area's
 * region and the dither follows the line exactly, steep or flat.
 */
export function areaBands(
	top: readonly Pt[],
	{
		depth,
		tones,
		floor,
	}: {
		/** Distance in px over which the fill thins from the first tone to the last. */
		depth: number;
		/** One tone per band, densest first. */
		tones: readonly number[];
		/** Where the last band ends (the plot's bottom edge), or a very large number. */
		floor: number;
	},
): Band[] {
	const bands: Band[] = [];
	const n = tones.length;
	if (top.length === 0 || n === 0) return bands;
	const reversed = [...top].reverse();
	for (let k = 0; k < n; k += 1) {
		const tone = tones[k] ?? 0;
		const a = (depth * k) / n;
		const last = k === n - 1;
		const b = (depth * (k + 1)) / n;
		const upper = top.map((p) => `${r1(p.x)} ${r1(p.y + a)}`);
		const lowerPts = last
			? reversed.map((p) => `${r1(p.x)} ${r1(Math.max(floor, p.y + b))}`)
			: reversed.map((p) => `${r1(p.x)} ${r1(p.y + b)}`);
		bands.push({
			path: `M${upper.join("L")}L${lowerPts.join("L")}Z`,
			tone,
		});
	}
	return bands;
}

/* ---------- stacking ---------- */

/**
 * Cumulative stacks. `values[s][i]` is series s at point i; the result gives
 * each series' lower and upper edge. Negative inputs are clamped to 0 so
 * stacks never fold back.
 */
export function stack(values: readonly (readonly number[])[]): {
	lower: number[][];
	upper: number[][];
} {
	const points = Math.max(0, ...values.map((row) => row.length));
	const running: number[] = new Array<number>(points).fill(0);
	const lower: number[][] = [];
	const upper: number[][] = [];
	for (const row of values) {
		const lo: number[] = [];
		const hi: number[] = [];
		for (let i = 0; i < points; i += 1) {
			const base = running[i] ?? 0;
			const v = Math.max(0, row[i] ?? 0);
			lo.push(base);
			hi.push(base + v);
			running[i] = base + v;
		}
		lower.push(lo);
		upper.push(hi);
	}
	return { lower, upper };
}

/* ---------- polar ---------- */

/** Angle in radians measured clockwise from 12 o'clock. */
export function polar(cx: number, cy: number, r: number, angle: number): Pt {
	return { x: cx + r * Math.sin(angle), y: cy - r * Math.cos(angle) };
}

/** A ring sector (donut slice, gauge arc) between two angles, clockwise from 12 o'clock. */
export function arcPath(
	cx: number,
	cy: number,
	inner: number,
	outer: number,
	from: number,
	to: number,
): string {
	const sweep = clamp(to - from, 0, Math.PI * 2 - 1e-4);
	if (sweep <= 0) return "";
	const large = sweep > Math.PI ? 1 : 0;
	const end = from + sweep;
	const o0 = polar(cx, cy, outer, from);
	const o1 = polar(cx, cy, outer, end);
	const i1 = polar(cx, cy, inner, end);
	const i0 = polar(cx, cy, inner, from);
	if (inner <= 0) {
		return `M${cx} ${cy}L${r1(o0.x)} ${r1(o0.y)}A${outer} ${outer} 0 ${large} 1 ${r1(o1.x)} ${r1(o1.y)}Z`;
	}
	return `M${r1(o0.x)} ${r1(o0.y)}A${outer} ${outer} 0 ${large} 1 ${r1(o1.x)} ${r1(o1.y)}L${r1(i1.x)} ${r1(i1.y)}A${inner} ${inner} 0 ${large} 0 ${r1(i0.x)} ${r1(i0.y)}Z`;
}

/* ---------- value → level ---------- */

/**
 * Which of `levels` dither steps (1…levels) a value earns relative to `max`;
 * 0 means "no data". Any value above zero earns at least step 1, so a quiet
 * cell never reads as empty.
 */
export function levelOf(value: number, max: number, levels: number): number {
	if (!(value > 0) || !(max > 0)) return 0;
	return clamp(Math.ceil((value / max) * levels), 1, levels);
}

/** Evenly spaced tones for `levels` steps, from `min` (step 1) to `max` (the last). */
export function levelTone(
	level: number,
	levels: number,
	min = 0.12,
	max = 1,
): number {
	if (level <= 0) return 0;
	if (levels <= 1) return max;
	return min + ((max - min) * (clamp(level, 1, levels) - 1)) / (levels - 1);
}
