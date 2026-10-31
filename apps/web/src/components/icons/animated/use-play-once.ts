import { useReducedMotion } from "motion/react";
import { useCallback, useEffect, useRef, useState } from "react";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import { shouldPlay } from "./playback";

/**
 * A one-shot cue for an action: true from the moment `active` turns true
 * (after `delayMs`, so a dialog's own entrance is over) until it turns
 * false again. The keyboard check is made when `active` turns true, so an
 * action the reader started from the keyboard never plays, and a later
 * mouse move does not change the answer. Reduced motion never plays.
 */
export function usePlayOnce(active: boolean, delayMs = 0): boolean {
	const reducedMotion = useReducedMotion() ?? false;
	const [playing, setPlaying] = useState(false);

	useEffect(() => {
		if (!active) {
			setPlaying(false);
			return;
		}
		const play = shouldPlay("once", {
			reducedMotion,
			keyboard: lastInputWasKeyboard(),
			hoverCapable: true,
		});
		if (!play) return;
		if (delayMs <= 0) {
			setPlaying(true);
			return () => setPlaying(false);
		}
		const timer = window.setTimeout(() => setPlaying(true), delayMs);
		return () => {
			window.clearTimeout(timer);
			setPlaying(false);
		};
	}, [active, delayMs, reducedMotion]);

	return playing;
}

/**
 * A one-shot cue the caller fires from an event handler (the deploy was
 * sent): `[playing, fire]`. `playing` stays true for `durationMs`, which
 * only needs to outlast the icon's animation. The keyboard check is made
 * when `fire()` runs, so a deploy sent by key never plays.
 */
export function useTriggeredPlay(durationMs = 700) {
	const [active, setActive] = useState(false);
	const timer = useRef<number | undefined>(undefined);
	useEffect(() => () => window.clearTimeout(timer.current), []);
	const playing = usePlayOnce(active);
	const fire = useCallback(() => {
		window.clearTimeout(timer.current);
		setActive(true);
		timer.current = window.setTimeout(() => setActive(false), durationMs);
	}, [durationMs]);
	return [playing, fire] as const;
}
