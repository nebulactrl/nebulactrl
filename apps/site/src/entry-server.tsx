import { renderToString } from "react-dom/server";
import { ComparePage } from "./pages/compare.tsx";
import { DocPageView, DocsHomeView } from "./pages/docs.tsx";
import { FaqPage } from "./pages/faq.tsx";
import { Landing } from "./pages/landing.tsx";
import { LicencePage, NotFoundPage, PrivacyPage } from "./pages/legal.tsx";
import type { Route } from "./routes.ts";
import type { SiteData } from "./server/site-data.ts";
import type { RenderContext } from "./site.ts";

/** One route to a complete HTML document. Islands are plain markup here; entry-client wakes them. */
export function renderPage(
	route: Route,
	ctx: RenderContext,
	data: SiteData,
): string {
	return `<!doctype html>${renderToString(view(route, ctx, data))}`;
}

function view(route: Route, ctx: RenderContext, data: SiteData) {
	switch (route.kind) {
		case "landing":
			return <Landing ctx={ctx} />;
		case "compare":
			return <ComparePage ctx={ctx} />;
		case "faq":
			return <FaqPage ctx={ctx} />;
		case "privacy":
			return <PrivacyPage ctx={ctx} />;
		case "licence":
			return <LicencePage ctx={ctx} data={data} />;
		case "docs-home":
			return <DocsHomeView ctx={ctx} data={data} />;
		case "doc":
			return <DocPageView ctx={ctx} data={data} slug={route.slug} />;
		case "not-found":
			return <NotFoundPage ctx={ctx} />;
	}
}
