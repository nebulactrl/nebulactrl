import fs from "node:fs";
import path from "node:path";
import { withOrigin } from "@nebulactrl/docs/build";
import { allRoutes, routePath } from "../../src/routes.ts";
import { repoRoot, type SiteData } from "../../src/server/site-data.ts";
import type { Facts } from "../../src/site.ts";
import type { SiteConfig } from "./config.ts";

/** Numbers the copy quotes, counted from the repository so the page cannot drift from it. */
export function readFacts(data: SiteData): Facts {
	const catalog = path.join(repoRoot, "templates", "catalog");
	const templates = fs.existsSync(catalog)
		? fs.readdirSync(catalog).filter((name) => name.endsWith(".yaml")).length
		: 0;
	const docsPages = [...data.corpus.entries.values()].filter(
		(entry) => entry.meta.kind !== "api-tag",
	).length;
	return { templates, docsPages };
}

/** A generated file: where it goes below the site root, its type and its bytes. */
export interface Output {
	file: string;
	type: string;
	body: string | Uint8Array;
}

const MD = "text/markdown; charset=utf-8";
const TXT = "text/plain; charset=utf-8";

/**
 * Every non-HTML file the site serves, from the same corpus as the in-app docs.
 * `llms.txt`, `llms-full.txt` and `docs/<slug>.md` carry absolute `SITE_URL` URLs.
 */
export function textOutputs(data: SiteData, config: SiteConfig): Output[] {
	const out: Output[] = [
		{
			file: "llms.txt",
			type: TXT,
			body: withOrigin(data.llms.index, config.siteUrl),
		},
		{
			file: "llms-full.txt",
			type: TXT,
			body: withOrigin(data.llms.full, config.siteUrl),
		},
		{
			file: "search.json",
			type: "application/json; charset=utf-8",
			body: JSON.stringify(data.search),
		},
		{ file: "robots.txt", type: TXT, body: robots(config) },
		{
			file: "sitemap.xml",
			type: "application/xml; charset=utf-8",
			body: sitemap(data, config),
		},
	];
	for (const [slug, markdown] of data.llms.pages) {
		out.push({
			file: `docs/${slug}.md`,
			type: MD,
			body: withOrigin(markdown, config.siteUrl),
		});
	}
	// llms.txt links the OpenAPI document at the control plane's own path; the site hosts a static copy there.
	if (data.openapi)
		out.push({
			file: "api/v1/openapi.json",
			type: "application/json; charset=utf-8",
			body: data.openapi,
		});
	// llms.txt also links the nebula.toml JSON Schema; editors fetch it from here too.
	// The control plane serves the same bytes at the same path, with its own origin.
	if (data.schema)
		out.push({
			file: "schema/nebula.toml.json",
			type: "application/schema+json",
			body: withOrigin(data.schema, config.siteUrl),
		});
	return out;
}

function robots(config: SiteConfig): string {
	return `User-agent: *\nAllow: /\n\nSitemap: ${config.siteUrl}/sitemap.xml\n`;
}

export function sitemap(data: SiteData, config: SiteConfig): string {
	const urls = allRoutes(data).map((route) => {
		const p = routePath(route);
		return `  <url><loc>${config.siteUrl}${p === "/" ? "/" : p}</loc></url>`;
	});
	return `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${urls.join("\n")}\n</urlset>\n`;
}
