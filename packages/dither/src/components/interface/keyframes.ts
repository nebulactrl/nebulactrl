/**
 * The few keyframes the interface pieces need that the app stylesheet does not
 * define: the skeleton's stepped pulse and the empty state's one-cell bob.
 * Injected once into a single `<style>`, like the band masks, and all of it
 * stands still under `prefers-reduced-motion`.
 *
 * Both animations use `step-end`: the dither only ever moves by whole levels
 * or whole cells, never in between.
 */
const CSS = `
@keyframes nc-if-p1{0%{opacity:0}16.667%{opacity:1}100%{opacity:0}}
@keyframes nc-if-p2{0%{opacity:0}33.333%{opacity:1}83.333%{opacity:0}100%{opacity:0}}
@keyframes nc-if-p3{0%{opacity:0}50%{opacity:1}66.667%{opacity:0}100%{opacity:0}}
.nc-if-pulse{opacity:0;animation-duration:var(--nc-if-dur,1.8s);animation-delay:var(--nc-if-delay,0s);animation-iteration-count:infinite;animation-timing-function:step-end}
.nc-if-p1{animation-name:nc-if-p1}
.nc-if-p2{animation-name:nc-if-p2}
.nc-if-p3{animation-name:nc-if-p3}
@keyframes nc-if-bob{0%,49.999%{transform:translateY(0)}50%,100%{transform:translateY(-1px)}}
.nc-if-bob{animation:nc-if-bob 2.4s step-end infinite}
@media (prefers-reduced-motion:reduce){.nc-if-pulse,.nc-if-bob{animation:none}}
`;

let injected = false;

/** Mount the sheet once. Safe to call from render: it does nothing after the first call or off the DOM. */
export function ensureInterfaceStyles(): void {
	if (injected || typeof document === "undefined") return;
	injected = true;
	const style = document.createElement("style");
	style.setAttribute("data-nc-interface-keyframes", "");
	style.textContent = CSS;
	document.head.appendChild(style);
}

ensureInterfaceStyles();
