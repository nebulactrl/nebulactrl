import "./styles/site.css";
import { enhance } from "./enhance.ts";
import type { IslandName } from "./islands/island.tsx";
import { loaders } from "./islands/registry.ts";

// No framework runs on page load. This file finds the islands, and only when
// one is due (idle, or scrolled near) does it fetch React and hydrate that one.

document.documentElement.classList.replace("no-js", "js");

async function hydrate(el: HTMLElement) {
	const name = el.dataset.island as IslandName;
	const load = loaders[name];
	if (!load || el.dataset.hydrated) return;
	el.dataset.hydrated = "";
	const [{ default: Component }, { hydrateRoot }, { createElement }] =
		await Promise.all([load(), import("react-dom/client"), import("react")]);
	const props = JSON.parse(el.dataset.props ?? "{}") as Record<string, unknown>;
	hydrateRoot(el, createElement(Component as never, props));
}

function whenIdle(run: () => void) {
	if ("requestIdleCallback" in window)
		window.requestIdleCallback(run, { timeout: 1500 });
	else setTimeout(run, 200);
}

const islands = document.querySelectorAll<HTMLElement>("[data-island]");
// The island a watched element belongs to. An island wrapper is `display:
// contents` unless it has a class of its own, and a box-less element never
// intersects: such an island is watched through its first child.
const owner = new Map<Element, HTMLElement>();
const visible = new IntersectionObserver(
	(entries) => {
		for (const entry of entries) {
			if (!entry.isIntersecting) continue;
			visible.unobserve(entry.target);
			const el = owner.get(entry.target);
			if (el) void hydrate(el);
		}
	},
	{ rootMargin: "0px" },
);
for (const el of islands) {
	if (el.dataset.hydrate !== "visible") {
		whenIdle(() => void hydrate(el));
		continue;
	}
	const box =
		getComputedStyle(el).display === "contents"
			? (el.firstElementChild ?? el)
			: el;
	owner.set(box, el);
	visible.observe(box);
}

enhance();

// Copy keys on the code blocks the docs compiler writes: plain delegated behaviour, no React.
document.addEventListener("click", (event) => {
	const button = (event.target as HTMLElement).closest<HTMLButtonElement>(
		"[data-docs-copy]",
	);
	if (!button) return;
	const text =
		button.closest(".docs-code")?.querySelector("pre")?.textContent ?? "";
	const label = button.querySelector<HTMLElement>(".docs-code-copy-text");
	navigator.clipboard.writeText(text.replace(/\n$/, "")).then(
		() => {
			button.setAttribute("data-copied", "");
			if (label) label.textContent = "Copied";
			setTimeout(() => {
				button.removeAttribute("data-copied");
				if (label) label.textContent = "Copy";
			}, 1800);
		},
		() => {
			if (label) label.textContent = "Press Ctrl+C";
		},
	);
});
