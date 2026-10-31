/**
 * The motion scale for JavaScript: the same names and values as `motion.css`
 * (which is the source of truth for CSS). Use these where a duration or curve
 * is needed in `Element.animate()`, a motion-library transition or a timer;
 * use the CSS variables everywhere else. `index.test.ts` fails if the two
 * drift.
 */

/** Durations in milliseconds, named by usage. */
export const durations = {
	/** Per-item stagger offset. */
	stagger: 40,
	/** Tooltip/path delay, shake segment, large stagger. */
	micro: 80,
	/** Modal/dropdown close, text swap, tooltip appear. */
	quick: 150,
	/** Icon swap, dropdown/modal open, tabs sliding, page slide. */
	fast: 250,
	/** Panel close, toast close. */
	medium: 350,
	/** Panel open, skeleton content reveal, input clear. */
	slow: 400,
	/** Emphasis moments, badge appear, text reveal, success check. */
	verySlow: 500,
} as const;

/** CSS easing strings, named by usage. */
export const eases = {
	/** Modal/dropdown/panel open + close, page slide, resize, position change. */
	smoothOut: "cubic-bezier(0.22, 1, 0.36, 1)",
	/** Icon swap, text swap, text reveal, skeleton reveal. */
	inOut: "ease-in-out",
	/** Tooltip open / close, press, 150ms colour and opacity changes. */
	out: "ease-out",
	/** Shimmer, skeleton pulse, spinner. */
	linear: "linear",
	/** Badge pop open. */
	bounce: "cubic-bezier(0.34, 1.36, 0.64, 1)",
	/** Bouncy hover-out (avatar return). */
	bounceStrong: "cubic-bezier(0.34, 3.85, 0.64, 1)",
} as const;

/** Translate distances in px. */
export const distances = {
	micro: 4,
	small: 6,
	base: 8,
	medium: 12,
	large: 30,
} as const;

/** Scale factors for open/close pops. */
export const scales = {
	large: 0.96,
	medium: 0.97,
	small: 0.98,
	tiny: 0.99,
} as const;

/** Blur radii in px. */
export const blurs = {
	small: 2,
	medium: 3,
	large: 8,
} as const;

/** better-ui, exact: press, staged enter, exit and icon swap. */
export const exact = {
	/** A pressed button scales to 0.96 over 150ms with ease-out; a disabled one never scales. */
	press: { scale: 0.96, duration: 150, ease: eases.out },
	/**
	 * A raised key (accent, secondary, danger) stands on a 2px travel edge and
	 * sinks onto it when pressed, scaling only to 0.985: the key moves down,
	 * not inward. Ghost controls and rows keep the plain press.
	 */
	key: { travel: 2, scale: 0.985 },
	/** Staged entrance: opacity, 4px blur and a 12px rise over 300ms, chunks 100ms apart. */
	enter: { duration: 300, rise: 12, blur: 4, stagger: 100, ease: eases.out },
	/** Exit: opacity, 4px blur and a fixed -12px over 150ms. */
	exit: { duration: 150, rise: -12, blur: 4, ease: eases.out },
	/** Contextual icon swap: scale 0.25 to 1 with opacity and 4px blur over 300ms. */
	iconSwap: {
		duration: 300,
		ease: "cubic-bezier(0.2, 0, 0, 1)",
		scaleFrom: 0.25,
		blur: 4,
	},
} as const;

export type Duration = keyof typeof durations;
export type Ease = keyof typeof eases;
