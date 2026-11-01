import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import type { SiteConfig } from "../scripts/lib/config.ts";
import { readFacts } from "../scripts/lib/outputs.ts";
import { renderPage } from "./entry-server.tsx";
import { mergeLines } from "./islands/quiet-lines.ts";
import { allRoutes, type Route } from "./routes.ts";
import { loadSiteData } from "./server/site-data.ts";
import type { Assets, RenderContext } from "./site.ts";

const assets: Assets = {
	clientJs: "/assets/entry.js",
	css: ["/assets/style.css"],
	inlineCss: "/*css*/",
	fonts: [],
	dev: false,
};
const config: SiteConfig = {
	siteUrl: "https://example.test",
	githubUrl: null,
	version: null,
	githubStars: null,
	dev: false,
};
const data = loadSiteData(null);
const ctx: RenderContext = { config, assets, facts: readFacts(data) };
const html = (route: Route) => renderPage(route, ctx, data);

const css = readFileSync(new URL("./styles/site.css", import.meta.url), "utf8");
const doc = (slug: string) => html({ kind: "doc", slug });
const SCENE_ISLANDS = ["lost-signal-scene", "hero-eclipse"];

describe("mergeLines", () => {
	test("a line that a range reports twice is one line, joined sideways", () => {
		const lines = mergeLines([
			{ left: 10, top: 0, right: 200, bottom: 40 },
			{ left: 120, top: 2, right: 300, bottom: 42 },
			{ left: 10, top: 2, right: 200, bottom: 42 },
			{ left: 10, top: 50, right: 90, bottom: 90 },
		]);
		expect(lines).toEqual([
			{ left: 10, top: 0, right: 300, bottom: 42 },
			{ left: 10, top: 50, right: 90, bottom: 90 },
		]);
	});

	test("an empty box is not a line", () => {
		expect(mergeLines([{ left: 5, top: 0, right: 5, bottom: 40 }])).toEqual([]);
	});
});

describe("the Eclipse behind the landing hero", () => {
	const page = html({ kind: "landing" });
	const hero = page.slice(
		page.indexOf('id="top-screen"'),
		page.indexOf('id="product"'),
	);

	test("it is one island in the hero, hydrated in view, inside the hero's scope", () => {
		expect(page.match(/data-island="hero-eclipse"/g)).toHaveLength(1);
		expect(hero).toContain('data-island="hero-eclipse"');
		expect(page).toMatch(/<section id="top-screen" data-ascii-scope/);
		const layer =
			hero.match(/<div[^>]*data-island="hero-eclipse"[^>]*>/)?.[0] ?? "";
		expect(layer).toContain('data-hydrate="visible"');
		expect(layer).toContain("pointer-events-none");
	});

	test("the copy column is the scene's panel and its quiet region, and holds the headline", () => {
		const column = hero.slice(hero.indexOf("data-ascii-panel") - 80);
		expect(column).toMatch(
			/<div[^>]*data-ascii-panel="[^"]*"[^>]*data-ascii-quiet="[^"]*"/,
		);
		expect(column.indexOf("data-ascii-panel")).toBeLessThan(
			column.indexOf("<h1"),
		);
	});

	test("the halftone floor sits under it and stands down once the scene paints", () => {
		expect(hero.indexOf("ht-layer ht-hero")).toBeGreaterThan(-1);
		expect(hero.indexOf("ht-layer ht-hero")).toBeLessThan(
			hero.indexOf('data-island="hero-eclipse"'),
		);
		expect(css).toMatch(
			/\[data-ascii="on"\] > \.ht-hero::before \{\s*opacity: 0;/,
		);
	});

	test("it is the page's only scene island", () => {
		for (const island of SCENE_ISLANDS.filter((n) => n !== "hero-eclipse")) {
			expect(page).not.toContain(`data-island="${island}"`);
		}
	});
});

describe("no other page has a scene", () => {
	test("every page but the landing page and the 404 has none, docs headers included", () => {
		for (const route of allRoutes(data)) {
			if (route.kind === "landing" || route.kind === "not-found") continue;
			const page = html(route);
			for (const island of SCENE_ISLANDS) {
				expect(page, `${JSON.stringify(route)} ${island}`).not.toContain(
					`data-island="${island}"`,
				);
			}
			expect(page, JSON.stringify(route)).not.toContain("data-ascii-scope");
		}
	});

	test("the docs home, Install and Quickstart headers are plain, over the halftone dawn", () => {
		const pages = [
			html({ kind: "docs-home" }),
			doc("installing"),
			doc("getting-started/quickstart"),
		];
		for (const page of pages) {
			expect(page).toContain("ht-dawn");
			expect(page).not.toContain("data-doc-band");
			expect(page.match(/<h1\b/g)).toHaveLength(1);
		}
	});
});

describe("Lost signal on the 404", () => {
	const page = html({ kind: "not-found" });

	test("the scene is one island that hydrates in view", () => {
		const tag = page.match(/<div[^>]*data-island="lost-signal-scene"[^>]*>/);
		expect(tag?.[0]).toContain('data-hydrate="visible"');
	});

	test("the message is real text and the gap opens around it", () => {
		const panel = page.slice(page.indexOf("data-ascii-panel"));
		expect(panel).toContain("No page at this address.");
		expect(panel).toContain("Back to the home page");
		expect(panel).toContain("data-ascii-quiet");
		expect(page).toContain("data-ascii-scope");
	});

	test("the halftone rim stays as the floor and the fallback, and stands down once the scene paints", () => {
		expect(page).toContain("ht-rim");
		expect(css).toContain('.ht-rim[data-ascii="on"]::before');
		expect(css).toMatch(
			/\.ht-rim\[data-ascii="on"\]::before\s*{\s*opacity:\s*0/,
		);
	});
});
