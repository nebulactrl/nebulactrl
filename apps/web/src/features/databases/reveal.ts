/**
 * A revealed connection string lives in component state only, never in the
 * query cache, and goes away at the server's `expiresAt` (or after the
 * client's own cap, whichever comes first). This is the pure part: the
 * clock is passed in, so the countdown and the clearing are testable.
 */

/** The most a reveal stays on screen, whatever `expiresAt` says. */
export const REVEAL_MAX_MS = 60_000;

export interface Reveal {
	key: string;
	value: string;
	/** Epoch ms at which the value must be gone. */
	clearAt: number;
}

/** The state a reveal starts in: the value and when to drop it. */
export function startReveal(
	revealed: { key: string; value: string; expiresAt: string },
	now: number,
): Reveal {
	const expires = Date.parse(revealed.expiresAt);
	const cap = now + REVEAL_MAX_MS;
	return {
		key: revealed.key,
		value: revealed.value,
		clearAt: Number.isNaN(expires) ? cap : Math.min(expires, cap),
	};
}

/** Whole seconds left, never negative. */
export function secondsLeft(reveal: Reveal, now: number): number {
	return Math.max(0, Math.ceil((reveal.clearAt - now) / 1000));
}
