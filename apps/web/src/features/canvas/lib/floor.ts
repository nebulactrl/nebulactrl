export interface FloorViewport {
	x: number;
	y: number;
	zoom: number;
}

/** World spacing of the lattice dots, px (`--canvas-lattice` in styles.css). */
export const LATTICE_PITCH = 24;
/** The lattice never gets tighter than this on screen, however far out. */
export const LATTICE_MIN_PITCH = 12;
/**
 * Bleed of the lattice layer past the canvas on every side. It is the widest
 * pitch there can be (`LATTICE_PITCH` at the 2x max zoom), so shifting the
 * layer by less than one pitch never shows its edge.
 */
export const LATTICE_BLEED = 48;

/** What the lattice layer needs to follow the viewport. */
export interface LatticeLayer {
	/** Screen pitch of the dots: the world pitch times the zoom. */
	pitch: number;
	/** Shift of the layer, 0 <= x, y < pitch. */
	x: number;
	y: number;
}

/**
 * The lattice is an oversized layer (`LATTICE_BLEED` of bleed on every
 * side) whose dots tile every `pitch` px. A pan only has to move it by the
 * viewport's offset modulo the pitch: a `transform`, which the compositor
 * applies without painting anything. Only a zoom changes the pitch (and so
 * repaints the one layer).
 *
 * The layer's tile grid starts at `-LATTICE_BLEED + shift`, so
 * `shift = (offset + LATTICE_BLEED) mod pitch` puts a tile corner at the
 * viewport's origin offset.
 */
export function latticeLayer(viewport: FloorViewport): LatticeLayer {
	const pitch =
		Math.round(
			Math.max(LATTICE_PITCH * viewport.zoom, LATTICE_MIN_PITCH) * 100,
		) / 100;
	return {
		pitch,
		x: mod(viewport.x + LATTICE_BLEED, pitch),
		y: mod(viewport.y + LATTICE_BLEED, pitch),
	};
}

function mod(value: number, by: number): number {
	return ((value % by) + by) % by;
}
