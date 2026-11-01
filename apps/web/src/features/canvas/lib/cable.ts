import type { Point } from "#/api/models";

/** Which side of a card a jack sits on; the cable leaves it in that direction. */
export type CableSide = "left" | "right" | "top" | "bottom";

export interface CableEnd extends Point {
	side: CableSide;
}

/** The dash of a cable drawn by hand: no variable references it. */
export const MANUAL_DASH = "7 6";
/** A staged reference is stitched: shorter and tighter than a hand-drawn dash. */
export const STAGED_DASH = "3 5";

/** A jack's radius: React Flow reports a handle's outer edge, a plug sits on its centre. */
export const JACK_RADIUS = 8;

const DIRECTION: Record<CableSide, Point> = {
	left: { x: -1, y: 0 },
	right: { x: 1, y: 0 },
	top: { x: 0, y: -1 },
	bottom: { x: 0, y: 1 },
};

function clamp(value: number, min: number, max: number): number {
	return Math.min(max, Math.max(min, value));
}

/**
 * React Flow reports a handle's outer edge; the cable ends on the jack's
 * centre, one radius toward the card.
 */
export function jackCentre(end: CableEnd, radius = JACK_RADIUS): CableEnd {
	const direction = DIRECTION[end.side];
	return {
		x: end.x - direction.x * radius,
		y: end.y - direction.y * radius,
		side: end.side,
	};
}

/** Which of a card's jacks hold a plug: left and right for links, top for the Internet. */
export interface Plugs {
	left: boolean;
	right: boolean;
	top: boolean;
}

export const NO_PLUGS: Plugs = { left: false, right: false, top: false };

/** A card's rectangle in canvas coordinates. */
export interface Box {
	x: number;
	y: number;
	w: number;
	h: number;
}

type JackSide = "left" | "right";

export interface Sides {
	source: JackSide;
	target: JackSide;
}

/** Free space between two cards that lets a cable run straight across it. */
const SIDE_CLEARANCE = 24;

/**
 * Which jacks a cable joins, from where its two cards stand. A target to the
 * right takes source right → target left; a target to the left takes source
 * left → target right, so the cable never doubles back behind a card. Cards
 * stacked in the same column have no clear side between them: both use the
 * side whose jacks are closer, and the cable bows out to that side.
 */
export function chooseSides(
	source: Box,
	target: Box,
	clearance = SIDE_CLEARANCE,
): Sides {
	if (target.x >= source.x + source.w + clearance) {
		return { source: "right", target: "left" };
	}
	if (target.x + target.w + clearance <= source.x) {
		return { source: "left", target: "right" };
	}
	const right = Math.abs(target.x + target.w - (source.x + source.w));
	const left = Math.abs(target.x - source.x);
	const side = left < right ? "left" : "right";
	return { source: side, target: side };
}

/**
 * The jack the Internet entry node (above its cards) plugs into: a top-centre
 * jack that only Internet cables use, so the cable drops straight in.
 */
export const INTERNET_JACK = "top";

export interface Cable {
	/** The SVG path: one cubic. */
	d: string;
	/** Where the cable is halfway, for its label. */
	mid: Point;
	/** Straight-line distance between the ends. */
	distance: number;
}

/**
 * A patch cable between two jacks: one cubic that leaves each jack along its
 * side and hangs under its own weight. The sag grows with the distance, so
 * a short hop hangs a little and a long run droops; only horizontal jacks
 * sag (a cable leaving a bottom jack already falls).
 */
export function cable(from: CableEnd, to: CableEnd): Cable {
	const distance = Math.hypot(to.x - from.x, to.y - from.y);
	const reach = clamp(distance * 0.24, 30, 120);
	// Only a cable running from one side of the canvas to the other hangs;
	// one leaving and entering on the same side bows out instead.
	const horizontal =
		(from.side === "left" && to.side === "right") ||
		(from.side === "right" && to.side === "left");
	const sag = horizontal ? clamp(distance * 0.3, 18, 96) : 0;
	const a = DIRECTION[from.side];
	const b = DIRECTION[to.side];
	const c1 = { x: from.x + a.x * reach, y: from.y + a.y * reach + sag };
	const c2 = { x: to.x + b.x * reach, y: to.y + b.y * reach + sag };
	const round = (n: number) => Math.round(n * 10) / 10;
	return {
		d: `M ${round(from.x)} ${round(from.y)} C ${round(c1.x)} ${round(c1.y)}, ${round(c2.x)} ${round(c2.y)}, ${round(to.x)} ${round(to.y)}`,
		mid: {
			x: (from.x + 3 * c1.x + 3 * c2.x + to.x) / 8,
			y: (from.y + 3 * c1.y + 3 * c2.y + to.y) / 8,
		},
		distance,
	};
}
