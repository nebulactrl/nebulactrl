import type { SiteData } from "./server/site-data.ts";

/** Every page the site renders. */
export type Route =
	| { kind: "landing" }
	| { kind: "compare" }
	| { kind: "faq" }
	| { kind: "privacy" }
	| { kind: "licence" }
	| { kind: "docs-home" }
	| { kind: "doc"; slug: string }
	| { kind: "not-found" };

export function routePath(route: Route): string {
	switch (route.kind) {
		case "landing":
			return "/";
		case "compare":
			return "/compare";
		case "faq":
			return "/faq";
		case "privacy":
			return "/privacy";
		case "licence":
			return "/licence";
		case "docs-home":
			return "/docs";
		case "doc":
			return `/docs/${route.slug}`;
		case "not-found":
			return "/404";
	}
}

/** Maps a URL path to a route; unknown paths are `not-found`. Used by dev and preview. */
export function matchRoute(pathname: string, data: SiteData): Route {
	const clean = pathname.replace(/\/+$/, "") || "/";
	if (clean === "/") return { kind: "landing" };
	if (clean === "/compare") return { kind: "compare" };
	if (clean === "/faq") return { kind: "faq" };
	if (clean === "/privacy") return { kind: "privacy" };
	if (clean === "/licence") return { kind: "licence" };
	if (clean === "/docs") return { kind: "docs-home" };
	if (clean.startsWith("/docs/")) {
		const slug = clean.slice("/docs/".length);
		if (data.corpus.entries.has(slug)) return { kind: "doc", slug };
	}
	return { kind: "not-found" };
}

/** Every route the production build writes (the 404 page is separate). */
export function allRoutes(data: SiteData): Route[] {
	return [
		{ kind: "landing" },
		{ kind: "compare" },
		{ kind: "faq" },
		{ kind: "docs-home" },
		...[...data.corpus.entries.keys()].map(
			(slug): Route => ({ kind: "doc", slug }),
		),
		{ kind: "privacy" },
		{ kind: "licence" },
	];
}
