/**
 * Entry for the self-contained maintenance/error pages. `scripts/build-pages-engine.ts`
 * bundles this plus the engine into one IIFE that the pages inline.
 *
 * Markup contract (no CSS vars needed, no network requests):
 *   <canvas data-ascii-canvas [data-font="..."]>   sized by the page's CSS
 *   [data-ascii-card]    the panel: soft dim around it
 *   [data-ascii-quiet]   text drawn over the field (title/wordmark): dimmed hard
 *
 * On the first painted frame `<html data-ascii="on">`; without WebGL2 or on
 * failure `data-ascii="off"` and nothing is drawn, so the page's own floor stays.
 * Reduced motion paints one static frame; hidden tabs and off-screen pause.
 */
import { DEFAULT_FONT_FAMILY } from "./core";
import { NEBULA_PALETTE } from "./palette";
import { createEclipse as createAscii } from "./scene-eclipse";

function mount(): void {
	const canvas = document.querySelector<HTMLCanvasElement>(
		"canvas[data-ascii-canvas]",
	);
	if (!canvas) return;
	const root = document.documentElement;
	const card = document.querySelector("[data-ascii-card]");
	const engine = createAscii(canvas, {
		palette: NEBULA_PALETTE,
		fontFamily: canvas.dataset.font ?? DEFAULT_FONT_FAMILY,
		maskRect: () => card?.getBoundingClientRect() ?? null,
		quietRects: () =>
			Array.from(document.querySelectorAll("[data-ascii-quiet]"), (el) =>
				el.getBoundingClientRect(),
			),
		onStatus: (status) => {
			root.dataset.ascii = status === "ready" ? "on" : "off";
		},
	});
	if (!engine) {
		root.dataset.ascii = "off";
		return;
	}
	engine.start();
	// Re-measure when late layout (webfonts, wrapping) moves the card.
	if (card && typeof ResizeObserver === "function") {
		new ResizeObserver(() => engine.resize()).observe(card);
	}
}

if (document.readyState === "loading") {
	document.addEventListener("DOMContentLoaded", mount, { once: true });
} else {
	mount();
}

Object.assign(window, {
	NebulaAscii: { createAscii, PALETTE: NEBULA_PALETTE },
});
