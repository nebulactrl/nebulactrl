import type { TocEntry } from "@nebulactrl/docs/types";
import { cn } from "cn";
import { useEffect, useState } from "react";

/**
 * "On this page": the h2/h3 outline, with the section being read marked.
 * Reading position comes from an IntersectionObserver over the scroll root,
 * not a scroll handler.
 */
export function DocsToc({
	entries,
	scrollRoot,
}: {
	entries: readonly TocEntry[];
	scrollRoot: HTMLElement | null;
}) {
	const [active, setActive] = useState<string | null>(null);

	useEffect(() => {
		if (!scrollRoot || entries.length === 0) return;
		const visible = new Set<string>();
		const observer = new IntersectionObserver(
			(changes) => {
				for (const change of changes) {
					if (change.isIntersecting) visible.add(change.target.id);
					else visible.delete(change.target.id);
				}
				const first = entries.find((entry) => visible.has(entry.id));
				if (first) setActive(first.id);
			},
			{ root: scrollRoot, rootMargin: "0px 0px -70% 0px" },
		);
		for (const entry of entries) {
			const el = document.getElementById(entry.id);
			if (el) observer.observe(el);
		}
		return () => observer.disconnect();
	}, [entries, scrollRoot]);

	if (entries.length < 2) return null;
	return (
		<nav
			aria-label="On this page"
			className="sticky top-6 max-h-[calc(100svh-52px-48px)] overflow-y-auto"
		>
			<div className="type-micro mb-2">On this page</div>
			<ul className="m-0 flex list-none flex-col gap-px border-line border-l p-0">
				{entries.map((entry) => (
					<li key={entry.id}>
						<a
							href={`#${entry.id}`}
							onClick={(event) => {
								event.preventDefault();
								document
									.getElementById(entry.id)
									?.scrollIntoView({ block: "start" });
								window.history.replaceState(
									window.history.state,
									"",
									`#${entry.id}`,
								);
								setActive(entry.id);
							}}
							aria-current={active === entry.id ? "location" : undefined}
							className={cn(
								"-ml-px block wrap-anywhere border-l py-0.5 text-xs leading-snug no-underline outline-none focus-visible:focus-ring",
								entry.level === 3 ? "pl-5" : "pl-3",
								active === entry.id
									? "border-accent-ink text-ink-1"
									: "border-transparent text-ink-3 hover:text-ink-1",
							)}
						>
							{entry.text}
						</a>
					</li>
				))}
			</ul>
		</nav>
	);
}
