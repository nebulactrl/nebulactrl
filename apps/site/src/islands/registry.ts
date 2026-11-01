import type { ComponentType } from "react";
import type { IslandName } from "./island.tsx";

type Loader = () => Promise<{ default: ComponentType<never> }>;

/** Client side: each island is its own chunk, fetched only when its element is on the page. */
export const loaders: Record<IslandName, Loader> = {
	copy: () => import("./copy-button.tsx"),
	menu: () => import("./menu-toggle.tsx"),
	"block-code": () => import("./block-code.tsx"),
	"docs-search": () => import("./docs-search.tsx"),
	"api-tag": () => import("./api-tag.tsx"),
	"deploy-bench": () => import("./deploy-bench.tsx"),
	"hero-eclipse": () => import("./hero-eclipse.tsx"),
	console: () => import("./console.tsx"),
	"docs-toc": () => import("./docs-toc.tsx"),
	"lost-signal-scene": () => import("./lost-signal-scene.tsx"),
};
