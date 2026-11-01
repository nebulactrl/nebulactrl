import { useEffect, useRef } from "react";
import { COPIED_MS, writeClipboard } from "#/components/common/copy-button";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import { morphText, setMorphText } from "#/lib/transitions/text-morph";
import "./docs.css";

/**
 * A compiled docs page. The HTML came out of the build-time compiler (raw
 * HTML in the markdown is escaped there, so nothing here is trusted input).
 * Two behaviours the static markup cannot carry:
 *
 * - the copy key on a code block, and
 * - links: a `/docs/…` or `#anchor` link navigates inside the app instead of
 *   reloading it (`onNavigate`), and an in-page anchor scrolls to its heading.
 */
export function DocContent({
	html,
	hash,
	onNavigate,
	scrollRoot,
}: {
	html: string;
	/** The `#anchor` to scroll to once the page is on screen, without the `#`. */
	hash?: string | undefined;
	/** Called for an in-app link: "#anchor" (already scrolled to) or "/docs/…". The caller decides how to go there. */
	onNavigate: (href: string) => void;
	/** The scrolling ancestor, for deep links; the nearest scrollable parent by default. */
	scrollRoot?: HTMLElement | null;
}) {
	const ref = useRef<HTMLDivElement>(null);

	// Deep links: scroll to the anchor after the HTML is in the DOM.
	// biome-ignore lint/correctness/useExhaustiveDependencies: `html` is the trigger: a new page re-scrolls
	useEffect(() => {
		const root = ref.current;
		if (!root) return;
		if (!hash) {
			(scrollRoot ?? root.closest<HTMLElement>("[data-docs-scroll]"))?.scrollTo(
				{ top: 0 },
			);
			return;
		}
		const target = root.querySelector<HTMLElement>(
			`[id="${CSS.escape(hash)}"]`,
		);
		target?.scrollIntoView({ block: "start" });
	}, [html, hash, scrollRoot]);

	const onClick = (event: React.MouseEvent<HTMLDivElement>) => {
		const target = event.target as HTMLElement;

		const copy = target.closest<HTMLButtonElement>("[data-docs-copy]");
		if (copy) {
			const text =
				copy.closest(".docs-code")?.querySelector("pre")?.textContent ?? "";
			void writeClipboard(text.replace(/\n$/, "")).then((ok) => {
				const label = copy.querySelector<HTMLElement>(".docs-code-copy-text");
				// A key press changes the label with no motion; a click morphs it.
				const say = (text: string) => {
					if (!label) return;
					if (lastInputWasKeyboard()) setMorphText(label, text);
					else morphText(label, text);
				};
				say(ok ? "Copied" : "Press ⌘C");
				if (ok) copy.setAttribute("data-copied", "");
				window.setTimeout(() => {
					copy.removeAttribute("data-copied");
					say("Copy");
				}, COPIED_MS);
			});
			return;
		}

		const link = target.closest<HTMLAnchorElement>("a[href]");
		if (!link || link.target === "_blank") return;
		if (
			event.metaKey ||
			event.ctrlKey ||
			event.shiftKey ||
			event.altKey ||
			event.button !== 0
		)
			return;
		const href = link.getAttribute("href") ?? "";
		if (href.startsWith("#")) {
			event.preventDefault();
			const id = decodeURIComponent(href.slice(1));
			ref.current
				?.querySelector<HTMLElement>(`[id="${CSS.escape(id)}"]`)
				?.scrollIntoView({ block: "start" });
			onNavigate(href);
		} else if (href === "/docs" || href.startsWith("/docs/")) {
			event.preventDefault();
			onNavigate(href);
		}
	};

	return (
		// biome-ignore lint/a11y/useKeyWithClickEvents: delegated click for links and copy buttons that are themselves focusable controls
		// biome-ignore lint/a11y/noStaticElementInteractions: see above
		<div
			ref={ref}
			className="docs-prose"
			onClick={onClick}
			// biome-ignore lint/security/noDangerouslySetInnerHtml: compiled at build time from repository markdown; raw HTML is escaped by the compiler
			dangerouslySetInnerHTML={{ __html: html }}
		/>
	);
}
