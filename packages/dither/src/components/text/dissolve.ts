/**
 * Pure scheduling for `DissolveText`: how text splits into units, which unit
 * arrives when, and which Bayer level each unit shows at a moment in time.
 */

import { mulberry32 } from "./scramble";

export type DissolveBy = "char" | "word" | "line";

export type DissolveDirection = "start" | "end" | "center" | "edges" | "random";

/** The mask has 16 steps: 0 = nothing inked, 16 = every cell inked. */
export const DISSOLVE_LEVELS = 16;

export interface DissolveUnit {
	/** What is shown for this unit. */
	text: string;
	/** True for whitespace that only separates units; it is not masked. */
	space: boolean;
	/** Index among animated units, or -1 for spaces. */
	index: number;
}

/**
 * Split text into units. `char` and `word` keep whitespace as separate
 * non-animated units so wrapping works; `line` splits on `\n` only.
 */
export function splitUnits(text: string, by: DissolveBy): DissolveUnit[] {
	const units: DissolveUnit[] = [];
	let index = 0;
	const push = (value: string, space: boolean) => {
		units.push({ text: value, space, index: space ? -1 : index });
		if (!space) index += 1;
	};
	if (by === "line") {
		const lines = text.split("\n");
		lines.forEach((line, i) => {
			push(line, false);
			if (i < lines.length - 1) push("\n", true);
		});
		return units;
	}
	if (by === "word") {
		for (const token of text.split(/(\s+)/)) {
			if (token === "") continue;
			push(token, /^\s+$/.test(token));
		}
		return units;
	}
	for (const char of text) push(char, /\s/.test(char));
	return units;
}

/**
 * The arrival rank (0 = first) of each of `count` animated units. Ranks are a
 * permutation of 0..count-1. `random` is a seeded shuffle, the same every visit.
 */
export function arrivalRanks(
	count: number,
	direction: DissolveDirection,
	seed = 1,
): number[] {
	const ids = Array.from({ length: count }, (_, i) => i);
	const ranks = new Array<number>(count).fill(0);
	const middle = (count - 1) / 2;
	let order: number[];
	switch (direction) {
		case "start":
			order = ids;
			break;
		case "end":
			order = [...ids].reverse();
			break;
		case "center":
			order = [...ids].sort(
				(a, b) => Math.abs(a - middle) - Math.abs(b - middle) || a - b,
			);
			break;
		case "edges":
			order = [...ids].sort(
				(a, b) => Math.abs(b - middle) - Math.abs(a - middle) || a - b,
			);
			break;
		case "random": {
			const random = mulberry32(seed);
			order = [...ids];
			for (let i = order.length - 1; i > 0; i -= 1) {
				const j = Math.floor(random() * (i + 1));
				const swap = order[i] as number;
				order[i] = order[j] as number;
				order[j] = swap;
			}
			break;
		}
	}
	order.forEach((unit, rank) => {
		ranks[unit] = rank;
	});
	return ranks;
}

export interface DissolveTiming {
	/** Time before the first unit starts, ms. */
	delay: number;
	/** Gap between the starts of consecutive units, ms. */
	stagger: number;
	/** How long one unit takes from empty to its end level, ms. */
	duration: number;
}

/** Start time of the unit with arrival rank `rank`, ms from t = 0. */
export function unitStart(rank: number, timing: DissolveTiming): number {
	return timing.delay + rank * timing.stagger;
}

/** Time when the last of `count` units has finished, ms. */
export function totalDuration(count: number, timing: DissolveTiming): number {
	return count === 0 ? 0 : unitStart(count - 1, timing) + timing.duration;
}

/**
 * Mask level (0..16) of a unit `elapsed` ms in, rising to `endLevel`
 * (16 = solid). Dissolving out is `endLevel - unitLevel(…)`-style: pass the
 * mirrored elapsed or use `from`/`to`.
 */
export function unitLevel(
	elapsed: number,
	rank: number,
	timing: DissolveTiming,
	endLevel = DISSOLVE_LEVELS,
): number {
	const local =
		(elapsed - unitStart(rank, timing)) / Math.max(1, timing.duration);
	const progress = Math.min(1, Math.max(0, local));
	return Math.round(progress * endLevel);
}

/**
 * The WAAPI keyframe levels for one unit: 0 … end, one entry per distinct
 * level, so each keyframe is one Bayer step. Reversed for dissolving out.
 */
export function levelSequence(endLevel: number, reverse = false): number[] {
	const end = Math.min(DISSOLVE_LEVELS, Math.max(0, Math.round(endLevel)));
	const levels = Array.from({ length: end + 1 }, (_, i) => i);
	return reverse ? levels.reverse() : levels;
}

/** Gap between consecutive units when `stagger` is not set: 28 ms a character, 90 ms a word or line. */
export function defaultStagger(by: DissolveBy): number {
	return by === "char" ? 28 : 90;
}

export interface DissolveTimingInput {
	by: DissolveBy;
	duration?: number | undefined;
	stagger?: number | undefined;
	delay?: number | undefined;
	/** Multiplies every timing: 2 plays twice as fast. Default 1. */
	speed?: number | undefined;
}

/**
 * The timing a dissolve runs with, in ms: defaults filled in, then every value
 * divided by `speed` (non-positive speeds count as 1).
 */
export function resolveDissolveTiming(
	input: DissolveTimingInput,
): DissolveTiming {
	const speed = input.speed !== undefined && input.speed > 0 ? input.speed : 1;
	return {
		duration: (input.duration ?? 600) / speed,
		stagger: (input.stagger ?? defaultStagger(input.by)) / speed,
		delay: (input.delay ?? 0) / speed,
	};
}
