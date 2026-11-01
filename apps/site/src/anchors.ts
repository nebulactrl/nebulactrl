/**
 * Same page links scroll with motion: a requestAnimationFrame loop on an ease
 * in out curve, its length scaled to the distance. The browser's own jump stays
 * the fallback (no script, a modifier click, reduced motion), and the hash and
 * the focus follow the scroll, so the link behaves as a link.
 */

/** `cubic-bezier(0.77, 0, 0.175, 1)`: the ease in out curve of on screen movement. */
const [X1, Y1, X2, Y2] = [0.77, 0, 0.175, 1] as const;

const bezier = (a: number, b: number, t: number) =>
	3 * a * (1 - t) ** 2 * t + 3 * b * (1 - t) * t ** 2 + t ** 3;
const slope = (a: number, b: number, t: number) =>
	3 * a * (1 - t) ** 2 + 6 * (b - a) * (1 - t) * t + 3 * (1 - b) * t ** 2;

/** Progress (0..1) of the curve at time `x` (0..1): the curve's t for x, then its y, by Newton steps with a bisection fallback. */
export function ease(x: number): number {
	if (x <= 0) return 0;
	if (x >= 1) return 1;
	let t = x;
	for (let i = 0; i < 8; i++) {
		const error = bezier(X1, X2, t) - x;
		if (Math.abs(error) < 1e-6) return bezier(Y1, Y2, t);
		const d = slope(X1, X2, t);
		if (Math.abs(d) < 1e-6) break;
		t -= error / d;
	}
	let lo = 0;
	let hi = 1;
	t = x;
	for (let i = 0; i < 24; i++) {
		if (bezier(X1, X2, t) < x) lo = t;
		else hi = t;
		t = (lo + hi) / 2;
	}
	return bezier(Y1, Y2, t);
}

export const MIN_MS = 450;
export const MAX_MS = 900;

/** How long a scroll of `distance` px takes: 300 ms plus 0.15 ms a pixel, held between 450 and 900 ms. */
export function duration(distance: number): number {
	return Math.min(MAX_MS, Math.max(MIN_MS, 300 + Math.abs(distance) * 0.15));
}

const px = (value: string) => {
	const n = Number.parseFloat(value);
	return Number.isFinite(n) ? n : 0;
};

/** Where the page must scroll for `el` to sit where a native anchor jump would put it. */
function targetTop(el: Element): number {
	const margin = px(getComputedStyle(el).scrollMarginTop);
	const padding = px(
		getComputedStyle(document.documentElement).scrollPaddingTop,
	);
	const top =
		el.getBoundingClientRect().top + window.scrollY - margin - padding;
	const max = document.documentElement.scrollHeight - window.innerHeight;
	return Math.min(Math.max(0, top), Math.max(0, max));
}

let stop: (() => void) | undefined;

/** Scrolls to `top`, then calls `done`. A wheel, a touch, a key or a pointer press by the visitor ends it where it is. */
function scrollTo(top: number, done: () => void) {
	stop?.();
	const from = window.scrollY;
	const distance = top - from;
	const total = duration(distance);
	const start = performance.now();
	let frame = 0;
	const events = ["wheel", "touchstart", "keydown", "pointerdown"] as const;
	const finish = () => {
		cancelAnimationFrame(frame);
		for (const name of events) window.removeEventListener(name, finish);
		stop = undefined;
		done();
	};
	const step = (now: number) => {
		const t = Math.min(1, (now - start) / total);
		window.scrollTo(0, from + distance * ease(t));
		if (t < 1) frame = requestAnimationFrame(step);
		else finish();
	};
	for (const name of events)
		window.addEventListener(name, finish, { passive: true, once: true });
	stop = finish;
	frame = requestAnimationFrame(step);
}

/** The link's own target when it points into this page: same origin, path and query, and a hash that names an element. */
function sameDocumentTarget(link: HTMLAnchorElement): HTMLElement | null {
	const url = new URL(link.href, location.href);
	if (url.origin !== location.origin) return null;
	if (url.pathname !== location.pathname || url.search !== location.search)
		return null;
	if (url.hash.length < 2) return null;
	const id = decodeURIComponent(url.hash.slice(1));
	return document.getElementById(id);
}

function focusTarget(el: HTMLElement) {
	// Something inside the target already took the focus (the nav's Install link puts it on the Copy key).
	if (
		el.contains(document.activeElement) &&
		document.activeElement !== document.body
	)
		return;
	if (
		!el.hasAttribute("tabindex") &&
		!el.matches("a[href], button, input, select, textarea, summary")
	)
		el.setAttribute("tabindex", "-1");
	el.focus({ preventScroll: true });
}

export function initAnchors() {
	const reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
	document.addEventListener("click", (event) => {
		if (event.defaultPrevented || event.button !== 0) return;
		if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey)
			return;
		const link = (event.target as Element | null)?.closest<HTMLAnchorElement>(
			"a[href]",
		);
		if (
			!link ||
			(link.target && link.target !== "_self") ||
			link.hasAttribute("download")
		)
			return;
		const el = sameDocumentTarget(link);
		if (!el) return;
		event.preventDefault();
		const hash = new URL(link.href, location.href).hash;
		if (location.hash !== hash) history.pushState(null, "", hash);
		const top = targetTop(el);
		if (reduced.matches || Math.abs(top - window.scrollY) < 1) {
			window.scrollTo(0, top);
			focusTarget(el);
			return;
		}
		scrollTo(top, () => focusTarget(el));
	});
}
