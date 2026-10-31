/**
 * Pure sequence logic for `ScrambleText`. Given the old text, the new text and
 * a seed, `scrambleAt` returns exactly what every slot shows at any moment, so
 * the animation is deterministic and testable without a DOM or a clock.
 */

/** Terminal-ish punctuation, a good fit for hover effects on nav links. */
export const SCRAMBLE_MONO = "!<>-_\\/[]{}=+*^?#%&@$~";
/** Lowercase hexadecimal digits. */
export const SCRAMBLE_HEX = "0123456789abcdef";
/** Four densities of block, lightest first: the dither steps a slot climbs through. */
export const SCRAMBLE_BLOCKS = ["░", "▒", "▓", "█"] as const;

export type ScrambleMode = "dither" | "blocks" | "glyphs";

/** Small seeded PRNG: the same seed yields the same sequence everywhere. */
export function mulberry32(seed: number): () => number {
	let state = seed >>> 0;
	return () => {
		state = (state + 0x6d2b79f5) >>> 0;
		let t = state;
		t = Math.imul(t ^ (t >>> 15), t | 1);
		t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
}

/** A stateless 0..1 hash of (seed, slot, frame): random access into the sequence. */
export function frameRandom(seed: number, slot: number, frame: number): number {
	let h =
		Math.imul(seed ^ 0x9e3779b9, 0x85ebca6b) ^
		Math.imul(slot + 1, 0xc2b2ae35) ^
		Math.imul(frame + 1, 0x27d4eb2f);
	h = Math.imul(h ^ (h >>> 15), 0x2c1b3c6d);
	h = Math.imul(h ^ (h >>> 12), 0x297a2d39);
	h ^= h >>> 15;
	return (h >>> 0) / 4294967296;
}

export interface ScrambleOptions {
	seed: number;
	mode: ScrambleMode;
	/** Pool for glyph flicker. Default: the letters of the text itself. */
	characters?: string | undefined;
	/** Per-character time from first placeholder to landing, ms. */
	duration: number;
	/** Gap between the starts of consecutive characters, ms. */
	stagger: number;
	/** Wait before the first character starts, ms. */
	delay: number;
	/** How often the placeholder changes, ms. */
	frameMs: number;
}

export const DEFAULT_SCRAMBLE: ScrambleOptions = {
	seed: 1,
	mode: "dither",
	duration: 520,
	stagger: 22,
	delay: 0,
	frameMs: 55,
};

export interface ScrambleSlot {
	/** The character this slot lands on. */
	to: string;
	/** The character it replaces ("" when there was none). */
	from: string;
	/** True when the slot animates at all. Spaces and unchanged characters do not. */
	changes: boolean;
	/** ms from t = 0 when the slot starts and when it lands. */
	start: number;
	end: number;
}

export interface ScramblePlan {
	slots: ScrambleSlot[];
	options: ScrambleOptions;
	/** Glyph pool in use. */
	pool: string[];
	/** ms until the last slot has landed. */
	total: number;
}

/** Distinct non-space characters of `text`, used as the default flicker pool. */
export function poolFromText(text: string): string[] {
	return [...new Set([...text].filter((c) => !/\s/.test(c)))];
}

/**
 * Lay out a scramble from `from` to `to`. Only slots whose character differs
 * animate; a fresh mount passes `from = ""` so every visible character does.
 * Animated slots are staggered in reading order, skipping the ones that stay.
 */
export function planScramble(
	from: string,
	to: string,
	options: Partial<ScrambleOptions> = {},
): ScramblePlan {
	const opts = { ...DEFAULT_SCRAMBLE, ...options };
	const before = [...from];
	const after = [...to];
	const pool = opts.characters ? [...opts.characters] : poolFromText(to + from);
	const slots: ScrambleSlot[] = [];
	let order = 0;
	let total = 0;
	after.forEach((char, i) => {
		const previous = before[i] ?? "";
		const changes = !/\s/.test(char) && previous !== char;
		const start = changes ? opts.delay + order * opts.stagger : 0;
		const end = changes ? start + opts.duration : 0;
		if (changes) {
			order += 1;
			total = Math.max(total, end);
		}
		slots.push({ to: char, from: previous, changes, start, end });
	});
	return {
		slots,
		options: opts,
		pool: pool.length > 0 ? pool : [...SCRAMBLE_MONO],
		total,
	};
}

export type ScrambleKind = "from" | "block" | "glyph" | "final";

export interface ScrambleCell {
	/** What to draw in the placeholder layer (empty for `final`). */
	char: string;
	kind: ScrambleKind;
	/** 0..1 progress through this slot's animation. */
	progress: number;
	/** Block density 1..4 for `block` cells, otherwise 0. */
	level: number;
	/** How filled a `block` cell is, 0..1 (the share of the dither it inks), otherwise 0. */
	tone: number;
}

const FINAL: ScrambleCell = {
	char: "",
	kind: "final",
	progress: 1,
	level: 0,
	tone: 0,
};

/**
 * What slot `index` shows `elapsed` ms in. Before its start it still shows the
 * old character; it climbs through block densities, then flickers through
 * glyphs, then lands. The placeholder changes every `frameMs`, never faster.
 */
export function scrambleAt(
	plan: ScramblePlan,
	index: number,
	elapsed: number,
): ScrambleCell {
	const slot = plan.slots[index];
	if (!slot) return FINAL;
	if (!slot.changes || elapsed >= slot.end) return FINAL;
	if (elapsed < slot.start)
		return {
			char: slot.from,
			kind: "from",
			progress: 0,
			level: 0,
			tone: 0,
		};

	const { mode, frameMs, seed } = plan.options;
	const progress = (elapsed - slot.start) / Math.max(1, slot.end - slot.start);
	const frame = Math.floor(elapsed / Math.max(1, frameMs));
	const random = frameRandom(seed, index, frame);
	const glyph = () => plan.pool[Math.floor(random * plan.pool.length)] ?? "#";

	if (mode === "glyphs")
		return { char: glyph(), kind: "glyph", progress, level: 0, tone: 0 };

	// Blocks thicken from sparse to solid across the first part of the slot.
	const blockSpan = mode === "blocks" ? 1 : 0.6;
	if (progress < blockSpan) {
		const level = Math.min(
			SCRAMBLE_BLOCKS.length,
			1 + Math.floor((progress / blockSpan) * SCRAMBLE_BLOCKS.length),
		);
		return {
			char: SCRAMBLE_BLOCKS[level - 1] ?? "█",
			kind: "block",
			progress,
			level,
			tone: progress / blockSpan,
		};
	}
	return { char: glyph(), kind: "glyph", progress, level: 0, tone: 0 };
}

/** Every slot's cell at `elapsed`, as plain strings; handy for snapshots and tests. */
export function scrambleFrame(plan: ScramblePlan, elapsed: number): string[] {
	return plan.slots.map((slot, i) => {
		const cell = scrambleAt(plan, i, elapsed);
		return cell.kind === "final" ? slot.to : cell.char;
	});
}

/** Steps of the block mask a block cell steps through: one per Bayer level of a 4×4 tile. */
export const BLOCK_STEPS = 16;

/** The mask step (1..16) of a block cell with fill `tone`: never empty, so a block is always visible. */
export function blockStep(tone: number): number {
	return Math.min(
		BLOCK_STEPS,
		Math.max(1, Math.ceil(Math.min(1, Math.max(0, tone)) * BLOCK_STEPS)),
	);
}

/**
 * The index of the slot the text is resolving at `elapsed`: the first
 * animated slot that has not landed yet, or -1 when every slot has landed.
 * The caret sits at the left edge of this slot.
 */
export function leadingSlot(plan: ScramblePlan, elapsed: number): number {
	return plan.slots.findIndex((slot) => slot.changes && elapsed < slot.end);
}

export interface ScrambleTimingInput {
	duration?: number | undefined;
	stagger?: number | undefined;
	delay?: number | undefined;
	/** Multiplies every timing: 2 plays twice as fast. Default 1. */
	speed?: number | undefined;
	/** Glyph changes per second while a character is unsettled. Default 20. */
	rate?: number | undefined;
}

/**
 * The timing a scramble runs with, in ms: defaults filled in (520 / 34 / 0 and
 * 20 glyphs a second), durations divided by `speed` and the glyph rate
 * multiplied by it. Non-positive speeds and rates fall back to the defaults.
 */
export function resolveScrambleTiming(
	input: ScrambleTimingInput = {},
): Pick<ScrambleOptions, "duration" | "stagger" | "delay" | "frameMs"> {
	const speed = input.speed !== undefined && input.speed > 0 ? input.speed : 1;
	const rate = input.rate !== undefined && input.rate > 0 ? input.rate : 20;
	return {
		duration: (input.duration ?? 520) / speed,
		stagger: (input.stagger ?? 34) / speed,
		delay: (input.delay ?? 0) / speed,
		frameMs: 1000 / (rate * speed),
	};
}
