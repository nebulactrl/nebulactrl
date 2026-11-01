import {
	createIndex,
	type SearchIndex,
	searchDocs,
	unpackSearch,
} from "@nebulactrl/docs/search";
import { docHref } from "@nebulactrl/docs/types";
import { useRouter } from "@tanstack/react-router";
import { BookOpenIcon, HashIcon } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import type { CommandItem } from "#/components/motion/command-palette";

/** Pages that explain how NebulaCtrl was built, not how to use it. */
export function isInternalSlug(slug: string): boolean {
	return slug.startsWith("decisions/") || slug.startsWith("architecture/");
}

// The docs search index is its own chunk (the compiled text of every page):
// it is fetched the first time a palette opens, never with the main bundle.

let cached: SearchIndex | null = null;
let pending: Promise<SearchIndex> | null = null;

export function loadSearchIndex(): Promise<SearchIndex> {
	if (cached) return Promise.resolve(cached);
	pending ??= import("virtual:docs-search").then((module) => {
		cached = createIndex(unpackSearch(module.default));
		return cached;
	});
	return pending;
}

/**
 * The docs rows for a palette's typed query. Loads the index once `active`
 * turns true and returns a `queryItems` function for the beUI palette.
 */
export function useDocsQueryItems(
	active: boolean,
): (query: string) => CommandItem[] {
	const router = useRouter();
	const [index, setIndex] = useState<SearchIndex | null>(cached);
	useEffect(() => {
		if (!active || index) return;
		let live = true;
		void loadSearchIndex().then((loaded) => {
			if (live) setIndex(loaded);
		});
		return () => {
			live = false;
		};
	}, [active, index]);

	return useCallback(
		(query: string) => {
			if (!index) return [];
			// The records of the decision log and the architecture notes are
			// for contributors: a "Context" heading from an ADR is no answer to
			// a question about the product.
			return searchDocs(index, query, { limit: 12 })
				.filter((hit) => !isInternalSlug(hit.slug))
				.slice(0, 6)
				.map((hit): CommandItem => {
					const href = docHref(hit.slug, hit.anchor);
					const named = hit.heading && hit.heading !== hit.pageTitle;
					return {
						id: `docs:${hit.slug}#${hit.anchor}`,
						// A bare "Summary" names nothing: lead with the page.
						label: named ? `${hit.pageTitle}: ${hit.heading}` : hit.pageTitle,
						hint: hit.section,
						group: "Docs",
						icon: named ? HashIcon : BookOpenIcon,
						onSelect: () => router.history.push(href),
					};
				});
		},
		[index, router],
	);
}
