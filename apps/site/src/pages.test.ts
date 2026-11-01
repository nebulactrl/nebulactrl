import { describe, expect, test } from "bun:test";
import type { SiteConfig } from "../scripts/lib/config.ts";
import { readFacts, sitemap } from "../scripts/lib/outputs.ts";
import {
	GROUPS,
	PRODUCTS,
	ROWS,
	SNAPSHOT,
	TOTALS,
	TRAILS,
} from "./content/comparison.ts";
import { faqGroups, faqItems } from "./content/faq.tsx";
import { renderPage } from "./entry-server.tsx";
import { allRoutes, matchRoute, type Route, routePath } from "./routes.ts";
import { loadSiteData } from "./server/site-data.ts";
import type { Assets, RenderContext } from "./site.ts";
import { decodeEntities, stripTags, visibleText } from "./test-html.ts";

const SITE = "https://example.test";
const assets: Assets = {
	clientJs: "/assets/entry.js",
	css: ["/assets/style.css"],
	inlineCss: "/*css*/",
	fonts: [],
	dev: false,
};

function setup(githubUrl: string | null) {
	const config: SiteConfig = {
		siteUrl: SITE,
		githubUrl,
		version: null,
		githubStars: null,
		dev: false,
	};
	const data = loadSiteData(githubUrl);
	const ctx: RenderContext = { config, assets, facts: readFacts(data) };
	return { config, data, ctx };
}

/** Files the build writes next to the pages. */
const GENERATED = [
	"/install.sh",
	"/llms.txt",
	"/llms-full.txt",
	"/api/v1/openapi.json",
	"/schema/nebula.toml.json",
];

const priv = setup(null);
const html = (route: Route) => renderPage(route, priv.ctx, priv.data);

function hrefs(page: string): string[] {
	return [...page.matchAll(/\shref="([^"]+)"/g)].map((m) =>
		decodeEntities(m[1] as string),
	);
}

function jsonLd(page: string): Record<string, unknown>[] {
	return [
		...page.matchAll(
			/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g,
		),
	].map((m) => JSON.parse(m[1] as string));
}

describe("the comparison data", () => {
	test("it holds all 100 rows, numbered 1 to 100 once each, in matrix order", () => {
		expect(ROWS).toHaveLength(100);
		expect(ROWS.map((r) => r.row)).toEqual(
			Array.from({ length: 100 }, (_, i) => String(i + 1)),
		);
		expect(new Set(GROUPS.map((g) => g.id)).size).toBe(GROUPS.length);
		for (const group of GROUPS) expect(group.rows.length).toBeGreaterThan(0);
	});

	test("TOTALS add up to 100 rows per product and match the verified matrix", () => {
		for (const { id } of PRODUCTS) {
			const t = TOTALS[id];
			expect(t.yes + t.partial + t.no + t.na, id).toBe(100);
		}
		expect(TOTALS.nebula).toEqual({ yes: 92, partial: 2, no: 6, na: 0 });
		expect(TOTALS.coolify).toEqual({ yes: 62, partial: 21, no: 15, na: 2 });
	});

	test("TRAILS follows the rule, not a hand picked list", () => {
		expect(TRAILS.map((r) => r.row)).toEqual(
			ROWS.filter((r) => r.nebula.mark !== "yes").map((r) => r.row),
		);
		expect(TRAILS).toHaveLength(
			TOTALS.nebula.partial + TOTALS.nebula.no + TOTALS.nebula.na,
		);
	});

	test("a NebulaCtrl cell that is not yes has a note of at most 8 words; a yes cell has none unless it states a limit", () => {
		// Rows where NebulaCtrl says yes and the note names the limit that comes with it.
		const yesWithLimit: Record<string, true> = { "76": true };
		for (const r of ROWS) {
			const words = (r.nebula.note ?? "").split(/\s+/).filter(Boolean);
			if (r.nebula.mark === "yes" && !yesWithLimit[r.row]) {
				expect(r.nebula.note, r.row).toBeUndefined();
			} else {
				expect(words.length, r.row).toBeGreaterThan(0);
				expect(words.length, r.row).toBeLessThanOrEqual(8);
			}
		}
	});
});

describe("the compare page", () => {
	const page = html({ kind: "compare" });

	test("it routes at /compare, is in allRoutes and in the sitemap", () => {
		expect(routePath({ kind: "compare" })).toBe("/compare");
		expect(matchRoute("/compare/", priv.data)).toEqual({ kind: "compare" });
		expect(allRoutes(priv.data).map(routePath)).toContain("/compare");
		expect(sitemap(priv.data, priv.config)).toContain(
			`<loc>${SITE}/compare</loc>`,
		);
	});

	test("it has one h1, a canonical URL, the target title and a current nav link", () => {
		expect(page.match(/<h1[ >]/g)?.length).toBe(1);
		expect(page).toContain(`<link rel="canonical" href="${SITE}/compare"/>`);
		expect(page).toContain(
			"<title>Coolify alternative: NebulaCtrl compared with Coolify, Dokploy and Railway</title>",
		);
		expect(page).toMatch(/href="\/compare"[^>]*aria-current="page"/);
		expect(page).toContain('id="main"');
	});

	test("it renders all 100 rows in one table with a tbody per group", () => {
		const rows = page.match(/<th scope="row"/g) ?? [];
		expect(rows).toHaveLength(100);
		expect(page.match(/<table[ >]/g)).toHaveLength(1);
		expect(page.match(/<tbody[ >]/g)).toHaveLength(GROUPS.length);
		for (const r of ROWS) expect(page, r.label).toContain(r.label);
		// 100 rows of four cells each
		expect(page.match(/data-mark="/g)).toHaveLength(400);
	});

	test("the table scrolls in a labelled, focusable region with a sticky header and an opaque sticky label column above the cells", () => {
		expect(page).toMatch(
			/<div role="region" aria-label="Capability comparison table" tabindex="0"/,
		);
		const head = page.match(/<thead>[\s\S]*?<\/thead>/)?.[0] ?? "";
		expect(head.match(/sticky top-0/g)).toHaveLength(5);
		const rowHeads = [...page.matchAll(/<th scope="row" class="([^"]*)"/g)].map(
			(m) => m[1] ?? "",
		);
		expect(rowHeads).toHaveLength(100);
		for (const classes of rowHeads) {
			expect(classes).toContain("sticky left-0");
			expect(classes).toContain("z-10");
			expect(classes).toContain("bg-void");
		}
		expect(visibleText(page)).toContain("Scroll sideways to see every product");
	});

	test("it never shows the internal evidence for NebulaCtrl cells", () => {
		expect(page).not.toContain("internal/");
		expect(page).not.toContain(".go");
		expect(page).not.toContain("pkg/");
		expect(page).not.toContain("docs/operations/");
	});

	test("it states the counts from TOTALS and the snapshot date", () => {
		const text = visibleText(page);
		expect(text).toContain("out of 100 capabilities");
		expect(page).toContain("data-dither-fill");
		expect(page.match(/\sdata-bars[\s>=]/g)).toHaveLength(1);
		for (const id of ["nebula", "coolify", "dokploy", "railway"] as const)
			expect(text).toContain(`${TOTALS[id].yes} of 100`);
		expect(text).toContain(SNAPSHOT);
	});

	test("it ships WebPage and BreadcrumbList structured data", () => {
		const types = jsonLd(page).map((d) => d["@type"]);
		expect(types).toEqual(["WebPage", "BreadcrumbList"]);
	});

	test("every internal link resolves to a page, a heading or a docs heading", () => {
		const ids = new Set([...page.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1]));
		for (const href of hrefs(page)) {
			if (/^[a-z]+:/.test(href)) continue;
			if (href.startsWith("#")) {
				expect(ids.has(href.slice(1)), href).toBe(true);
				continue;
			}
			if (GENERATED.includes(href)) continue;
			if (href.startsWith("/assets/")) continue;
			if (href.endsWith(".svg") || href.endsWith(".ico")) continue;
			const [path] = href.split("#") as [string];
			expect(matchRoute(path, priv.data).kind !== "not-found", href).toBe(true);
		}
	});
});

describe("the FAQ page", () => {
	const page = html({ kind: "faq" });
	const items = faqItems();

	test("it routes at /faq, is in allRoutes and in the sitemap", () => {
		expect(routePath({ kind: "faq" })).toBe("/faq");
		expect(matchRoute("/faq", priv.data)).toEqual({ kind: "faq" });
		expect(allRoutes(priv.data).map(routePath)).toContain("/faq");
		expect(sitemap(priv.data, priv.config)).toContain(`<loc>${SITE}/faq</loc>`);
	});

	test("it has one h1, a canonical URL and a current nav link", () => {
		expect(page.match(/<h1[ >]/g)?.length).toBe(1);
		expect(page).toContain(`<link rel="canonical" href="${SITE}/faq"/>`);
		expect(page).toMatch(/href="\/faq"[^>]*aria-current="page"/);
		expect(page).toContain('id="main"');
	});

	test("three groups hold every question once, each as a details element", () => {
		const groups = faqGroups();
		expect(groups.map((g) => g.title)).toEqual([
			"Before you install",
			"Running it",
			"Security and data",
		]);
		expect(new Set(items.map((i) => i.q)).size).toBe(items.length);
		expect(page.match(/<details class="faq-item/g)).toHaveLength(items.length);
		expect(page.match(/<h2 id="[a-z-]+"/g)).toHaveLength(3);
		for (const q of [
			"Can I move to another server?",
			"How do I back up the secret key?",
			"Does it need Docker Compose?",
			"What happens when the control plane is down?",
		])
			expect(items.map((i) => i.q)).toContain(q);
	});

	test("the FAQPage schema has one answer per question, none empty", () => {
		const faq = jsonLd(page).find((d) => d["@type"] === "FAQPage") as {
			mainEntity: { name: string; acceptedAnswer: { text: string } }[];
		};
		expect(faq.mainEntity).toHaveLength(items.length);
		expect(faq.mainEntity.map((e) => e.name)).toEqual(items.map((i) => i.q));
		for (const e of faq.mainEntity)
			expect(e.acceptedAnswer.text.length, e.name).toBeGreaterThan(40);
		expect(jsonLd(page).map((d) => d["@type"])).toContain("BreadcrumbList");
	});

	test("/compare and /faq end with the install command, as the landing page does", () => {
		for (const kind of ["compare", "faq"] as const) {
			const page = html({ kind });
			expect(page, kind).toContain('id="install-again"');
			expect(page, kind).toContain('data-island="copy"');
			expect(stripTags(page), kind).toContain(
				"curl -fsSL https://example.test/install.sh | sudo bash",
			);
		}
	});

	test("every link resolves: docs pages and their anchors, site pages and generated files", () => {
		const docs = hrefs(page).filter((h) => h.startsWith("/docs/"));
		expect(docs.length).toBeGreaterThan(8);
		for (const href of hrefs(page)) {
			if (/^[a-z]+:/.test(href) || href.startsWith("#")) continue;
			if (href.startsWith("/assets/")) continue;
			if (href.endsWith(".svg") || href.endsWith(".ico")) continue;
			const [path, anchor] = href.split("#") as [string, string | undefined];
			if (GENERATED.includes(path)) continue;
			const route = matchRoute(path, priv.data);
			expect(route.kind !== "not-found", `${href} resolves to no page`).toBe(
				true,
			);
			if (anchor && route.kind === "doc")
				expect(
					priv.data.corpus.headingIds.get(route.slug)?.has(anchor),
					`${href}: no heading #${anchor}`,
				).toBe(true);
		}
	});

	test("the Coolify answer links the compare page, not an anchor", () => {
		const text = faqItems().find((i) => i.q.includes("Coolify"));
		expect(text).toBeDefined();
		expect(page).toContain('href="/compare"');
		expect(page).not.toContain('href="#compare"');
	});

	test("it says NebulaCtrl sends no telemetry", () => {
		expect(page).toContain("NebulaCtrl sends no telemetry");
	});
});

describe("copy rules on the compare and FAQ pages", () => {
	for (const kind of ["compare", "faq"] as const) {
		test(`/${kind} has no banned words, no exclamation marks, no hyphenated prose words`, () => {
			const text = visibleText(html({ kind }));
			expect(text).not.toMatch(
				/\b(elevate|seamless|seamlessly|unleash|delve|tapestry)\b/i,
			);
			expect(text).not.toContain("!");
			expect(text).not.toContain("\u2014");
			const hyphenated = text.match(/[A-Za-z]{2,}-[A-Za-z]{2,}/g) ?? [];
			// Technical names and file names are allowed.
			const allowed = [
				"DB-IP",
				"llms-full",
				"DNS-01",
				"AES-256-GCM",
				"sign-up",
			];
			expect(hyphenated.filter((w) => !allowed.includes(w))).toEqual([]);
		});
	}

	test("the footer lists Compare and FAQ on every page", () => {
		for (const route of allRoutes(priv.data).slice(0, 6)) {
			const page = html(route);
			expect(hrefs(page), routePath(route)).toContain("/compare");
			expect(hrefs(page), routePath(route)).toContain("/faq");
		}
	});
});
