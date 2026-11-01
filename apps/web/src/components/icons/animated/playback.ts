/**
 * When an animated icon may move. One policy for every icon, so a call site
 * names the kind of trigger and never re-derives the rules:
 *
 * - `hover`: a pointer rests on the control. Only on devices with a real
 *   hover (`(hover: hover) and (pointer: fine)`); touch fires phantom hovers
 *   that stick until the next tap.
 * - `state`: the icon reports a state that is still true (a mutation is
 *   running). It is a progress cue, so keyboard-started work still shows it.
 * - `once`: a one-shot flourish for an action just taken (deploy sent, a
 *   confirm opened). Skipped when the keyboard started the action: the
 *   reader is moving by key and the motion only makes them wait.
 *
 * Reduced motion beats all three: the icon renders at rest.
 */
export type PlaybackTrigger = "hover" | "state" | "once";

export interface PlaybackContext {
	reducedMotion: boolean;
	/** The last input before the action was the keyboard. */
	keyboard: boolean;
	/** The device has a fine pointer with true hover. */
	hoverCapable: boolean;
}

export function shouldPlay(
	trigger: PlaybackTrigger,
	context: PlaybackContext,
): boolean {
	if (context.reducedMotion) return false;
	switch (trigger) {
		case "hover":
			return context.hoverCapable;
		case "state":
			return true;
		case "once":
			return !context.keyboard;
	}
}

/** The motion variant an icon renders for a playing flag. */
export function iconVariant(playing: boolean): "animate" | "normal" {
	return playing ? "animate" : "normal";
}
