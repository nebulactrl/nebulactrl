import type { TocEntry } from "@nebulactrl/docs/types";
import { useEffect, useState } from "react";

/** A heading counts as read once its top has passed this far below the top of the window (the docs bar is 56px). */
const READ_LINE = 120;

/**
 * "On this page": the outline of the current docs page, with the heading being
 * read marked (`aria-current="location"` and a lighter ink). The server renders
 * every entry unmarked and the island follows the scroll, so the list is plain
 * anchors without a script.
 */
export default function DocsToc({ entries }: { entries: TocEntry[] }) {
	const [active, setActive] = useState("");

	useEffect(() => {
		const headings = entries
			.map((entry) => document.getElementById(entry.id))
			.filter((el): el is HTMLElement => el !== null);
		let frame = 0;
		const update = () => {
			frame = 0;
			let current = "";
			for (const heading of headings) {
				if (heading.getBoundingClientRect().top > READ_LINE) break;
				current = heading.id;
			}
			setActive(current);
		};
		const schedule = () => {
			if (!frame) frame = requestAnimationFrame(update);
		};
		update();
		addEventListener("scroll", schedule, { passive: true });
		addEventListener("resize", schedule);
		return () => {
			cancelAnimationFrame(frame);
			removeEventListener("scroll", schedule);
			removeEventListener("resize", schedule);
		};
	}, [entries]);

	return (
		<nav
			aria-label="On this page"
			className="sticky top-[72px] max-h-[calc(100vh-72px)] overflow-y-auto px-2 py-12"
		>
			<h2 className="type-micro mb-3">On this page</h2>
			<ul className="m-0 flex list-none flex-col gap-1 border-line-faint border-l p-0">
				{entries.map((entry) => {
					const on = entry.id === active;
					return (
						<li key={entry.id}>
							<a
								href={`#${entry.id}`}
								aria-current={on ? "location" : undefined}
								className={`block break-words text-xs no-underline ${on ? "font-semibold text-ink-1" : "text-ink-3 hover:text-ink-1"} ${entry.level === 3 ? "pl-6" : "pl-3"}`}
							>
								{entry.text}
							</a>
						</li>
					);
				})}
			</ul>
		</nav>
	);
}
