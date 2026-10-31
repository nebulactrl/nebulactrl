/**
 * The dither clock: one shared rAF scheduler for every animated dither.
 *
 * - A frame loop runs only while at least one subscriber is active, so an idle
 *   page costs nothing.
 * - A subscriber is active only while it is enabled, on screen, in a visible
 *   tab and motion is not reduced (`isFrameActive`).
 * - Time only advances while running; one long frame counts as at most 0.1 s,
 *   so motion never jumps after a tab switch.
 * - The scheduler core (`createScheduler`) takes its clock and rAF as
 *   arguments so the gating and fps logic are unit tested with a fake clock.
 */
import {
	type MutableRefObject,
	type RefObject,
	useEffect,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";

/** Longest slice of time a single frame may add, in seconds. */
export const MAX_FRAME_DELTA = 0.1;
export const DEFAULT_FPS = 24;
export const MAX_FPS = 30;
/** rAF jitter allowance when comparing elapsed time with the frame interval, in ms. */
const FRAME_SLACK_MS = 1.5;

export function clampFps(fps: number | undefined): number {
	const value = fps === undefined || !Number.isFinite(fps) ? DEFAULT_FPS : fps;
	return Math.min(MAX_FPS, Math.max(1, value));
}

export interface FrameGate {
	enabled: boolean;
	inView: boolean;
	documentVisible: boolean;
	reducedMotion: boolean;
}

/** True when a subscriber should be ticking. */
export function isFrameActive(gate: FrameGate): boolean {
	return (
		gate.enabled && gate.inView && gate.documentVisible && !gate.reducedMotion
	);
}

/** Why a still frame is drawn instead of animating. */
export type StillReason = "reduced" | "disabled";

export type DitherFrameState = "running" | "paused" | "static";

/** `static` when motion is off, `paused` when hidden or off-screen, else `running`. */
export function frameState(gate: FrameGate): DitherFrameState {
	if (!gate.enabled || gate.reducedMotion) return "static";
	if (!gate.inView || !gate.documentVisible) return "paused";
	return "running";
}

export type FrameCallback = (
	time: number,
	delta: number,
	still?: StillReason,
) => void;

export interface FrameEntry {
	fps: number;
	active: boolean;
	/** Seconds the entry has been running. Only advances while active. */
	time: number;
	/** ms timestamp of the last tick; NaN until the first tick after activation. */
	last: number;
	onFrame: FrameCallback;
}

export interface SchedulerEnv {
	request(callback: (now: number) => void): number;
	cancel(handle: number): void;
}

export interface Scheduler {
	add(entry: FrameEntry): void;
	remove(entry: FrameEntry): void;
	setActive(entry: FrameEntry, active: boolean): void;
	/** True while a frame is queued. */
	isRunning(): boolean;
}

export function createFrameEntry(
	fps: number,
	onFrame: FrameCallback,
): FrameEntry {
	return {
		fps: clampFps(fps),
		active: false,
		time: 0,
		last: Number.NaN,
		onFrame,
	};
}

/** Advance one entry at timestamp `now` (ms). Exported for tests. */
export function stepEntry(entry: FrameEntry, now: number): boolean {
	if (Number.isNaN(entry.last)) {
		entry.last = now;
		entry.onFrame(entry.time, 0);
		return true;
	}
	const interval = 1000 / entry.fps;
	const elapsed = now - entry.last;
	if (elapsed + FRAME_SLACK_MS < interval) return false;
	const delta = Math.min(elapsed / 1000, MAX_FRAME_DELTA);
	entry.time += delta;
	// Keep the average rate steady: advance the gate by exactly one interval
	// (carrying the remainder, including any slack that let this frame through),
	// unless we fell a whole frame behind.
	entry.last = elapsed < interval * 2 ? entry.last + interval : now;
	entry.onFrame(entry.time, delta);
	return true;
}

export function createScheduler(env: SchedulerEnv): Scheduler {
	const entries = new Set<FrameEntry>();
	let handle = 0;

	const hasActive = () => {
		for (const entry of entries) if (entry.active) return true;
		return false;
	};

	const loop = (now: number) => {
		handle = 0;
		for (const entry of entries) {
			if (entry.active) stepEntry(entry, now);
		}
		if (handle === 0 && hasActive()) handle = env.request(loop);
	};

	const wake = () => {
		if (handle === 0 && hasActive()) handle = env.request(loop);
	};

	const sleepIfIdle = () => {
		if (handle !== 0 && !hasActive()) {
			env.cancel(handle);
			handle = 0;
		}
	};

	return {
		add(entry) {
			entries.add(entry);
			wake();
		},
		remove(entry) {
			entries.delete(entry);
			sleepIfIdle();
		},
		setActive(entry, active) {
			if (entry.active === active) return;
			entry.active = active;
			if (active) wake();
			else {
				entry.last = Number.NaN;
				sleepIfIdle();
			}
		},
		isRunning: () => handle !== 0,
	};
}

let shared: Scheduler | null = null;
function scheduler(): Scheduler {
	shared ??= createScheduler({
		request: (callback) => requestAnimationFrame(callback),
		cancel: (handle) => cancelAnimationFrame(handle),
	});
	return shared;
}

// -- reduced motion ---------------------------------------------------------

const REDUCED_QUERY = "(prefers-reduced-motion: reduce)";
let reducedQuery: MediaQueryList | null = null;
function reducedMql(): MediaQueryList | null {
	if (typeof matchMedia !== "function") return null;
	reducedQuery ??= matchMedia(REDUCED_QUERY);
	return reducedQuery;
}

export function prefersReducedMotion(): boolean {
	return reducedMql()?.matches ?? false;
}

function subscribeReduced(onChange: () => void): () => void {
	const mql = reducedMql();
	mql?.addEventListener("change", onChange);
	return () => mql?.removeEventListener("change", onChange);
}

// -- document visibility ----------------------------------------------------

function documentVisible(): boolean {
	return (
		typeof document === "undefined" || document.visibilityState !== "hidden"
	);
}

function subscribeVisibility(onChange: () => void): () => void {
	document.addEventListener("visibilitychange", onChange);
	return () => document.removeEventListener("visibilitychange", onChange);
}

// -- intersection -----------------------------------------------------------

interface SharedObserver {
	io: IntersectionObserver;
	callbacks: Map<Element, (visible: boolean) => void>;
}
const observers = new Map<string, SharedObserver>();

/**
 * Observe whether `el` is within `rootMargin` of the viewport, sharing one
 * IntersectionObserver per margin. Returns the unsubscribe function. Without
 * IntersectionObserver the element counts as visible.
 */
export function observeVisibility(
	el: Element,
	rootMargin: string,
	callback: (visible: boolean) => void,
): () => void {
	if (typeof IntersectionObserver === "undefined") {
		callback(true);
		return () => {};
	}
	let shared = observers.get(rootMargin);
	if (!shared) {
		const callbacks = new Map<Element, (visible: boolean) => void>();
		const io = new IntersectionObserver(
			(records) => {
				for (const record of records) {
					callbacks.get(record.target)?.(record.isIntersecting);
				}
			},
			{ rootMargin },
		);
		shared = { io, callbacks };
		observers.set(rootMargin, shared);
	}
	shared.callbacks.set(el, callback);
	shared.io.observe(el);
	const entry = shared;
	return () => {
		entry.io.unobserve(el);
		entry.callbacks.delete(el);
	};
}

/** Margin around the viewport inside which a dither keeps animating. */
export const NEAR_MARGIN = "64px 0px";

export interface DitherFrameOptions {
	/** Frame rate cap, default 24, max 30. */
	fps?: number | undefined;
	/** `false` holds the current frame. */
	enabled?: boolean | undefined;
	/** Element whose visibility gates the loop. Omit to run whenever the tab is visible. */
	target?: RefObject<Element | null> | undefined;
	/** The moment drawn when motion is off or reduced, seconds. */
	stillTime?: number | undefined;
}

/**
 * Call `onFrame(time, delta)` once per frame on the shared scheduler while the
 * target is on screen. When motion is off (reduced, or `enabled: false`) it
 * calls once with delta 0 and `still` set: `time` is `stillTime` under
 * reduced motion, otherwise the time already reached (or `stillTime` if the
 * clock never ran). The callback is kept in a ref and may change every render.
 */
export function useDitherFrame(
	onFrame: FrameCallback,
	options: DitherFrameOptions = {},
): DitherFrameState {
	const { fps, enabled = true, target, stillTime = 0 } = options;
	const callback = useRef(onFrame);
	callback.current = onFrame;

	const reduced = useSyncExternalStore(
		subscribeReduced,
		prefersReducedMotion,
		() => false,
	);
	const visible = useSyncExternalStore(
		subscribeVisibility,
		documentVisible,
		() => true,
	);
	const [inView, setInView] = useState(target === undefined);
	const entryRef = useRef<FrameEntry | null>(null);

	// Observe the target once it exists.
	useEffect(() => {
		const el = target?.current;
		if (!el) {
			setInView(true);
			return;
		}
		return observeVisibility(el, NEAR_MARGIN, setInView);
	}, [target]);

	// One scheduler entry per mounted hook.
	useEffect(() => {
		const entry = createFrameEntry(DEFAULT_FPS, (time, delta, still) =>
			callback.current(time, delta, still),
		);
		entryRef.current = entry;
		scheduler().add(entry);
		return () => {
			scheduler().remove(entry);
			entryRef.current = null;
		};
	}, []);

	useEffect(() => {
		const entry = entryRef.current;
		if (entry) entry.fps = clampFps(fps);
	}, [fps]);

	const state = frameState({
		enabled,
		inView,
		documentVisible: visible,
		reducedMotion: reduced,
	});

	useEffect(() => {
		const entry = entryRef.current;
		if (!entry) return;
		scheduler().setActive(entry, state === "running");
		if (state === "static") {
			const why: StillReason = reduced ? "reduced" : "disabled";
			const time = reduced
				? stillTime
				: entry.time > 0
					? entry.time
					: stillTime;
			callback.current(time, 0, why);
		}
	}, [state, reduced, stillTime]);

	return state;
}

/**
 * Seconds on the shared clock as a ref: no re-render per frame. Read
 * `.current` inside your own draw code. Holds `stillTime` under reduced motion.
 */
export function useDitherClock(opts: {
	fps?: number;
	enabled?: boolean;
	target?: RefObject<Element | null>;
	stillTime?: number;
}): MutableRefObject<number> {
	const time = useRef(opts.stillTime ?? 0);
	useDitherFrame((t) => {
		time.current = t;
	}, opts);
	return time;
}

// -- the documented public surface ------------------------------------------

/**
 * Shared easing and timing for interface pieces' transitions. Strings are
 * ready for CSS; `ms` has the same durations as numbers.
 */
export const MOTION = {
	/** A strong ease-out. */
	ease: "cubic-bezier(0.23, 1, 0.32, 1)",
	/** Presses and colour changes. */
	fast: "150ms",
	/** Small reveals. */
	base: "240ms",
	/** Large surfaces fading in. */
	slow: "600ms",
	ms: { fast: 150, base: 240, slow: 600 },
} as const;

/** Live `prefers-reduced-motion`; follows the setting while the page is open. */
export function usePrefersReducedMotion(): boolean {
	return useSyncExternalStore(
		subscribeReduced,
		prefersReducedMotion,
		() => false,
	);
}

/** `false` while the tab is hidden. */
export function useDocumentVisible(): boolean {
	return useSyncExternalStore(subscribeVisibility, documentVisible, () => true);
}

/** Is `ref`'s element within `margin` of the viewport? Default `NEAR_MARGIN`. */
export function useInView(
	ref: RefObject<Element | null>,
	margin: string = NEAR_MARGIN,
): boolean {
	const [inView, setInView] = useState(false);
	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		return observeVisibility(el, margin, setInView);
	}, [ref, margin]);
	return inView;
}

/**
 * Your own frame loop on the shared scheduler: `onFrame(time, delta)` once per
 * frame while `target` is on screen. Under reduced motion or with
 * `enabled: false` it calls once with `stillTime` and a delta of 0. Returns
 * `"running"`, `"paused"` or `"static"`; re-renders only when that changes.
 */
export function useFrameLoop(
	onFrame: (time: number, delta: number) => void,
	options: DitherFrameOptions = {},
): DitherFrameState {
	return useDitherFrame((time, delta) => onFrame(time, delta), options);
}
