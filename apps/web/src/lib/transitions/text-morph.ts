import { durations } from "@nebulactrl/tokens";
import { prefersReducedMotion } from "./reduced-motion";

/**
 * Text morph: changes a short label in place when the old
 * and new text share letters at the start or the end ("Copy" to "Copied",
 * "Save" to "Saved"). The shared letters stay where they are; only the part
 * that differs cross-blurs while its width eases to the new text. The CSS is
 * `.t-morph*` in `transitions.css`.
 *
 * Text that shares nothing ("Processing" to "Done") is a different job: a
 * plain swap, which this also falls back to (the whole text is then the
 * changing part).
 *
 * The result is always the plain new text: `el.dataset.text` is the source of
 * truth while a morph runs, so a second change mid-flight starts from the
 * right text. Reduced motion sets the text at once.
 */

interface MorphElement extends HTMLElement {
	_morphTimer?: number;
}

function cssMs(name: string, fallback: number): number {
	const raw = getComputedStyle(document.documentElement)
		.getPropertyValue(name)
		.trim();
	const value = Number.parseFloat(raw);
	if (Number.isNaN(value)) return fallback;
	return raw.endsWith("ms") ? value : value * 1000;
}

/** The letters two texts share at the start and at the end. Exported for tests. */
export function sharedAffixes(
	prev: string,
	next: string,
): { start: number; end: number } {
	let start = 0;
	while (
		start < prev.length &&
		start < next.length &&
		prev[start] === next[start]
	)
		start++;
	let end = 0;
	while (
		end < prev.length - start &&
		end < next.length - start &&
		prev[prev.length - 1 - end] === next[next.length - 1 - end]
	)
		end++;
	return { start, end };
}

/** Sets `el`'s text with no motion (first paint, reduced motion). */
export function setMorphText(el: HTMLElement, text: string): void {
	const target = el as MorphElement;
	window.clearTimeout(target._morphTimer);
	target.dataset.text = text;
	target.textContent = text;
}

/** Morphs `el` from its current text to `next`. */
export function morphText(el: HTMLElement, next: string): void {
	const target = el as MorphElement;
	const prev = target.dataset.text ?? target.textContent ?? "";
	if (prev === next) return;
	if (prefersReducedMotion() || !target.isConnected) {
		setMorphText(target, next);
		return;
	}
	target.dataset.text = next;
	window.clearTimeout(target._morphTimer);

	const { start, end } = sharedAffixes(prev, next);

	const swap = document.createElement("span");
	swap.className = "t-morph-swap";
	const out = document.createElement("span");
	out.className = "t-morph-old";
	out.setAttribute("aria-hidden", "true");
	out.textContent = prev.slice(start, prev.length - end);
	const inn = document.createElement("span");
	inn.className = "t-morph-new";
	inn.textContent = next.slice(start, next.length - end);
	swap.append(out, inn);

	target.textContent = "";
	target.append(next.slice(0, start), swap, next.slice(next.length - end));

	swap.style.width = `${out.getBoundingClientRect().width}px`;
	void swap.offsetWidth; // force a reflow so the width transitions
	swap.classList.add("is-morphing");
	swap.style.width = `${inn.getBoundingClientRect().width}px`;

	const settle =
		Math.max(
			cssMs("--text-morph-dur", durations.quick),
			cssMs("--text-morph-resize-dur", durations.fast),
		) + 40;
	target._morphTimer = window.setTimeout(() => {
		target.textContent = next;
	}, settle);
}
