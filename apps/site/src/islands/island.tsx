import type { ReactNode } from "react";

export type HydrateMode = "idle" | "visible";

/**
 * The only way a component reaches the browser. The server renders `children`
 * inside this wrapper; entry-client finds `[data-island]`, loads the island
 * module and hydrates it with `data-props`. Everything outside an island is
 * plain HTML with no JavaScript attached.
 */
export function Island({
	name,
	props = {},
	mode = "idle",
	className,
	children,
}: {
	name: IslandName;
	props?: Record<string, unknown>;
	mode?: HydrateMode;
	className?: string;
	children: ReactNode;
}) {
	return (
		<div
			data-island={name}
			data-hydrate={mode}
			data-props={JSON.stringify(props)}
			className={className ?? "contents"}
		>
			{children}
		</div>
	);
}

export type IslandName =
	| "copy"
	| "menu"
	| "block-code"
	| "docs-search"
	| "api-tag"
	| "deploy-bench"
	| "hero-eclipse"
	| "console"
	| "docs-toc"
	| "lost-signal-scene";
