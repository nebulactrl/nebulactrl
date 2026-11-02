/*
 * `motion/mini` accepts an element only if `element instanceof EventTarget`.
 * Under happy-dom the global EventTarget is not the class DOM nodes derive
 * from, so every slide animation throws "No valid elements provided". A test
 * that mounts the inspector's panel swaps the global for the DOM's own class.
 */
const DomEventTarget: typeof EventTarget = Object.getPrototypeOf(
	Node.prototype,
).constructor;

/** Makes DOM nodes `instanceof EventTarget`; returns what undoes it. */
export function useDomEventTarget(): () => void {
	const saved = globalThis.EventTarget;
	globalThis.EventTarget = DomEventTarget;
	return () => {
		globalThis.EventTarget = saved;
	};
}
