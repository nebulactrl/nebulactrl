import {
	type LatLon,
	latLonToUV,
	type MapBounds,
} from "@nebulactrl/dither/backgrounds/geo";
import type { Size } from "./labels";

/**
 * The latitudes a map may show when it widens to fill a tall box: from just
 * above the North Cape to the tip of South America, so a world map never
 * grows polar caps of grey Antarctica.
 */
export const LAT_LIMITS = { north: 84, south: -62 } as const;

/** A map drawn into a box: which part of the world, on what grid, and where. */
export interface Projection {
	/** What the grid shows: the requested bounds, widened to the box's shape. */
	bounds: MapBounds;
	/** Dots across and down. */
	columns: number;
	rows: number;
	/** CSS pixels from one dot to the next (square cells, whole pixels). */
	pitch: number;
	/** The grid's rectangle in the box, in CSS pixels, centred. */
	x: number;
	y: number;
	width: number;
	height: number;
}

/**
 * Fits `bounds` into a box, filling it as far as the world allows: the
 * requested region is always fully inside, and the side that has room to
 * spare shows more of the surrounding land (up to `LAT_LIMITS` and one full
 * turn of longitude) instead of leaving the box empty. What cannot be filled
 * is left as margin and the grid is centred. The grid is a whole number of
 * `pitch`-pixel cells, so its dots stay crisp at any box size; widening the
 * box adds dots, it never stretches them. Null while the box is smaller
 * than a cell.
 */
export function fitProjection(
	bounds: MapBounds,
	box: Size,
	pitch: number,
): Projection | null {
	if (box.width <= 0 || box.height <= 0 || pitch <= 0) return null;
	const boxAspect = box.width / box.height;
	let lonSpan = bounds.east - bounds.west;
	let latSpan = bounds.north - bounds.south;
	if (lonSpan / latSpan < boxAspect) {
		lonSpan = Math.min(latSpan * boxAspect, 360);
	} else {
		latSpan = Math.min(
			lonSpan / boxAspect,
			LAT_LIMITS.north - LAT_LIMITS.south,
		);
	}
	const aspect = lonSpan / latSpan;
	const columns = Math.floor(Math.min(box.width, box.height * aspect) / pitch);
	const rows = Math.floor(columns / aspect);
	if (columns < 1 || rows < 1) return null;
	// Whole rows: the span of one axis absorbs the rounding. Longitude does, so
	// the request is never cropped, except at a full turn, where it would wrap
	// past itself; there latitude gives up the sliver (the request still fits).
	const fitLat = (lonSpan * rows) / columns;
	if (lonSpan >= 360 && fitLat >= bounds.north - bounds.south) latSpan = fitLat;
	else lonSpan = (latSpan * columns) / rows;

	const midLat = (bounds.north + bounds.south) / 2;
	const midLon = (bounds.east + bounds.west) / 2;
	// Slide the widened band back inside the limits; it still holds the request.
	const south = Math.max(
		Math.min(midLat - latSpan / 2, LAT_LIMITS.north - latSpan),
		LAT_LIMITS.south,
	);
	const width = columns * pitch;
	const height = rows * pitch;
	return {
		bounds: {
			north: south + latSpan,
			south,
			west: midLon - lonSpan / 2,
			east: midLon + lonSpan / 2,
		},
		columns,
		rows,
		pitch,
		x: Math.floor((box.width - width) / 2),
		y: Math.floor((box.height - height) / 2),
		width,
		height,
	};
}

/**
 * The box shape (width / height) nearest `preferred` that `bounds` fills
 * with no empty band: a box wider than `360 / latSpan` runs out of longitude
 * (bands left and right), one narrower than `lonSpan / LAT_LIMITS span` runs
 * out of latitude (bands above and below), as `fitProjection` widens a side
 * only that far.
 */
export function fillAspect(preferred: number, bounds: MapBounds): number {
	const lonSpan = bounds.east - bounds.west;
	const latSpan = bounds.north - bounds.south;
	const min = lonSpan / (LAT_LIMITS.north - LAT_LIMITS.south);
	const max = 360 / latSpan;
	return Math.min(Math.max(preferred, min), max);
}

/** Whether two projections show the same part of the world. */
export function sameBounds(a: MapBounds, b: MapBounds): boolean {
	return (
		a.north === b.north &&
		a.south === b.south &&
		a.west === b.west &&
		a.east === b.east
	);
}

/** Where a place sits in the projection's grid, in CSS pixels from its top left corner. */
export function projectPoint(
	projection: Pick<Projection, "bounds" | "width" | "height">,
	at: LatLon,
): { x: number; y: number } {
	const [u, v] = latLonToUV(at[0], at[1], projection.bounds);
	return { x: u * projection.width, y: (1 - v) * projection.height };
}

/** Whether a place falls inside the projection's grid. */
export function inProjection(
	projection: Pick<Projection, "bounds" | "width" | "height">,
	at: LatLon,
): boolean {
	const { x, y } = projectPoint(projection, at);
	return x >= 0 && x <= projection.width && y >= 0 && y <= projection.height;
}

/** A CSS `translate(x, y) scale(sx, sy)` about the element's top left corner. */
export interface ViewTransform {
	x: number;
	y: number;
	scaleX: number;
	scaleY: number;
}

/**
 * The transform that makes the grid of `to` look like the grid of `from`:
 * every place lands where `from` draws it. Played from this to nothing, it
 * pans and zooms `to` into place; played from nothing to this, it pans and
 * zooms `from` away to `to`'s framing. Both run on the same curve, so a
 * place on either layer travels along one straight line. Longitudes wrap, so
 * the pan takes the short way round.
 */
export function viewTransform(
	from: Pick<Projection, "bounds" | "x" | "y" | "width" | "height">,
	to: Pick<Projection, "bounds" | "x" | "y" | "width" | "height">,
): ViewTransform {
	const fromLon = from.bounds.east - from.bounds.west;
	const toLon = to.bounds.east - to.bounds.west;
	const fromLat = from.bounds.north - from.bounds.south;
	const toLat = to.bounds.north - to.bounds.south;
	const sxFrom = from.width / fromLon;
	const sxTo = to.width / toLon;
	const syFrom = from.height / fromLat;
	const syTo = to.height / toLat;
	const midFrom = (from.bounds.east + from.bounds.west) / 2;
	const midTo = (to.bounds.east + to.bounds.west) / 2;
	const turn = ((((midTo - midFrom) % 360) + 540) % 360) - 180;
	const westTo = midFrom + turn - toLon / 2;
	return {
		x: from.x + sxFrom * (westTo - from.bounds.west) - to.x,
		y: from.y + syFrom * (from.bounds.north - to.bounds.north) - to.y,
		scaleX: sxFrom / sxTo,
		scaleY: syFrom / syTo,
	};
}

/** The CSS value of a view transform. */
export function transformCss(t: ViewTransform): string {
	return `translate(${t.x}px, ${t.y}px) scale(${t.scaleX}, ${t.scaleY})`;
}
