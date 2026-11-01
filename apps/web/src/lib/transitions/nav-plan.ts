// Pure decisions behind every route transition: whether a navigation
// animates, which variant it plays, and which shared elements are armed.
// `route-transitions.ts` gathers the facts from the document and the router
// and applies the answer; nothing here reads either.

/** `none` navigates instantly; the others set the timing in styles.css. */
export type NavVariant = "none" | "page" | "fast" | "reduced";

/** What the visitor did just before the navigation, as `interaction.ts` saw it. */
export interface NavInteraction {
	source: "pointer" | "keyboard" | "history";
	/** Pressed inside the sidebar or the command palette. */
	frequent: boolean;
	/** A held key or an arrow-style key: a stream of navigations, not one. */
	repeating: boolean;
	/** Milliseconds between that input and the navigation. */
	ageMs: number;
}

export interface NavInput {
	supported: boolean;
	/** A previous location existed and its path differs from the new one. */
	pathChanged: boolean;
	reducedMotion: boolean;
	interaction: NavInteraction | null;
	/** Milliseconds since the previous transition started. */
	sinceLastMs: number;
}

export interface NavPlan {
	variant: NavVariant;
	/** Whether cards, rows, titles and tabs morph (never under reduced motion). */
	shared: boolean;
	/** Why, for the tests and for anyone debugging a missing animation. */
	reason: string;
}

/**
 * An input older than this did not cause the navigation now resolving: a
 * slow loader can hold a navigation back for a while after the click, but
 * not forever.
 */
export const INTERACTION_FRESH_MS = 2000;

/**
 * Navigations closer together than this are a stream (a key walking a
 * list, a script redirecting twice): only the last one is worth showing.
 */
export const RAPID_MS = 120;

const none = (reason: string): NavPlan => ({
	variant: "none",
	shared: false,
	reason,
});

export function classifyNavigation(input: NavInput): NavPlan {
	if (!input.supported) return none("unsupported");
	if (!input.pathChanged) return none("same-path");

	const interaction =
		input.interaction && input.interaction.ageMs <= INTERACTION_FRESH_MS
			? input.interaction
			: null;
	if (interaction?.repeating) return none("repeating-key");
	if (input.sinceLastMs < RAPID_MS) return none("rapid");

	if (input.reducedMotion) {
		return { variant: "reduced", shared: false, reason: "reduced-motion" };
	}
	const frequent =
		interaction !== null &&
		(interaction.frequent || interaction.source === "keyboard");
	return frequent
		? { variant: "fast", shared: true, reason: "frequent" }
		: { variant: "page", shared: true, reason: "page" };
}

/** Keys that walk through things: each press moves on, so none should animate. */
const SCRUB_KEYS: Record<string, true> = {
	ArrowUp: true,
	ArrowDown: true,
	ArrowLeft: true,
	ArrowRight: true,
	Home: true,
	End: true,
	PageUp: true,
	PageDown: true,
};

/** A keyboard event reduced to what decides whether it is a stream of input. */
export function isRepeatingKey(event: {
	key: string;
	repeat: boolean;
}): boolean {
	return event.repeat || Object.hasOwn(SCRUB_KEYS, event.key);
}

/** An element that can take part in a morph: its `data-vt` key and itself. */
export interface KeyedElement<E> {
	key: string;
	element: E;
}

/** How many anchors one navigation arms at most. */
const MAX_ANCHORS = 3;

/**
 * The elements to name for one navigation. A navigation started from a
 * keyed element (a card, a row, a sidebar label) arms exactly that one. One
 * started from anywhere else arms the leaving page's anchors instead, so a
 * canvas shrinks into its card on the way back; with a keyed press the
 * anchors stay quiet, since the visitor is going somewhere else. Reduced
 * motion, or any navigation that is not `shared`, arms nothing.
 */
export function selectArmed<E>(options: {
	shared: boolean;
	pressed: KeyedElement<E> | null;
	anchors: readonly KeyedElement<E>[];
}): KeyedElement<E>[] {
	if (!options.shared) return [];
	if (options.pressed) return [options.pressed];
	const seen = new Set<string>();
	const armed: KeyedElement<E>[] = [];
	for (const anchor of options.anchors) {
		if (seen.has(anchor.key)) continue;
		seen.add(anchor.key);
		armed.push(anchor);
		if (armed.length === MAX_ANCHORS) break;
	}
	return armed;
}
