/**
 * The scheduler behind the homepage's replicas that tell a story once.
 *
 * A story is a list of steps on a clock. It starts the first time its element is
 * mostly in view and plays to the end, then rests on the end state for good. One
 * story plays at a time: a second one that comes into view waits for the first to
 * finish, so the page never moves in two places. There is no
 * `requestAnimationFrame`: each step is one timeout, and the motion between steps
 * is CSS transitions the step starts. Under reduced motion nothing plays: `end()`
 * runs once and the replica rests on the state the server rendered.
 */

/** One thing that happens, `at` ms after the story starts. */
export interface Step {
	at: number;
	run: () => void;
}

export interface PlayOptions {
	/** When the last step has settled, in ms from the start. Default: the last step's `at`. */
	length?: number;
	/** The end state, drawn without playing (reduced motion). The server markup is already this. */
	end: () => void;
	/** Share of the element that must be in view to play. Default 0.5. */
	threshold?: number;
}

/** The browser, as the scheduler sees it. Tests pass their own. */
export interface PlayEnv {
	setTimeout(run: () => void, ms: number): unknown;
	clearTimeout(id: unknown): void;
	/** Calls `onChange` with whether the element is at least `threshold` in view; returns the unsubscribe. */
	observe(
		el: Element,
		threshold: number,
		onChange: (inView: boolean) => void,
	): () => void;
	reduced(): boolean;
}

export const IN_VIEW = 0.5;

export function browserEnv(): PlayEnv {
	const query = matchMedia("(prefers-reduced-motion: reduce)");
	return {
		setTimeout: (run, ms) => setTimeout(run, ms),
		clearTimeout: (id) => clearTimeout(id as number),
		observe(el, threshold, onChange) {
			const io = new IntersectionObserver(
				(entries) => {
					const last = entries.at(-1);
					if (last)
						onChange(
							last.isIntersecting && last.intersectionRatio >= threshold,
						);
				},
				{ threshold: [0, threshold, 1] },
			);
			io.observe(el);
			return () => io.disconnect();
		},
		reduced: () => query.matches,
	};
}

/** The story that is playing, and those waiting for it, in the order they came into view. */
interface Turn {
	start: () => void;
}
let playing: Turn | null = null;
const waiting: Turn[] = [];

function next() {
	playing = waiting.shift() ?? null;
	playing?.start();
}

function take(turn: Turn) {
	if (playing === null) {
		playing = turn;
		turn.start();
	} else waiting.push(turn);
}

function release(turn: Turn) {
	const queued = waiting.indexOf(turn);
	if (queued >= 0) waiting.splice(queued, 1);
	if (playing === turn) next();
}

/**
 * Plays `steps` once, the first time `el` is in view and no other story is
 * playing. Returns `stop()`: it cancels the story for good (a visitor took over
 * the replica) and leaves the DOM as it is, so the caller decides what a
 * hand-over looks like.
 */
export function playOnce(
	el: Element,
	steps: readonly Step[],
	opts: PlayOptions,
	env: PlayEnv = browserEnv(),
): () => void {
	if (env.reduced()) {
		opts.end();
		return () => {};
	}
	const sorted = [...steps].sort((a, b) => a.at - b.at);
	const length = opts.length ?? sorted.at(-1)?.at ?? 0;
	const timers: unknown[] = [];
	let stopped = false;
	let fired = false;

	const turn: Turn = {
		start() {
			if (stopped) return;
			for (const step of sorted) timers.push(env.setTimeout(step.run, step.at));
			timers.push(env.setTimeout(() => release(turn), length));
		},
	};

	let offView: (() => void) | undefined = env.observe(
		el,
		opts.threshold ?? IN_VIEW,
		(inView) => {
			if (!inView || stopped || fired) return;
			fired = true;
			offView?.();
			offView = undefined;
			take(turn);
		},
	);
	// The observer may have fired before `offView` was assigned.
	if (fired) {
		offView?.();
		offView = undefined;
	}

	return () => {
		if (stopped) return;
		stopped = true;
		offView?.();
		for (const id of timers) env.clearTimeout(id);
		release(turn);
	};
}
