import { loaders } from "virtual:docs-loaders";
import { metas, nav } from "virtual:docs-manifest";
import type { DocMeta, DocPage, NavItem } from "@nebulactrl/docs/types";
import { useQuery } from "@tanstack/react-query";

// The runtime face of the build-time docs: metadata is a few KB and always at
// hand once the docs chunk loads; a page's body is its own chunk, fetched on
// demand and cached for the session.

export const docNav = nav;
const bySlug = new Map<string, DocMeta>(metas.map((meta) => [meta.slug, meta]));

export function docMeta(slug: string): DocMeta | undefined {
	return bySlug.get(slug);
}

/** Every page in reading order: section by section, parents before their children. */
export const docOrder: DocMeta[] = (() => {
	const out: DocMeta[] = [];
	const visit = (item: NavItem) => {
		const meta = bySlug.get(item.slug);
		if (meta) out.push(meta);
		for (const child of item.children) visit(child);
	};
	for (const section of nav) for (const item of section.items) visit(item);
	return out;
})();

export function childrenOf(slug: string): DocMeta[] {
	return metas
		.filter((meta) => meta.parent === slug)
		.sort((a, b) => a.order - b.order);
}

function loadDocPage(slug: string): Promise<DocPage> {
	const load = loaders[slug];
	if (!load) return Promise.reject(new Error(`No docs page "${slug}"`));
	return load().then((module) => module.default);
}

export function useDocPage(slug: string) {
	return useQuery({
		queryKey: ["docs-page", slug],
		queryFn: () => loadDocPage(slug),
		staleTime: Number.POSITIVE_INFINITY,
		retry: false,
		refetchInterval: false,
		enabled: bySlug.has(slug),
	});
}
