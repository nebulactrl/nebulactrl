/**
 * Reads `prefers-reduced-motion` at the moment it's called, rather than as a
 * hook, because most of its call sites are imperative (a pointerdown
 * handler, a router event) and not component renders. A component that
 * needs to *re-render* when the preference flips should use `motion/react`'s
 * `useReducedMotion()` instead; this is for the one-shot checks around a
 * WAAPI animation.
 */
export function prefersReducedMotion(): boolean {
	return (
		typeof window !== "undefined" &&
		typeof window.matchMedia === "function" &&
		window.matchMedia("(prefers-reduced-motion: reduce)").matches
	);
}
