// Virtual modules produced by the docs Vite plugin (build/plugin.ts). Each is
// its own chunk: the manifest and loaders reach the docs route, a page module
// is fetched only when that page opens, and the search index only when asked.

declare module "virtual:docs-manifest" {
	import type { DocMeta, NavSection } from "@nebulactrl/docs/types";
	export const metas: DocMeta[];
	export const nav: NavSection[];
}

declare module "virtual:docs-loaders" {
	import type { DocPage } from "@nebulactrl/docs/types";
	export const loaders: Record<string, () => Promise<{ default: DocPage }>>;
}

declare module "virtual:docs-search" {
	import type { SearchData } from "@nebulactrl/docs/types";

	const data: SearchData;
	export default data;
}
