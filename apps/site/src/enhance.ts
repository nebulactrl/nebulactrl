/**
 * Plain DOM behaviour with no framework, so the first screen costs no React.
 * The homepage's stories are islands (the product replica, the bench, the hero's Eclipse), and the
 * load sequence is CSS. Measured bars fill once as they scroll into view
 * (`initBars`): the one reveal the visitor's scroll starts.
 */

import { initAnchors } from "./anchors.ts";

/**
 * iOS Safari paints `:active` only under a `touchstart` listener, so without
 * one a key does not sink under a finger. One empty passive listener on the
 * body turns the press back on for every key on the page.
 */
document.body.addEventListener("touchstart", () => {}, { passive: true });

const reduced = () =>
	window.matchMedia("(prefers-reduced-motion: reduce)").matches;

/** Gap between neighbouring bars that reveal in the same frame, and how many gaps at most. */
const BAR_STAGGER_MS = 70;
const BAR_STAGGER_STEPS = 4;

/**
 * The measured bars (`[data-bars]` groups) are rendered at their final value.
 * Until a bar scrolls into view the stylesheet holds it at `--dither-grow: 0`;
 * revealing it drops that, and the kit's own width transition fills it. One
 * reveal per bar, never replayed. Bars that enter in the same frame (a whole
 * group on a wide screen) are staggered 70ms apart, so the group reads as one
 * moment; on a narrow screen each bar fills as it arrives.
 */
function initBars() {
	const bars = [
		...document.querySelectorAll<HTMLElement>(
			'[data-bars] [role="progressbar"]',
		),
	];
	if (bars.length === 0) return;
	if (reduced() || !("IntersectionObserver" in window)) {
		for (const bar of bars) bar.dataset.revealed = "";
		return;
	}
	const io = new IntersectionObserver(
		(entries) => {
			const due = entries
				.filter((entry) => entry.isIntersecting)
				.sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
			due.forEach((entry, index) => {
				const bar = entry.target as HTMLElement;
				io.unobserve(bar);
				bar.style.setProperty(
					"--dither-fill-delay",
					`${Math.min(index, BAR_STAGGER_STEPS) * BAR_STAGGER_MS}ms`,
				);
				bar.dataset.revealed = "";
				// The delay is for the reveal only; later changes tween at once.
				window.setTimeout(
					() => bar.style.removeProperty("--dither-fill-delay"),
					2000,
				);
			});
		},
		{ threshold: 0.6, rootMargin: "0px 0px -8% 0px" },
	);
	for (const bar of bars) io.observe(bar);
}
/**
 * The nav's Install key scrolls to the install well (a plain anchor, so it works
 * without a script); with a script it also puts the focus on the Copy key once the
 * well is on screen, so Enter copies the command.
 */
function initInstallLink() {
	for (const anchor of document.querySelectorAll<HTMLAnchorElement>(
		"[data-install-link]",
	)) {
		anchor.addEventListener("click", () => {
			const key = document.querySelector<HTMLElement>("#install button");
			if (key) window.setTimeout(() => key.focus({ preventScroll: true }), 400);
		});
	}
}

/**
 * The mark inside the closing heading (`[data-splay-host]`) opens once, 300ms
 * after the heading scrolls into view, and closes again; hovering it does the
 * same through the kit. Reduced motion leaves it still.
 */
const SPLAY_MS = 1400;
function initSplay() {
	if (reduced() || !("IntersectionObserver" in window)) return;
	for (const host of document.querySelectorAll<HTMLElement>(
		"[data-splay-host]",
	)) {
		const mark = host.querySelector(".nc-mark");
		if (!mark) continue;
		const io = new IntersectionObserver(
			(entries) => {
				if (!entries.some((entry) => entry.isIntersecting)) return;
				io.disconnect();
				window.setTimeout(() => {
					mark.setAttribute("data-splay", "");
					window.setTimeout(() => mark.removeAttribute("data-splay"), SPLAY_MS);
				}, 300);
			},
			{ threshold: 0.9 },
		);
		io.observe(host);
	}
}

/**
 * The one orchestrated moment of the page: a `[data-reveal]` element (the
 * dashboard under the hero) is held back by the stylesheet until it first comes
 * into view, then `data-shown` lets it rise into place. Once; never replayed.
 * Without IntersectionObserver it is shown at once.
 */
function initReveal() {
	const targets = document.querySelectorAll<HTMLElement>("[data-reveal]");
	if (!("IntersectionObserver" in window)) {
		for (const el of targets) el.dataset.shown = "";
		return;
	}
	const io = new IntersectionObserver(
		(entries) => {
			for (const entry of entries) {
				if (!entry.isIntersecting) continue;
				(entry.target as HTMLElement).dataset.shown = "";
				io.unobserve(entry.target);
			}
		},
		{ rootMargin: "0px 0px -10% 0px" },
	);
	for (const el of targets) io.observe(el);
}

export function enhance() {
	initBars();
	initInstallLink();
	initSplay();
	initReveal();
	initAnchors();
}
