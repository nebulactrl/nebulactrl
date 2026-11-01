import {
	WORLD_LAND_HEIGHT,
	WORLD_LAND_RLE,
	WORLD_LAND_WIDTH,
} from "./world-land";

/** Equirectangular land bitmap. Row 0 is the north row, column 0 is -180°. */
export interface LandMask {
	width: number;
	height: number;
	/** One byte per cell: 255 for land, 0 for sea. Ready to upload as an R8 texture. */
	data: Uint8Array;
}

/**
 * Decodes the run-length varint format written by apps/web/scripts/build-land-mask.ts:
 * `width`, `height`, then alternating sea/land run lengths (sea first).
 */
export function decodeLandMask(base64: string): LandMask {
	const raw = atob(base64);
	const bytes = new Uint8Array(raw.length);
	for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);

	let at = 0;
	const readVarint = (): number => {
		let value = 0;
		let shift = 0;
		for (;;) {
			const byte = bytes[at++];
			if (byte === undefined) throw new Error("land mask: truncated data");
			value += (byte & 0x7f) * 2 ** shift;
			if (byte < 0x80) return value;
			shift += 7;
		}
	};

	const width = readVarint();
	const height = readVarint();
	const total = width * height;
	const data = new Uint8Array(total);
	let cursor = 0;
	let land = false;
	while (at < bytes.length) {
		const run = readVarint();
		if (cursor + run > total)
			throw new Error("land mask: run overflows bitmap");
		if (land) data.fill(255, cursor, cursor + run);
		cursor += run;
		land = !land;
	}
	if (cursor !== total) throw new Error("land mask: runs do not fill bitmap");
	return { width, height, data };
}

let cached: LandMask | undefined;

/** The bundled world mask, decoded once. */
export function getWorldLand(): LandMask {
	if (!cached) {
		cached = decodeLandMask(WORLD_LAND_RLE);
		if (
			cached.width !== WORLD_LAND_WIDTH ||
			cached.height !== WORLD_LAND_HEIGHT
		) {
			throw new Error("land mask: header does not match generated constants");
		}
	}
	return cached;
}

/** True when the cell containing `lat`, `lon` (degrees) is land. */
export function isLand(mask: LandMask, lat: number, lon: number): boolean {
	const wrapped = ((((lon + 180) % 360) + 360) % 360) / 360;
	const x = Math.min(mask.width - 1, Math.floor(wrapped * mask.width));
	const north = (90 - Math.max(-90, Math.min(90, lat))) / 180;
	const y = Math.min(mask.height - 1, Math.floor(north * mask.height));
	return mask.data[y * mask.width + x] === 255;
}

/** Columns of the bundled mask, from 180°W eastwards. */
export const LAND_COLUMNS = WORLD_LAND_WIDTH;
/** Rows of the bundled mask, from 90°N southwards. */
export const LAND_ROWS = WORLD_LAND_HEIGHT;

/** True when the bundled mask has land under `lat`, `lon` (degrees). */
export function isLandAt(lat: number, lon: number): boolean {
	return isLand(getWorldLand(), lat, lon);
}

/**
 * Land coverage around a point, 0 to 1, blended from the four nearest cells
 * of `mask` (default: the bundled world). Thresholding it at 0.5 gives smoother
 * coastlines than `isLand` when the mask is drawn larger than its resolution.
 */
export function landCoverage(
	lat: number,
	lon: number,
	mask: LandMask = getWorldLand(),
): number {
	const wrap = (value: number, size: number) => ((value % size) + size) % size;
	const x = ((lon + 180) / 360) * mask.width - 0.5;
	const y = Math.min(
		mask.height - 1,
		Math.max(0, ((90 - lat) / 180) * mask.height - 0.5),
	);
	const x0 = Math.floor(x);
	const y0 = Math.floor(y);
	const fx = x - x0;
	const fy = y - y0;
	const y1 = Math.min(mask.height - 1, y0 + 1);
	const at = (column: number, row: number) =>
		(mask.data[row * mask.width + wrap(column, mask.width)] ?? 0) / 255;
	const top = at(x0, y0) * (1 - fx) + at(x0 + 1, y0) * fx;
	const bottom = at(x0, y1) * (1 - fx) + at(x0 + 1, y1) * fx;
	return top * (1 - fy) + bottom * fy;
}

export interface LandTexture {
	width: number;
	height: number;
	data: Uint8Array;
	linear: true;
}

const textureRecords = new Map<string, Readonly<Record<string, LandTexture>>>();

/**
 * `{ [name]: texture }` for `DitherShader`'s `textures` prop. The record is
 * cached per name so the engine sees a stable identity and uploads once.
 * Row 0 is the north row, so `texture(name, vec2(lon / 360 + 0.5, lat / 180 + 0.5))`
 * samples a point directly.
 */
export function worldLandTextures(
	name: string,
): Readonly<Record<string, LandTexture>> {
	let record = textureRecords.get(name);
	if (!record) {
		record = { [name]: { ...getWorldLand(), linear: true } };
		textureRecords.set(name, record);
	}
	return record;
}
