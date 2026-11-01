import { describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import type { SiteConfig } from "../scripts/lib/config.ts";
import { readFacts } from "../scripts/lib/outputs.ts";
import { NO_JS_SNIPPET } from "./components/chrome.tsx";
import { renderPage } from "./entry-server.tsx";
import { allRoutes } from "./routes.ts";
import { loadSiteData } from "./server/site-data.ts";
import type { Assets, RenderContext } from "./site.ts";

const root = path.resolve(import.meta.dir, "..");
const headers = fs.readFileSync(path.join(root, "public/_headers"), "utf8");

/** The `_headers` file as { pattern -> [name, value][] }. */
function parseHeaders(text: string): Map<string, [string, string][]> {
	const rules = new Map<string, [string, string][]>();
	let current: [string, string][] | null = null;
	for (const line of text.split("\n")) {
		if (line.trim() === "" || line.trimStart().startsWith("#")) continue;
		if (/^\S/.test(line)) {
			current = [];
			rules.set(line.trim(), current);
			continue;
		}
		const at = line.indexOf(":");
		if (!current || at < 1) throw new Error(`bad _headers line: ${line}`);
		current.push([line.slice(0, at).trim(), line.slice(at + 1).trim()]);
	}
	return rules;
}

const rules = parseHeaders(headers);
const header = (pattern: string, name: string) =>
	rules
		.get(pattern)
		?.find(([n]) => n.toLowerCase() === name.toLowerCase())?.[1];

describe("public/_headers", () => {
	test("stays inside the Cloudflare limits", () => {
		expect(rules.size).toBeLessThanOrEqual(100);
		for (const line of headers.split("\n"))
			expect(line.length).toBeLessThanOrEqual(2000);
	});

	test("security headers on every response", () => {
		const csp = header("/*", "Content-Security-Policy") ?? "";
		expect(csp).toContain("default-src 'self'");
		expect(csp).toContain("object-src 'none'");
		expect(csp).toContain("frame-ancestors 'none'");
		expect(csp).not.toContain("'unsafe-eval'");
		expect(header("/*", "Strict-Transport-Security")).toMatch(/max-age=\d+/);
		expect(header("/*", "X-Content-Type-Options")).toBe("nosniff");
		expect(header("/*", "Referrer-Policy")).toBeTruthy();
	});

	test("the CSP allows exactly the inline script the pages carry, by hash", () => {
		const hash = createHash("sha256").update(NO_JS_SNIPPET).digest("base64");
		expect(header("/*", "Content-Security-Policy")).toContain(
			`script-src 'self' 'sha256-${hash}'`,
		);
	});

	test("content types and caching", () => {
		expect(header("/install.sh", "Content-Type")).toBe(
			"text/plain; charset=utf-8",
		);
		expect(header("/install.sh", "Cache-Control")).toMatch(/max-age=\d{1,4}$/);
		expect(header("/releases/*", "Content-Type")).toBe(
			"application/json; charset=utf-8",
		);
		expect(header("/releases/*", "Cache-Control")).toMatch(/max-age=\d{1,4}$/);
		expect(header("/llms*.txt", "Content-Type")).toStartWith("text/plain");
		expect(header("/*.md", "Content-Type")).toStartWith("text/markdown");
		expect(header("/assets/*", "Cache-Control")).toContain("immutable");
		// Set once: a second Cache-Control on /* would be joined with these.
		expect(header("/*", "Cache-Control")).toBeUndefined();
	});
});

describe("pages against the CSP", () => {
	const assets: Assets = {
		clientJs: "/assets/entry.js",
		css: ["/assets/style.css"],
		inlineCss: "/*css*/",
		fonts: ["/assets/font.woff2"],
		dev: false,
	};
	const config: SiteConfig = {
		siteUrl: "https://nebulactrl.com",
		githubUrl: null,
		version: null,
		githubStars: null,
		dev: false,
	};
	const data = loadSiteData(null);
	const ctx: RenderContext = { config, assets, facts: readFacts(data) };
	const pages = [
		...allRoutes(data).map((route) => renderPage(route, ctx, data)),
		renderPage({ kind: "not-found" }, ctx, data),
	];

	test("every inline script is the allowed one or a JSON-LD data block", () => {
		let inline = 0;
		for (const page of pages) {
			for (const m of page.matchAll(
				/<script([^>]*)>([\s\S]*?)<\/script[^>]*>/gi,
			)) {
				const attrs = m[1] ?? "";
				if (/\ssrc=/.test(attrs) || /application\/ld\+json/.test(attrs))
					continue;
				inline++;
				expect(m[2]).toBe(NO_JS_SNIPPET);
			}
		}
		expect(inline).toBe(pages.length);
	});

	test("no page loads a script, stylesheet, font, image or icon from another origin", () => {
		const own = (url: string) =>
			url.startsWith("/") ||
			url.startsWith("data:") ||
			url.startsWith(config.siteUrl);
		for (const page of pages) {
			for (const m of page.matchAll(/<(script|img|source)\b[^>]*>/gi)) {
				for (const a of (m[0] as string).matchAll(
					/\s(?:src|srcset)="([^"]+)"/g,
				))
					for (const url of (a[1] as string)
						.split(",")
						.map((u) => u.trim().split(" ")[0] as string))
						expect(own(url), `${m[1]} ${url}`).toBe(true);
			}
			for (const m of page.matchAll(/<link\b[^>]*>/g)) {
				const tag = m[0] as string;
				if (!/rel="(stylesheet|preload|icon)"/.test(tag)) continue;
				const href = /href="([^"]+)"/.exec(tag)?.[1] ?? "";
				expect(own(href), href).toBe(true);
			}
		}
	});
});

describe("wrangler.jsonc", () => {
	// The file is JSON with `//` comment lines.
	const text = fs
		.readFileSync(path.join(root, "wrangler.jsonc"), "utf8")
		.split("\n")
		.filter((line) => !line.trimStart().startsWith("//"))
		.join("\n");
	const config = JSON.parse(text.replace(/,(\s*[}\]])/g, "$1")) as {
		name: string;
		main?: string;
		compatibility_date: string;
		assets: Record<string, string>;
		routes: { pattern: string; custom_domain?: boolean }[];
		workers_dev?: boolean;
	};

	test("static assets only: no Worker script, built site, 404 page", () => {
		expect(config.name).toBeTruthy();
		expect(config.main).toBeUndefined();
		expect(config.compatibility_date).toMatch(/^\d{4}-\d{2}-\d{2}$/);
		expect(config.assets.directory).toBe("./dist");
		expect(config.assets.not_found_handling).toBe("404-page");
	});

	test("page URLs are served the way the canonical links spell them", () => {
		// Pages are <route>/index.html and every canonical URL has no trailing slash.
		expect(config.assets.html_handling).toBe("drop-trailing-slash");
	});

	test("apex and www are custom domains of this Worker", () => {
		const domains = config.routes
			.filter((r) => r.custom_domain)
			.map((r) => r.pattern);
		expect(domains.sort()).toEqual(["nebulactrl.com", "www.nebulactrl.com"]);
		expect(config.workers_dev).toBe(false);
	});
});
