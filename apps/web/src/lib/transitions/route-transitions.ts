import { startInteractionRecorder, takeInteraction } from "./interaction";
import {
	classifyNavigation,
	type KeyedElement,
	type NavVariant,
	selectArmed,
} from "./nav-plan";
import { prefersReducedMotion } from "./reduced-motion";
import { armSources, pairTargets } from "./shared-elements";
import { keySelector, NameRegistry, PAGE_CLASS, PAGE_NAME } from "./vt-names";

// Every route change runs inside `document.startViewTransition`, and the
// shared elements (a project card growing into its canvas, a sidebar label
// becoming the page title, a server card becoming its page header) morph
// through the browser's own snapshots. The timing and the look live in the
// `::view-transition-*` rules in styles.css; this file decides when a
// transition runs and which elements are named for it.
//
// How it hooks into TanStack Router (1.170): the router wraps the commit of
// every navigation in `router.startViewTransition(update)`, which consults
// `defaultViewTransition` and the per-navigation `viewTransition` flag. Both
// only let a navigation opt in or out and pick `types`; neither hands back
// the `ViewTransition`, and the transition's end is the one moment names can
// be cleaned up safely. So the router's own method is replaced by this one.
// It is a public, typed member of the router, `update` is the router's
// commit and is awaited exactly as before, and without the API the
// navigation is just `update()`.

/** The slice of the router this file uses; the real router satisfies it. */
export interface TransitionRouter {
	startViewTransition: (update: () => Promise<void>) => unknown;
	latestLocation: { pathname: string };
	state: { resolvedLocation?: { pathname: string } | undefined };
}

/** Whether this browser can run view transitions at all. */
function supportsViewTransitions(): boolean {
	return (
		typeof document !== "undefined" &&
		typeof document.startViewTransition === "function"
	);
}

const registry = new NameRegistry<HTMLElement>();
let nextOwner = 0;
let lastStartedAt = Number.NEGATIVE_INFINITY;

interface Active {
	owner: number;
	transition: ViewTransition;
}
let active: Active | null = null;

function setRootState(variant: NavVariant, scope: "page" | "root") {
	const root = document.documentElement;
	root.dataset.vtVariant = variant;
	root.dataset.vtScope = scope;
}

/** Ends one transition's hold on the page: its names, and the root flags. */
function finish(owner: number) {
	registry.release(owner);
	if (active?.owner !== owner) return;
	active = null;
	delete document.documentElement.dataset.vtVariant;
	delete document.documentElement.dataset.vtScope;
}

/** Drops a running transition at once, so a plain update shows its result. */
function skipActive() {
	if (!active) return;
	active.transition.skipTransition();
	finish(active.owner);
}

function keyedElements(
	elements: Iterable<HTMLElement>,
): KeyedElement<HTMLElement>[] {
	const keyed: KeyedElement<HTMLElement>[] = [];
	for (const element of elements) {
		const key = element.getAttribute("data-vt");
		if (key) keyed.push({ key, element });
	}
	return keyed;
}

function findTargets(key: string): HTMLElement[] {
	return Array.from(document.querySelectorAll<HTMLElement>(keySelector(key)));
}

function runRouteTransition(
	router: TransitionRouter,
	update: () => Promise<void>,
): Promise<void> {
	const previous = router.state.resolvedLocation?.pathname;
	const now = performance.now();
	// Only a change of path consumes the last press: a search-only navigation
	// (opening an inspector) must not use up the one a card press is waiting for.
	const pathChanged =
		previous !== undefined && previous !== router.latestLocation.pathname;
	const taken = pathChanged ? takeInteraction(now) : null;
	const plan = classifyNavigation({
		supported: supportsViewTransitions(),
		pathChanged,
		reducedMotion: prefersReducedMotion(),
		interaction: taken?.interaction ?? null,
		sinceLastMs: now - lastStartedAt,
	});
	if (plan.variant === "none") {
		// A new page supersedes a transition still running; a search-only
		// update (the canvas filling in its address) leaves it alone.
		if (pathChanged) skipActive();
		return update();
	}

	const owner = ++nextOwner;
	lastStartedAt = now;
	const main = document.getElementById("main-content");

	// Names go on just now and come off when the transition has finished, so
	// nothing else on the page is ever snapshotted and two cards can never
	// share a name.
	if (main) registry.assign(main, PAGE_NAME, PAGE_CLASS, owner);
	const pressedElement = taken?.pressed ?? null;
	const pressedKey = pressedElement?.getAttribute("data-vt") ?? null;
	const armed = armSources(
		registry,
		owner,
		selectArmed({
			shared: plan.shared,
			pressed:
				pressedElement && pressedKey
					? { key: pressedKey, element: pressedElement }
					: null,
			anchors: keyedElements(
				document.querySelectorAll<HTMLElement>("[data-vt-anchor]"),
			),
		}),
	);
	setRootState(plan.variant, main ? "page" : "root");

	let transition: ViewTransition;
	try {
		transition = document.startViewTransition(async () => {
			await update();
			pairTargets(registry, owner, armed, findTargets);
			// A page without the shell on either side (login, setup) has no
			// area to lift: the whole document cross-fades instead.
			const shell = main && document.getElementById("main-content");
			setRootState(plan.variant, shell ? "page" : "root");
		});
	} catch {
		finish(owner);
		return update();
	}
	track(transition, owner);
	// The router awaits the commit, not the animation.
	return transition.updateCallbackDone;
}

let installed = false;

/**
 * Routes every navigation through a view transition. Call once, right
 * after `createRouter`. A browser without view transitions, a navigation
 * that changes only the search or hash, a held arrow key, a reload and
 * reduced motion's lift all end up as the plain update.
 */
export function installRouteTransitions(router: TransitionRouter): void {
	if (installed) return;
	installed = true;
	startInteractionRecorder();
	router.startViewTransition = (update) => runRouteTransition(router, update);
}

/**
 * Remembers the running transition and ends its hold on the page when it
 * finishes, however it ends. `ready` rejects when a transition is skipped,
 * which is routine (a newer navigation took over), not an error.
 */
function track(transition: ViewTransition, owner: number) {
	active = { owner, transition };
	transition.ready.catch(() => undefined);
	transition.updateCallbackDone.catch(() => undefined);
	const done = () => finish(owner);
	void transition.finished.then(done, done);
}
