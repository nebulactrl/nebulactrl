import { isRepeatingKey, type NavInteraction } from "./nav-plan";

// Remembers the last thing the visitor pressed, so the navigation it causes
// can tell where it came from. A card press has the card to morph out of; a
// sidebar or palette press wants the fast variant; a held arrow key wants
// no animation at all. The listeners are capture-phase and passive, run
// before any handler can navigate, and only write two fields.

/** Where a press counts as frequent navigation: the sidebar and the palette. */
const FREQUENT_SELECTOR =
	"[data-vt-frequent], aside[aria-label='Primary'], [role='dialog'][aria-label='Command palette']";

/** A press on one of these may carry its `data-vt` key on a child. */
const CONTAINER_SELECTOR = "a, button, tr, [role='link'], [role='button']";

const PRESSABLE_KEY = "[data-vt]:not([data-vt-anchor])";

interface Recorded {
	source: NavInteraction["source"];
	element: Element | null;
	frequent: boolean;
	repeating: boolean;
	at: number;
}

let last: Recorded | null = null;
let started = false;

function record(
	source: Recorded["source"],
	target: EventTarget | null,
	repeating: boolean,
) {
	const element = target instanceof Element ? target : null;
	last = {
		source,
		element,
		frequent: element?.closest(FREQUENT_SELECTOR) != null,
		repeating,
		at: performance.now(),
	};
}

/**
 * The element carrying the `data-vt` key for a press on `target`: the
 * nearest keyed ancestor, or, for a press on a link or button, the keyed
 * child inside it (a sidebar link whose label carries the key). Anchors
 * (a whole page's root) are not pressed sources: they are armed on their
 * own when a navigation leaves their page.
 */
function keyedElementFor(target: Element | null): HTMLElement | null {
	if (!target) return null;
	const own = target.closest<HTMLElement>(PRESSABLE_KEY);
	if (own) return own;
	return (
		target
			.closest(CONTAINER_SELECTOR)
			?.querySelector<HTMLElement>(PRESSABLE_KEY) ?? null
	);
}

/** Starts the document-wide recorder. Safe to call more than once. */
export function startInteractionRecorder(): void {
	if (started || typeof document === "undefined") return;
	started = true;
	document.addEventListener(
		"pointerdown",
		(event) => {
			if (event.button !== 0) return;
			record("pointer", event.target, false);
		},
		{ capture: true, passive: true },
	);
	document.addEventListener(
		"keydown",
		(event) => {
			// A held modifier starts a pointer gesture or a shortcut chord.
			if (["Shift", "Control", "Alt", "Meta"].includes(event.key)) return;
			record("keyboard", event.target, isRepeatingKey(event));
		},
		{ capture: true, passive: true },
	);
	window.addEventListener("popstate", () => record("history", null, false), {
		passive: true,
	});
}

export interface TakenInteraction {
	interaction: NavInteraction;
	/** The keyed element the press landed on, if it is still on the page. */
	pressed: HTMLElement | null;
}

/** The last press, once: a later navigation cannot adopt it again. */
export function takeInteraction(
	now: number = performance.now(),
): TakenInteraction | null {
	const current = last;
	last = null;
	if (!current) return null;
	const element = keyedElementFor(current.element);
	return {
		interaction: {
			source: current.source,
			frequent: current.frequent,
			repeating: current.repeating,
			ageMs: now - current.at,
		},
		pressed: element?.isConnected ? element : null,
	};
}
