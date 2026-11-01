/**
 * Name → face. A pure, deterministic generator: the same name always yields
 * the same bitmap, and every bitmap is left/right symmetric because only the
 * left half (plus the centre column) is ever drawn and then mirrored.
 */

/** The face grid is FACE_SIZE × FACE_SIZE cells. Odd, so there is a centre column. */
export const FACE_SIZE = 9;

const LAST = FACE_SIZE - 1;

/** Hues (oklch degrees) an avatar can take: accent first, then the cool/warm spread of the status tokens. */
export const AVATAR_HUES = [278, 215, 165, 95, 40, 335] as const;

/** 32-bit FNV-1a over the trimmed, lower-cased name: stable across runs and engines. */
export function hashName(name: string): number {
	const text = name.trim().toLowerCase();
	let hash = 0x811c9dc5;
	for (let i = 0; i < text.length; i++) {
		hash ^= text.charCodeAt(i);
		hash = Math.imul(hash, 0x01000193);
	}
	return hash >>> 0;
}

/** mulberry32: a tiny seeded generator returning floats in [0, 1). */
export function seededRandom(seed: number): () => number {
	let state = seed >>> 0;
	return () => {
		state = (state + 0x6d2b79f5) >>> 0;
		let t = state;
		t = Math.imul(t ^ (t >>> 15), t | 1);
		t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
}

export interface Face {
	/** Row-major, FACE_SIZE², 1 = a feature cell (hair, eyes, nose, mouth). */
	bits: Uint8Array;
	/** Index into AVATAR_HUES. */
	hueIndex: number;
	/** oklch hue in degrees. */
	hue: number;
}

/** Features of the face, drawn into the left half and mirrored. */
function drawFace(random: () => number): Uint8Array {
	const bits = new Uint8Array(FACE_SIZE * FACE_SIZE);
	const pick = (count: number) => Math.floor(random() * count);
	// Setting a cell sets its mirror, which is what guarantees symmetry.
	const set = (x: number, y: number) => {
		bits[y * FACE_SIZE + x] = 1;
		bits[y * FACE_SIZE + (LAST - x)] = 1;
	};

	// Hair: rows 0–1.
	switch (pick(5)) {
		case 1:
			for (let x = 1; x <= 4; x++) set(x, 0);
			break;
		case 2:
			for (let x = 2; x <= 4; x++) set(x, 0);
			for (let x = 1; x <= 4; x++) set(x, 1);
			break;
		case 3:
			set(4, 0);
			set(3, 1);
			set(4, 1);
			break;
		case 4:
			for (let x = 0; x <= 4; x++) set(x, 0);
			set(0, 1);
			set(0, 2);
			break;
		default:
			break;
	}

	// Eyes: row 3 (and 4 for the tall style).
	switch (pick(4)) {
		case 1:
			set(1, 3);
			set(2, 3);
			break;
		case 2:
			set(2, 3);
			set(2, 4);
			break;
		case 3:
			set(1, 4);
			set(2, 3);
			break;
		default:
			set(2, 3);
			break;
	}

	// Nose: optional single cell on the centre column.
	if (random() < 0.55) bits[5 * FACE_SIZE + 4] = 1;

	// Ears: optional.
	if (random() < 0.4) set(0, 4);

	// Mouth: rows 6–7.
	switch (pick(4)) {
		case 1:
			for (let x = 3; x <= 4; x++) set(x, 7);
			break;
		case 2:
			for (let x = 2; x <= 4; x++) set(x, 7);
			set(1, 6);
			break;
		case 3:
			bits[7 * FACE_SIZE + 4] = 1;
			break;
		default:
			for (let x = 2; x <= 4; x++) set(x, 6);
			break;
	}
	return bits;
}

/** The whole face for a name: feature bitmap plus the hue it is tinted with. */
export function faceFor(name: string): Face {
	const hash = hashName(name);
	const random = seededRandom(hash);
	const hueIndex = hash % AVATAR_HUES.length;
	return {
		bits: drawFace(random),
		hueIndex,
		hue: AVATAR_HUES[hueIndex] ?? 278,
	};
}

/** Just the feature bitmap (FACE_SIZE² cells, row-major). */
export function faceBitmap(name: string): Uint8Array {
	return faceFor(name).bits;
}

export interface FaceRun {
	x: number;
	y: number;
	w: number;
}

/** Horizontal runs of feature cells: fewer SVG rects than one per cell. */
export function faceRuns(bits: Uint8Array): FaceRun[] {
	const runs: FaceRun[] = [];
	for (let y = 0; y < FACE_SIZE; y++) {
		let x = 0;
		while (x < FACE_SIZE) {
			if (bits[y * FACE_SIZE + x] !== 1) {
				x++;
				continue;
			}
			const start = x;
			while (x < FACE_SIZE && bits[y * FACE_SIZE + x] === 1) x++;
			runs.push({ x: start, y, w: x - start });
		}
	}
	return runs;
}

/** The colours of one avatar hue: skin, its dithered light and shade, and the ink of the features. */
export function avatarColors(hue: number) {
	return {
		base: `oklch(0.5 0.12 ${hue})`,
		light: `oklch(0.8 0.12 ${hue})`,
		shade: `oklch(0.28 0.09 ${hue})`,
		ink: `oklch(0.15 0.03 ${hue})`,
	};
}

/**
 * The same four colours for any CSS colour (a palette name's `var()`, a custom
 * property, a hex): skin and ink are mixes of it with black, the dither light
 * is a mix with white. `color-mix` resolves in the browser, so theme tokens work.
 */
export function avatarColorsFrom(css: string) {
	const mix = (other: string, pct: number) =>
		`color-mix(in oklab, ${css} ${pct}%, ${other})`;
	return {
		base: mix("black", 68),
		light: mix("white", 55),
		shade: mix("black", 36),
		ink: mix("black", 14),
	};
}

/** The face for a name as a feature bitmap (`FACE_SIZE`² cells, row-major): the same face elsewhere, such as an Open Graph image. */
export function avatarFace(name: string): Uint8Array {
	return faceBitmap(name);
}

/** The skin colour for a name (a CSS `oklch()`): the hue the avatar picks without a `color`. */
export function avatarColor(name: string): string {
	return avatarColors(faceFor(name).hue).base;
}
