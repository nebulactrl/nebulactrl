import { durations, eases } from "@nebulactrl/tokens";
import { cubicBezier } from "motion/react";
import { bezier } from "#/lib/ease";

/**
 * How the canvas camera moves to keep the selected card clear of the
 * inspector. The pan is the panel's own motion seen from the canvas, so it
 * runs as long as the panel's open and on the same curve; it is a straight
 * line (`interpolate: "linear"`), not React Flow's default zoom-out arc, so
 * the card never swoops while the panel slides.
 */
export const PAN_EASE = cubicBezier(...bezier(eases.smoothOut));

/** How long the pan takes, in ms: the same as the panel's open. */
export const PAN_MS = durations.fast;

/** How long a width drag has to rest before the camera follows it, in ms. */
export const RESIZE_SETTLE_MS = 160;

/**
 * The pan's duration in ms. None under reduced motion, and none when the
 * keyboard opened the panel (it has no slide then either).
 */
export function panDuration(reduced: boolean, keyboard: boolean): number {
	return reduced || keyboard ? 0 : PAN_MS;
}

/** When the camera reacts to the panel's reserved width changing. */
export type RevealTiming =
	| { kind: "next-frames" }
	| { kind: "after-rest"; ms: number };

/**
 * The panel opened (it reserved nothing before) or another service took it
 * over (the width is the same): the camera starts two animation frames on,
 * after the panel's first frame has been painted. A width that changed
 * while the panel stayed open is a drag, a tab class change or a window
 * resize: the camera waits for it to rest rather than chase every pointer
 * move.
 */
export function revealTiming(previousPx: number, px: number): RevealTiming {
	if (previousPx === 0 || previousPx === px) return { kind: "next-frames" };
	return { kind: "after-rest", ms: RESIZE_SETTLE_MS };
}
