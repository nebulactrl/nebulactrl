/**
 * Motion of the dither globe, kept apart from React so it can be tested.
 *
 * The view is an offset from the `start` longitude and `tilt` props. While the
 * pointer holds the globe nothing moves on its own. On release the globe
 * coasts on the speed of the last drag, which decays; the idle spin fades back
 * in behind it, so the hand-off never jerks.
 */

export interface GlobeView {
	/** Degrees added to the `start` longitude. Decreases as the globe spins. */
	lon: number;
	/** Degrees added to the `tilt` prop. */
	tilt: number;
	/** Coasting speed in longitude, degrees per second. */
	vLon: number;
	/** Coasting speed in tilt, degrees per second. */
	vTilt: number;
	/** Share of the idle spin currently applied, 0 to 1. */
	gain: number;
}

export const REST_VIEW: GlobeView = {
	lon: 0,
	tilt: 0,
	vLon: 0,
	vTilt: 0,
	gain: 1,
};

/** Seconds for a coast to lose most of its speed (time constant of the decay). */
export const COAST_TAU = 0.55;
/** Seconds for the idle spin to come back after a drag (time constant). */
export const SPIN_RETURN_TAU = 0.9;
/** Coasting slower than this, in degrees per second, stops. */
export const COAST_STOP = 0.4;
/** Fastest a release may coast, in degrees per second. */
export const COAST_MAX = 240;

export const TILT_RANGE = { min: -60, max: 80 } as const;

const clamp = (value: number, min: number, max: number) =>
	Math.min(max, Math.max(min, value));

/** The tilt in degrees for a `tilt` prop and an offset, kept inside the range. */
export function tiltOf(tilt: number, offset: number): number {
	return clamp(tilt + offset, TILT_RANGE.min, TILT_RANGE.max);
}

/** An offset that keeps `tilt + offset` inside the range. */
export function clampTiltOffset(tilt: number, offset: number): number {
	return clamp(offset, TILT_RANGE.min - tilt, TILT_RANGE.max - tilt);
}

/** Smooths a drag speed: the last sample counts 60%, the history 40%. */
export function smoothSpeed(
	previous: number,
	dDegrees: number,
	dSeconds: number,
): number {
	if (dSeconds <= 0) return previous;
	const instant = clamp(dDegrees / dSeconds, -COAST_MAX, COAST_MAX);
	return previous * 0.4 + instant * 0.6;
}

export interface StepOptions {
	/** Idle spin in degrees per second; negative spins the other way. */
	spin: number;
	/** The pointer holds the globe: no spin, no coast. */
	held: boolean;
	/** `false` (reduced motion) drops any coast speed instead of playing it out. */
	coast: boolean;
	/** The `tilt` prop, so a coast cannot push the tilt out of range. */
	tilt: number;
}

/** Advances the view by `dt` seconds. */
export function stepGlobe(
	view: GlobeView,
	dt: number,
	{ spin, held, coast, tilt }: StepOptions,
): GlobeView {
	if (dt <= 0) return view;
	if (held) {
		// Idle spin restarts from nothing once the hand lets go.
		return view.gain === 0 ? view : { ...view, gain: 0 };
	}
	const decay = Math.exp(-dt / COAST_TAU);
	const startLon = coast ? view.vLon : 0;
	const startTilt = coast ? view.vTilt : 0;
	let vLon = startLon * decay;
	let vTilt = startTilt * decay;
	if (Math.abs(vLon) < COAST_STOP) vLon = 0;
	if (Math.abs(vTilt) < COAST_STOP) vTilt = 0;
	const gain =
		view.gain + (1 - view.gain) * (1 - Math.exp(-dt / SPIN_RETURN_TAU));
	// Integrate the speed over the step: average of start and end speed.
	const lon = view.lon + (startLon + vLon) * 0.5 * dt - spin * gain * dt;
	const rawTilt = view.tilt + (startTilt + vTilt) * 0.5 * dt;
	const next = clampTiltOffset(tilt, rawTilt);
	return {
		lon,
		tilt: next,
		vLon,
		// A coast that hits the end of the range stops there.
		vTilt: next === rawTilt ? vTilt : 0,
		gain,
	};
}
