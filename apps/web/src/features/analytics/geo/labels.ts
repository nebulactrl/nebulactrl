/** A place to name on the map, positioned in CSS pixels of the map box (origin top left). */
export interface LabelMarker {
	key: string;
	label: string;
	/** Requests from this place: the busiest are named first and win a contested spot. */
	count: number;
	x: number;
	y: number;
}

export interface Size {
	width: number;
	height: number;
}

/** Where a label sits against its marker. */
type LabelSide = "right" | "left" | "top" | "bottom";

export interface PlacedLabel {
	key: string;
	label: string;
	side: LabelSide;
	/** Top left corner of the label's box, in CSS pixels of the map box. */
	left: number;
	top: number;
	width: number;
	height: number;
}

export interface LabelPlacement {
	/** Labels to draw, busiest place first. */
	placed: PlacedLabel[];
	/** Keys of the markers in the box left without a label; their tooltip names them. */
	hidden: string[];
}

export interface PlaceLabelsOptions {
	/** Most labels to draw. Default: no cap. */
	max?: number;
	/** Distance from the marker's centre to the label's near edge. Default 10. */
	gap?: number;
	/** Half the side of the box every marker keeps clear of other labels. Default 5. */
	marker?: number;
	/** Space kept between two labels. Default 2. */
	pad?: number;
}

/** The order a label tries the spots around its marker: beside it first, then above and below. */
const SIDES: readonly LabelSide[] = ["right", "left", "top", "bottom"];

interface Rect {
	l: number;
	t: number;
	r: number;
	b: number;
}

const hits = (a: Rect, b: Rect) =>
	a.l < b.r && a.r > b.l && a.t < b.b && a.b > b.t;

function labelRect(
	side: LabelSide,
	m: LabelMarker,
	size: Size,
	gap: number,
): Rect {
	switch (side) {
		case "right":
			return {
				l: m.x + gap,
				t: m.y - size.height / 2,
				r: m.x + gap + size.width,
				b: m.y + size.height / 2,
			};
		case "left":
			return {
				l: m.x - gap - size.width,
				t: m.y - size.height / 2,
				r: m.x - gap,
				b: m.y + size.height / 2,
			};
		case "top":
			return {
				l: m.x - size.width / 2,
				t: m.y - gap - size.height,
				r: m.x + size.width / 2,
				b: m.y - gap,
			};
		case "bottom":
			return {
				l: m.x - size.width / 2,
				t: m.y + gap,
				r: m.x + size.width / 2,
				b: m.y + gap + size.height,
			};
	}
}

/**
 * Names the busiest markers without letting two labels (or a label and
 * another marker) overlap. Markers are visited by descending count; each
 * tries the four spots around itself (right, left, above, below) and takes
 * the first that stays inside the box and clear of every label already
 * placed and every other marker. A marker with no free spot, or past `max`,
 * is returned in `hidden`, for its tooltip to name. Markers outside the box
 * are in neither list. `measure` gives a label's size in CSS pixels.
 */
export function placeLabels(
	markers: readonly LabelMarker[],
	box: Size,
	measure: (label: string) => Size,
	{
		max = Number.POSITIVE_INFINITY,
		gap = 10,
		marker = 5,
		pad = 2,
	}: PlaceLabelsOptions = {},
): LabelPlacement {
	const inside = markers.filter(
		(m) => m.x >= 0 && m.x <= box.width && m.y >= 0 && m.y <= box.height,
	);
	// Array#sort is stable, so equal counts keep the caller's order.
	const byCount = [...inside].sort((a, b) => b.count - a.count);
	const dots = new Map<string, Rect>(
		inside.map((m) => [
			m.key,
			{ l: m.x - marker, t: m.y - marker, r: m.x + marker, b: m.y + marker },
		]),
	);
	const taken: Rect[] = [];
	const placed: PlacedLabel[] = [];
	const hidden: string[] = [];
	for (const m of byCount) {
		if (placed.length >= max) {
			hidden.push(m.key);
			continue;
		}
		const size = measure(m.label);
		let fit: { side: LabelSide; rect: Rect } | undefined;
		for (const side of SIDES) {
			const rect = labelRect(side, m, size, gap);
			const padded = {
				l: rect.l - pad,
				t: rect.t - pad,
				r: rect.r + pad,
				b: rect.b + pad,
			};
			const free =
				rect.l >= 0 &&
				rect.t >= 0 &&
				rect.r <= box.width &&
				rect.b <= box.height &&
				!taken.some((o) => hits(padded, o)) &&
				![...dots].some(([key, dot]) => key !== m.key && hits(rect, dot));
			if (free) {
				fit = { side, rect };
				break;
			}
		}
		if (!fit) {
			hidden.push(m.key);
			continue;
		}
		taken.push(fit.rect);
		placed.push({
			key: m.key,
			label: m.label,
			side: fit.side,
			left: fit.rect.l,
			top: fit.rect.t,
			width: size.width,
			height: size.height,
		});
	}
	return { placed, hidden };
}

/** A small view still names this many places. */
const MIN_LABELS = 4;

/**
 * How many labels a view may name: fewer as it shows more of the world (the
 * labels would bury the land) and fewer in a small box, roughly one per
 * 15 000 square pixels, but never fewer than `MIN_LABELS` so a phone-width
 * map still names its handful of places (collisions hide what does not fit).
 * `latSpan` is the degrees of latitude the view shows.
 */
export function labelCap(latSpan: number, box: Size): number {
	const byZoom =
		latSpan >= 100 ? 8 : latSpan >= 60 ? 12 : latSpan >= 30 ? 16 : 24;
	const byArea = Math.max(
		MIN_LABELS,
		Math.floor((box.width * box.height) / 15_000),
	);
	return Math.min(byZoom, byArea);
}
