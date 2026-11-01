import { describe, expect, test } from "bun:test";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import {
	ConfigError,
	readConfig,
	type SiteConfig,
} from "../scripts/lib/config.ts";
import { readFacts, sitemap, textOutputs } from "../scripts/lib/outputs.ts";
import { TOML_LINES } from "./components/console/story.ts";
import { faqItems } from "./content/faq.tsx";
import {
	deployFailRecording,
	deployRecording,
	replayClock,
} from "./content/recordings.ts";
import { renderPage } from "./entry-server.tsx";
import { allRoutes, matchRoute, type Route, routePath } from "./routes.ts";
import { loadSiteData } from "./server/site-data.ts";
import { type Assets, installCommand, type RenderContext } from "./site.ts";
import { decodeEntities, stripTags, visibleText } from "./test-html.ts";

const SITE = "https://example.test";

/** A file of the repository, so a replica's claim is checked against the code it describes. */
const repoFile = (rel: string) =>
	readFileSync(path.resolve(import.meta.dir, "../../..", rel), "utf8");

/** Every Go source of a directory of the repository, tests apart, as one text. */
const repoDir = (rel: string) =>
	readdirSync(path.resolve(import.meta.dir, "../../..", rel))
		.filter((name) => name.endsWith(".go") && !name.endsWith("_test.go"))
		.map((name) => repoFile(`${rel}/${name}`))
		.join("\n");
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

const plain = setup(null);
const withGithub = setup("https://github.example/org/repo");
const html = (route: Route, s = plain) => renderPage(route, s.ctx, s.data);

function hrefs(page: string): string[] {
	return [...page.matchAll(/\shref="([^"]+)"/g)].map((m) =>
		decodeEntities(m[1] as string),
	);
}

describe("configuration", () => {
	test("a production build without SITE_URL fails with a message that says what to do", () => {
		expect(() => readConfig({}, { dev: false })).toThrow(ConfigError);
		expect(() => readConfig({}, { dev: false })).toThrow(
			/SITE_URL is required/,
		);
		expect(() => readConfig({ SITE_URL: "   " }, { dev: false })).toThrow(
			/SITE_URL is required/,
		);
	});

	test("SITE_URL must be a URL; a trailing slash is dropped", () => {
		expect(() =>
			readConfig({ SITE_URL: "nebulactrl.example" }, { dev: false }),
		).toThrow(/not a URL/);
		expect(() =>
			readConfig({ SITE_URL: "ftp://x.test" }, { dev: false }),
		).toThrow(/https/);
		expect(
			readConfig({ SITE_URL: "https://x.test/" }, { dev: false }).siteUrl,
		).toBe("https://x.test");
	});

	test("dev defaults to localhost on the dev port, GITHUB_URL is optional", () => {
		const dev = readConfig({}, { dev: true, devPort: 5174 });
		expect(dev.siteUrl).toBe("http://localhost:5174");
		expect(dev.githubUrl).toBeNull();
		expect(
			readConfig({ GITHUB_URL: "https://github.com/o/r/" }, { dev: true })
				.githubUrl,
		).toBe("https://github.com/o/r");
	});

	test("`bun run build` exits non-zero and explains when SITE_URL is missing", async () => {
		const proc = Bun.spawn(["bun", "run", "scripts/build.ts"], {
			cwd: new URL("..", import.meta.url).pathname,
			env: { ...process.env, SITE_URL: "" },
			stdout: "pipe",
			stderr: "pipe",
		});
		const stderr = await new Response(proc.stderr).text();
		expect(await proc.exited).toBe(1);
		expect(stderr).toContain("SITE_URL is required");
	});
});

describe("generated files carry absolute SITE_URL URLs", () => {
	const files = new Map(
		textOutputs(plain.data, plain.config).map((o) => [o.file, String(o.body)]),
	);

	test("llms.txt, llms-full.txt and every docs/<slug>.md exist", () => {
		expect(files.has("llms.txt")).toBe(true);
		expect(files.has("llms-full.txt")).toBe(true);
		const pages = [...plain.data.corpus.entries.keys()];
		for (const slug of pages) expect(files.has(`docs/${slug}.md`)).toBe(true);
	});

	test("no placeholder origin is left and every site link is absolute", () => {
		for (const [file, body] of files) {
			if (!/\.(txt|md)$/.test(file)) continue;
			expect(body, file).not.toContain("origin.nebulactrl.invalid");
			// A markdown link to a site path ("](/docs/...") would be relative.
			expect(body.match(/\]\(\/[^)]*\)/g) ?? [], file).toEqual([]);
		}
	});

	test("llms.txt links the docs, llms-full.txt and the OpenAPI document under SITE_URL", () => {
		const index = files.get("llms.txt") as string;
		expect(index).toContain(`](${SITE}/docs/getting-started/quickstart.md)`);
		expect(index).toContain(`](${SITE}/llms-full.txt)`);
		expect(index).toContain(`](${SITE}/api/v1/openapi.json)`);
		expect(files.has("api/v1/openapi.json")).toBe(true);
		// The nebula.toml JSON Schema is linked from llms.txt and hosted next to it.
		expect(index).toContain(`](${SITE}/schema/nebula.toml.json)`);
		expect(files.has("schema/nebula.toml.json")).toBe(true);
		expect(files.get("schema/nebula.toml.json") as string).toContain(
			`${SITE}/schema/nebula.toml.json`,
		);
		expect(files.get("llms-full.txt") as string).toContain(
			`Source: ${SITE}/docs/installing/installer`,
		);
	});

	test("every absolute markdown link to the site points at a page that exists", () => {
		const known = new Set([
			"/llms.txt",
			"/llms-full.txt",
			"/api/v1/openapi.json",
			"/schema/nebula.toml.json",
		]);
		for (const slug of plain.data.corpus.entries.keys())
			known.add(`/docs/${slug}.md`);
		for (const slug of plain.data.corpus.entries.keys())
			known.add(`/docs/${slug}`);
		known.add("/docs");
		for (const [file, body] of files) {
			if (!/\.(txt|md)$/.test(file)) continue;
			for (const m of body.matchAll(
				new RegExp(`\\]\\(${SITE.replaceAll(".", "\\.")}(/[^)#\\s]*)`, "g"),
			)) {
				expect(known.has(m[1] as string), `${file}: ${m[1]}`).toBe(true);
			}
		}
	});

	test("the sitemap lists the landing page, the docs and the legal pages", () => {
		const xml = sitemap(plain.data, plain.config);
		expect(xml).toContain(`<loc>${SITE}/</loc>`);
		expect(xml).toContain(`<loc>${SITE}/docs/installing/installer</loc>`);
		expect(xml).toContain(`<loc>${SITE}/privacy</loc>`);
		expect(xml).toContain(`<loc>${SITE}/licence</loc>`);
		expect(xml).not.toContain("/404");
	});
});

describe("the landing page", () => {
	const page = html({ kind: "landing" });

	test("its primary action is the install command for SITE_URL, with a copy button island", () => {
		// The command is split into spans so a phone can wrap it before the URL and the pipe.
		const flat = stripTags(page);
		expect(flat).toContain(installCommand(SITE));
		expect(flat).toContain(
			"curl -fsSL https://example.test/install.sh | sudo bash",
		);
		expect(page).toContain('data-island="copy"');
		expect(page).not.toContain("Install now");
		expect(page).toContain('href="/docs"');
	});

	test("every link into the docs resolves to a page, and every anchor to a heading", () => {
		const links = hrefs(page).filter((h) => h.startsWith("/docs"));
		expect(links.length).toBeGreaterThan(1);
		for (const href of links) {
			const [path, anchor] = href.split("#") as [string, string | undefined];
			const route = matchRoute(path, plain.data);
			expect(route.kind === "not-found", `${href} resolves to no page`).toBe(
				false,
			);
			if (anchor && route.kind === "doc") {
				expect(
					plain.data.corpus.headingIds.get(route.slug)?.has(anchor),
					`${href}: no heading #${anchor}`,
				).toBe(true);
			}
		}
	});

	test("every other internal link reaches a page, a generated file or a section id", () => {
		const generated = new Set([
			"/install.sh",
			"/llms.txt",
			"/llms-full.txt",
			"/api/v1/openapi.json",
			"/schema/nebula.toml.json",
			"/favicon.svg",
			"/favicon.ico",
		]);
		const ids = new Set([...page.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1]));
		for (const href of hrefs(page)) {
			if (/^[a-z]+:/.test(href) || href.startsWith("/docs")) continue;
			if (href.startsWith("#")) {
				expect(ids.has(href.slice(1)), href).toBe(true);
				continue;
			}
			const [path, anchor] = href.split("#") as [string, string | undefined];
			if (path === "/") {
				if (anchor) expect(ids.has(anchor), href).toBe(true);
				continue;
			}
			expect(
				generated.has(path) ||
					matchRoute(path, plain.data).kind !== "not-found",
				href,
			).toBe(true);
		}
	});

	test("the sections come in order: hero, the product, the four numbered sections, the questions, the closing", () => {
		const at = (needle: string) => page.indexOf(needle);
		const order = [
			'id="top-screen"',
			'id="product"',
			'<section id="releases"',
			'<section id="agent"',
			'<section id="control-plane"',
			'<section id="features"',
			'id="questions"',
			'id="install-final"',
		].map(at);
		for (const i of order) expect(i).toBeGreaterThan(-1);
		expect(order).toEqual([...order].sort((a, b) => a - b));
	});

	test("the landing holds the questions as an accordion and not the comparison; /faq and /compare hold the rest", () => {
		expect(page).not.toContain('id="faq"');
		expect(page).not.toContain('id="compare"');
		expect(page).not.toContain("<table");
		expect(page).not.toContain('"@type":"FAQPage"');
		const section = page.slice(page.indexOf('id="questions"'));
		const rows = section.match(/<details class="faq-item/g) ?? [];
		expect(rows.length).toBeGreaterThanOrEqual(5);
		expect(rows.length).toBeLessThanOrEqual(6);
		expect(section).toContain('href="/faq"');
		// Each row is a question the FAQ page asks, in its own words.
		const asked = [...section.matchAll(/<summary[^>]*>([^<]+)</g)].map((m) =>
			decodeEntities(m[1] as string),
		);
		const known = new Set(faqItems().map((item) => item.q));
		for (const q of asked) expect(known.has(q), q).toBe(true);
	});

	test("the footer says it is free and open source under AGPL-3.0-only, with a GitHub link only when GITHUB_URL is set", () => {
		expect(page).toContain(
			"NebulaCtrl is free and open source under AGPL-3.0-only.",
		);
		// No GITHUB_URL: no GitHub link at all, so none is dead.
		expect(page).not.toContain("github.com");
		expect(page).not.toContain("github.example");
		expect(html({ kind: "landing" }, withGithub)).toContain(
			"https://github.example/org/repo",
		);
	});

	test("the JSON-LD names the AGPL as the license", () => {
		expect(page).toContain(
			'"license":"https://www.gnu.org/licenses/agpl-3.0.html"',
		);
	});

	test("it ships a skip link, one h1, a canonical URL and an og:image", () => {
		expect(page).toContain('href="#main"');
		expect(page.match(/<h1[ >]/g)?.length).toBe(1);
		expect(page).toContain(`<link rel="canonical" href="${SITE}/"/>`);
		expect(page).toContain(`content="${SITE}/og.png"`);
		expect(page).toContain('<main id="main"');
	});

	test("it ends with the closing: an invitation, the install command, and links to the FAQ and the comparison", () => {
		// The mark sits above the heading, hidden from readers.
		expect(page).toMatch(
			/<span aria-hidden="true" data-splay-host="[^"]*" class="[^"]*"><svg[\s\S]*?<\/svg><\/span><h2[^>]*>Try it on a server you already have\.<\/h2>/,
		);
		expect(page).toContain("closing-plate");
		expect(page).toContain(
			"Free. No sign-up, no licence key. Uninstalling leaves your containers running.",
		);
		expect(page).toContain('id="install-final"');
		expect(page).toContain('href="/compare"');
		expect(page).toContain('href="/faq"');
	});
});

describe("the hero", () => {
	const page = html({ kind: "landing" });
	const hero = page.slice(
		page.indexOf('id="top-screen"'),
		page.indexOf('id="product"'),
	);
	const flat = (markup: string) =>
		decodeEntities(stripTags(markup, " ")).replace(/\s+/g, " ").trim();

	test("the headline is one h1 in two tones: ink-1, then ink-3 on its own line", () => {
		expect(hero.match(/<h1[ >]/g)).toHaveLength(1);
		expect(hero).toMatch(
			/<h1[^>]*><span class="block text-ink-1[^"]*">Deploy to servers you own\.<\/span> <span class="block text-ink-3">Stay in control\.<\/span><\/h1>/,
		);
		expect(flat(hero.slice(hero.indexOf("<h1")))).toStartWith(
			"Deploy to servers you own. Stay in control.",
		);
	});

	test("the subline leads with the platform and offers the AI agent over MCP or the CLI as an option", () => {
		const text = flat(hero);
		expect(text).toContain("A free deploy platform for your Docker servers");
		expect(text).toContain(
			"Git or image, verified releases, databases and backups",
		);
		expect(text).toContain(
			"Bring your AI agent over MCP or the CLI if you like.",
		);
		// The platform comes first: the hero makes no promise about approvals or diffs.
		expect(text.indexOf("deploy platform")).toBeLessThan(
			text.indexOf("AI agent"),
		);
		expect(text).not.toContain("every change");
		expect(text).not.toContain("diff");
	});

	test("the install well comes before the docs link, with a line on the script and what it changes", () => {
		expect(hero.indexOf("<h1")).toBeLessThan(hero.indexOf('id="install"'));
		expect(hero.indexOf('id="install"')).toBeLessThan(
			hero.indexOf('href="/docs"'),
		);
		expect(hero).toMatch(/<a href="\/docs"[^>]*>Read the docs<\/a>/);
		expect(flat(hero)).toContain("Paste it on a Linux server as root.");
		expect(hrefs(hero)).toContain("/install.sh");
		expect(hrefs(hero)).toContain("/docs/installing/installer");
		expect(hrefs(hero)).toContain("/compare");
	});

	test("the hero holds no console; its one scene is the Eclipse island", () => {
		expect(hero).not.toContain('data-island="console"');
		expect(hero).toContain('data-island="hero-eclipse"');
	});
});

describe("the dashboard under the hero", () => {
	const page = html({ kind: "landing" });
	const section = page.slice(
		page.indexOf('id="product"'),
		page.indexOf('<section id="releases"'),
	);

	test("it is a figure at the container's width right under the hero, with a caption, and it reveals once", () => {
		expect(page.indexOf('id="top-screen"')).toBeLessThan(
			page.indexOf('id="product"'),
		);
		expect(section).toMatch(/<figure[^>]*data-reveal=""/);
		expect(section).toContain("screen-plate");
		// It is a capture, so the replica island is not here.
		expect(section).not.toContain("data-island=");
	});

	test("the picture has a mobile crop first, then 1440 and 2880 wide sources, avif before webp, sized and lazy", () => {
		const sources = [...section.matchAll(/<source [^>]*>/g)].map((m) => m[0]);
		expect(sources).toHaveLength(4);
		expect(sources[0]).toContain('media="(max-width: 767px)"');
		expect(sources[0]).toContain("dashboard-canvas-mobile.avif");
		expect(sources[1]).toContain("dashboard-canvas-mobile.webp");
		expect(sources[2]).toContain('type="image/avif"');
		expect(sources[2]).toContain("dashboard-canvas-1440.avif 1440w");
		expect(sources[2]).toContain("dashboard-canvas-2880.avif 2880w");
		expect(sources[3]).toContain('type="image/webp"');
		expect(sources[3]).toContain("dashboard-canvas-2880.webp 2880w");
		for (const source of sources.slice(2))
			expect(source).toContain('sizes="(min-width: 1280px) 1240px, 100vw"');
		const img = section.match(/<img [^>]*>/)?.[0] ?? "";
		expect(img).toContain('width="1440"');
		expect(img).toContain('height="900"');
		expect(img).toContain('loading="lazy"');
		expect(img).toContain('decoding="async"');
		expect(img).toContain("dashboard-canvas-1440.webp");
		// Every file it names is shipped.
		for (const file of section.match(/\/shots\/[\w.-]+\.(?:avif|webp)/g) ?? [])
			expect(
				existsSync(path.resolve(import.meta.dir, "../public", file.slice(1))),
				file,
			).toBe(true);
	});

	test("its alt text says what the capture shows", () => {
		expect(section).toContain(
			'alt="The NebulaCtrl dashboard: the production canvas of a project called Acme Shop, with the web, api and postgres services live behind the public domain shop.localhost."',
		);
	});

	test("the bottom of the plate dissolves through an ordered dither ramp mask on the whole plate, below the service cards", () => {
		const plate =
			section.match(/<div class="screen-plate" style="([^"]*)"/)?.[1] ?? "";
		expect(plate).toContain("mask-image:url(");
		expect(plate).toContain("data:image/svg+xml");
		expect(plate).toContain("linear-gradient(#000, #000)");
		// The ramp covers only the bottom 14%, under the cards, so no service dissolves.
		expect(plate).toContain("mask-size:12px 14%, 100% 86.5%");
		expect(plate).toContain("mask-repeat:repeat-x, no-repeat");
	});
});

describe("the AI agent section", () => {
	const page = html({ kind: "landing" });
	const markup =
		(page.split('<section id="agent"')[1] ?? "").split("</section>")[0] ?? "";

	const text = decodeEntities(stripTags(markup, " ")).replace(/\s+/g, " ");

	test("it is the second numbered section, after the releases, with the agent and approval replica as one island, hidden from assistive tech, with one sentence for it, and no image", () => {
		expect(page.indexOf('<section id="releases"')).toBeLessThan(
			page.indexOf('<section id="agent"'),
		);
		expect(markup).toContain("Bring your AI agent");
		expect(markup).toContain('data-island="console"');
		expect(markup).not.toContain("<img");
		expect(markup).toContain('aria-hidden="true"');
		expect(markup).toMatch(/<p class="sr-only">[^<]+\.<\/p>/);
		// The replica is not in the hero.
		const hero = page.slice(
			page.indexOf('id="top-screen"'),
			page.indexOf('id="product"'),
		);
		expect(hero).not.toContain('data-island="console"');
	});

	test("the left pane is a generic MCP client; the right is the approval page with the product's own words", () => {
		expect(text).toContain("agent · MCP");
		expect(text).toContain("Approve a production change");
		expect(text).toContain("Deploy web to production (shop / production)");
		expect(text).toContain("Waiting for approval");
		expect(text).toContain("Approved");
		expect(text).toContain("Decline");
		expect(text).toContain("Approve change");
		expect(text).toContain(
			"Rolling update: the new container must pass its health check before the old one stops.",
		);
	});

	test("the tools and commands it shows exist in the code", () => {
		const mcp = repoDir("internal/mcp/composites");
		for (const tool of ["sync_config", "apply_changes"]) {
			expect(text, tool).toContain(tool);
			expect(mcp, tool).toContain(`Name:  "${tool}"`);
		}
		expect(text).toContain("nebulactrl deployments status");
		expect(repoFile("cmd/cli/cmd_deployments.go")).toContain(
			'Use: "status <deployment-id>"',
		);
		expect(text).toContain("confirmation_required");
		expect(repoFile("internal/mcp/mcpkit/mcpkit.go")).toContain(
			'"confirmation_required"',
		);
	});

	test("it is the end of the story: the file written, approved, web Live, three phases done, and no beam or shimmer", () => {
		expect(markup).not.toContain("data-t=");
		expect(markup).not.toContain("data-caret");
		expect(markup.match(/data-line="\d"/g)).toHaveLength(7);
		expect(markup.match(/class="con-caret"/g)).toHaveLength(7);
		const phases = [
			...markup.matchAll(/<div class="con-phase[^"]*">[\s\S]*?<\/div>/g),
		].map((m) => stripTags(m[0], " ").replace(/\s+/g, " ").trim());
		expect(phases).toEqual(["Build", "Release", "Verify"]);
		expect(markup.match(/<i data-s="done"/g)).toHaveLength(3);
		for (const banned of ["beam", "shimmer", "sweep"])
			expect(page.toLowerCase(), banned).not.toContain(banned);
	});

	test("the nebula.toml it writes is on the page whole: seven lines, no `env` table, domains as a table", () => {
		const code = decodeEntities(stripTags(markup));
		for (const line of TOML_LINES) expect(code, line).toContain(line);
		expect(TOML_LINES).toHaveLength(7);
		expect(code).not.toContain("[services.web.env]");
	});

	test("it says where the agent lives and what MCP does: its own client, MCP or the CLI, the admin switches MCP on, no AI in the dashboard, approval over MCP", () => {
		expect(text).toContain("MCP or the CLI");
		expect(text).toContain("instance admin");
		expect(text).toContain("no AI of its own");
		expect(text).toContain("Over MCP");
		expect(hrefs(markup)).toContain("/docs/reference/mcp");
	});
});

describe("the numbered sections", () => {
	const page = html({ kind: "landing" });
	const IDS = ["releases", "agent", "control-plane", "features"];
	/** A section's markup, up to the next numbered section (a section may hold a code well that is itself a <section>). */
	const section = (id: string) => {
		const from = page.indexOf(`<section id="${id}"`);
		const next = [...IDS.slice(IDS.indexOf(id) + 1), "questions"]
			.map((other) => page.indexOf(`id="${other}"`, from + 1))
			.find((at) => at > -1);
		return page.slice(from, next);
	};
	/** A feature card's markup, inside its own opening tag. */
	const card = (id: string) => {
		const after =
			(section("features").split(`<article id="${id}"`)[1] ?? "").split(
				"</article>",
			)[0] ?? "";
		return after.slice(after.indexOf(">") + 1);
	};
	const plain = (markup: string) =>
		decodeEntities(stripTags(markup)).replace(/\s+/g, " ").trim();

	test("four sections in order, each a section head with its number, one h2 and a lead of at most three sentences", () => {
		const found = [
			...page.matchAll(/<section id="([^"]+)" aria-labelledby=/g),
		].map((m) => m[1]);
		expect(found.filter((id) => IDS.includes(id as string))).toEqual(IDS);
		IDS.forEach((id, i) => {
			const markup = section(id);
			expect(markup, id).toContain('class="section-head');
			expect(plain(markup), id).toContain(`0${i + 1}`);
			expect(markup.match(/<h2[ >]/g), id).toHaveLength(1);
			const lead =
				markup.match(
					/<p class="m-0 max-w-\[58ch\][^>]*>([\s\S]*?)<\/p>/,
				)?.[1] ?? "";
			expect(lead, id).not.toBe("");
			expect(
				(plain(lead).match(/\.(\s|$)/g) ?? []).length,
				id,
			).toBeLessThanOrEqual(3);
		});
	});

	test("every release proves itself: the dithered Deployment bar and the recorded step lines, with rolling and recreate both said", () => {
		const markup = section("releases");
		expect(markup).toContain('data-island="deploy-bench"');
		expect(markup).toContain("Deploy web");
		expect(markup).toContain("Healthy release");
		expect(markup).toContain("Failing health check");
		expect(markup.match(/aria-pressed=/g)).toHaveLength(2);
		expect(markup).toContain("segmented-thumb");
		expect(markup).toContain('role="progressbar"');
		expect(markup).toContain("Deployment");
		const pull = deployRecording.events.find(
			(e) => e.step === "pull" && e.status === "started",
		);
		expect(pull?.message).toBeTruthy();
		expect(markup).toContain(pull?.message as string);
		for (const label of ["Pull image", "Health check", "Move the route"])
			expect(markup, label).toContain(label);
		const copy = plain(markup);
		expect(copy).toContain(
			"A rolling deploy starts the new container beside the old one",
		);
		expect(copy).toContain(
			"If the check fails, the old release keeps serving.",
		);
		expect(copy).toContain("Services with volumes recreate instead");
		expect(hrefs(markup)).toContain("/docs/operations/deployments");
	});

	test("the control plane can stop: the terminal runs the documented commands and prints nothing no recording holds", () => {
		const markup = section("control-plane");
		const copy = plain(markup);
		expect(copy).toContain("never stops containers on your servers");
		expect(copy).toContain("reconciles");
		for (const command of [
			"cd /opt/nebulactrl",
			"sudo docker compose stop nebulactrl",
			"sudo docker compose start nebulactrl",
		])
			expect(copy, command).toContain(command);
		// The control plane's own doc runs Compose from that directory, on that service name.
		const doc = repoFile("docs/installing/control-plane.md");
		expect(doc).toContain("cd /opt/nebulactrl");
		expect(doc).toContain("docker compose logs -f nebulactrl");
		// No HTTP status or other output is printed: only commands and comments.
		expect(copy).not.toMatch(/HTTP\/[12]/);
		for (const item of [
			"Every container on your servers",
			"The proxy on each server",
			"The dashboard and deploys",
			"Logs and metrics in the interface",
		])
			expect(copy, item).toContain(item);
		// The FAQ says the same.
		const faq = faqItems().find(
			(item) => item.q === "What happens when the control plane is down?",
		);
		expect(faq).toBeTruthy();
		expect(hrefs(markup)).toContain("/docs/installing/control-plane");
	});

	test("six feature cards of one shape: an engraved replica, a title and one paragraph with a doc link", () => {
		const cards = [
			...section("features").matchAll(/<article id="([^"]+)"/g),
		].map((m) => m[1]);
		expect(cards).toEqual([
			"rollback",
			"servers",
			"databases",
			"domains",
			"uptime",
			"leaving",
		]);
		for (const id of cards) {
			const markup = card(id as string);
			expect(markup, id).toContain("surface-well");
			expect(markup, id).toContain('aria-hidden="true"');
			expect(markup.match(/<h3[ >]/g), id).toHaveLength(1);
			expect(
				hrefs(markup).filter((h) => h.startsWith("/docs/")).length,
				id,
			).toBeGreaterThan(0);
		}
		// No card nests a raised card inside its well.
		for (const id of cards)
			expect(
				(card(id as string).match(/surface-raised/g) ?? []).length,
				id,
			).toBeLessThanOrEqual(id === "servers" ? 4 : 0);
	});

	test("rolling back: the rollback on top and Live, earlier releases with the product's own key", () => {
		const copy = plain(card("rollback"));
		expect(copy).toContain("Rollback to this release");
		expect(copy).toContain("Live");
		expect(copy).toContain("databases and volumes are not rolled back");
		const tab = repoFile(
			"apps/web/src/features/deployments/deployments-tab.tsx",
		);
		expect(tab).toContain("Rollback to this release");
	});

	test("servers dial out: outbound for management and 80 and 443 for a public site, never 'no open ports'", () => {
		const copy = plain(card("servers"));
		expect(copy).toContain("opens the connection");
		expect(copy).toContain("80 and 443");
		expect(copy.toLowerCase()).not.toContain("no open ports");
		expect(copy.toLowerCase()).not.toContain("nothing dials in");
		expect(copy).toContain("Cloudflare Tunnel");
		expect(copy).not.toContain("WebSocket");
	});

	test("databases: the catalog's seven engines, Valkey first and the default, with S3 compatible backups", () => {
		const markup = card("databases");
		const engines = [
			"Valkey",
			"PostgreSQL",
			"MySQL",
			"MariaDB",
			"Redis",
			"MongoDB",
			"ClickHouse",
		];
		const catalog = repoFile("internal/databases/engines/catalog.go");
		let last = -1;
		for (const engine of engines) {
			expect(catalog, engine).toContain(`Label: "${engine}"`);
			const at = markup.indexOf(`>${engine}<`);
			expect(at, engine).toBeGreaterThan(last);
			last = at;
		}
		expect(plain(markup)).toContain("Default");
		expect(plain(markup)).toContain("S3 compatible storage");
	});

	test("domains, uptime and leaving say what their docs and the code say", () => {
		expect(plain(card("domains"))).toContain("Tailnet only");
		expect(plain(card("uptime"))).toContain("every 30 seconds");
		const view = repoFile("internal/statuspublic/view.go");
		expect(plain(card("uptime"))).toContain("Operational");
		expect(view).toContain("Operational");
		// Uninstalling: the real script, and the promise that containers keep running.
		const uninstall = repoFile("deploy/install/uninstall.sh");
		const leaving = plain(card("leaving"));
		expect(leaving).toContain("sudo bash /opt/nebulactrl/uninstall.sh");
		expect(uninstall.length).toBeGreaterThan(0);
		expect(leaving).toContain("leaves your containers running");
	});
});

describe("every route renders", () => {
	test("each page has one title, a description, a main landmark and no placeholder origin", () => {
		for (const route of allRoutes(plain.data)) {
			const page = html(route);
			const where = routePath(route);
			expect(page.startsWith("<!doctype html>"), where).toBe(true);
			expect(page.match(/<title>/g)?.length, where).toBe(1);
			expect(page, where).toContain('name="description"');
			expect(page, where).toContain('id="main"');
			expect(page, where).not.toContain("origin.nebulactrl.invalid");
			expect(page, where).not.toContain("github.com/nebulactrl");
		}
	});

	test("the 404 page is branded and not indexed", () => {
		const page = html({ kind: "not-found" });
		expect(page).toContain("No page at this address");
		expect(page).toContain('content="noindex"');
	});

	test("docs pages link every internal link target that exists", () => {
		const bad: string[] = [];
		for (const route of allRoutes(plain.data)) {
			if (route.kind !== "doc") continue;
			for (const href of hrefs(html(route))) {
				if (!href.startsWith("/docs")) continue;
				const [path] = href.split("#") as [string];
				if (path.endsWith(".md")) continue;
				if (matchRoute(path, plain.data).kind === "not-found")
					bad.push(`${routePath(route)} -> ${href}`);
			}
		}
		expect(bad).toEqual([]);
	});

	test("the privacy page says the site collects nothing; the licence names AGPL-3.0-only", () => {
		expect(html({ kind: "privacy" })).toContain(
			"This site collects nothing about you.",
		);
		const licence = html({ kind: "licence" });
		expect(licence).toContain("AGPL-3.0-only");
	});
});

describe("landing copy rules", () => {
	const page = html({ kind: "landing" });
	/** Visible prose: no scripts, styles, terminals, code or attributes. */
	const text = visibleText(page);

	test("no hyphens inside words (file names, flags and identifiers with digits or dots excepted)", () => {
		// Proper names keep their own spelling: the IP database and template apps; "sign-up" is the product's own phrase.
		const names = new Set(["DB-IP", "Paperless-ngx", "sign-up"]);
		const offenders = (
			text.match(/[A-Za-z]+(?:-[A-Za-z]+)+(?:\.[a-z]+)?/g) ?? []
		).filter((w) => !/[0-9.]/.test(w) && !names.has(w));
		expect(offenders).toEqual([]);
	});

	test("none of the banned words", () => {
		for (const word of [
			"elevate",
			"seamless",
			"unleash",
			"next gen",
			"game changer",
			"delve",
			"tapestry",
			"in the world of",
			"oops",
		]) {
			expect(text.toLowerCase(), word).not.toContain(word);
		}
	});

	test("the landing never says 'failover' or 'HA'", () => {
		expect(text.toLowerCase()).not.toContain("failover");
		expect(text).not.toMatch(/\bHA\b/);
	});

	test("neither the homepage nor its footer names a platform that inspired a feature, and the footer names the licence", () => {
		// The words of the page, replicas and footer included, and every attribute.
		for (const name of ["Railway", "Coolify", "Dokploy", "Nixpacks"])
			expect(page.toLowerCase(), name).not.toContain(name.toLowerCase());
		const footer = page.slice(page.indexOf("<footer"));
		expect(footer).toContain("Compared row by row");
		expect(footer).toContain("with other self hosted deploy platforms.");
		expect(footer).toContain("open source under AGPL-3.0-only");
	});

	test("no exclamation marks, headings in sentence case", () => {
		expect(text).not.toContain("!");
		for (const m of page.matchAll(/<h[123][^>]*>([^<]+)</g)) {
			const words = (m[1] as string).split(" ");
			const shouting = words.filter(
				(w, i) =>
					i > 0 &&
					!/[.?]$/.test(words[i - 1] as string) &&
					/^[A-Z][a-z]+$/.test(w) &&
					![
						"Docker",
						"Compose",
						"Cloudflare",
						"Tailscale",
						"Lynis",
						"Traefik",
						"Git",
					].includes(w),
			);
			expect(shouting, m[1]).toEqual([]);
		}
	});

	test("the recordings hold no secret, private address or machine name", () => {
		const all =
			JSON.stringify(deployRecording) + JSON.stringify(deployFailRecording);
		for (const leak of [
			/192\.168\./,
			/10\.\d+\.\d+\.\d+/,
			/\/Users\//,
			/nc-rec/,
			/POSTGRES_PASSWORD=/,
			/SECRET_KEY=/,
		])
			expect(all, String(leak)).not.toMatch(leak);
	});

	test("the canvas is playable: the island carries the recorded phases, route switch and events", () => {
		expect(page).toContain('data-island="deploy-bench"');
		expect(page).toContain("Deploy web");
		expect(deployRecording.routeSwitchS).toBeGreaterThan(0);
		expect(deployRecording.routeSwitchS).toBeLessThan(
			deployRecording.totalSeconds,
		);
		expect(deployRecording.phases.map((p) => p.id)).toEqual([
			"build",
			"release",
			"verify",
		]);
		// The route moves only after the health check has passed.
		const health = deployRecording.events.find(
			(e) => e.step === "health" && e.status === "succeeded",
		);
		const routeSwitch = deployRecording.routeSwitchS;
		expect(routeSwitch).not.toBeNull();
		expect(health?.t ?? Number.POSITIVE_INFINITY).toBeLessThanOrEqual(
			routeSwitch ?? Number.NEGATIVE_INFINITY,
		);
	});

	test("the bench carries a failing recording whose route never moves, and a replay clock that caps long waits", () => {
		expect(deployFailRecording.outcome).toBe("failed");
		expect(deployFailRecording.routeSwitchS).toBeNull();
		expect(page).toContain("Failing health check");
		expect(page).toContain("Healthy release");
		const clock = replayClock(deployFailRecording, 6);
		expect(clock.replayTotal).toBeLessThan(deployFailRecording.totalSeconds);
		expect(clock.toRecording(0)).toBe(0);
		expect(clock.toRecording(clock.replayTotal)).toBeCloseTo(
			deployFailRecording.totalSeconds,
			3,
		);
		const happy = replayClock(deployRecording, 6);
		expect(happy.replayTotal).toBeCloseTo(deployRecording.totalSeconds, 3);
	});

	test("the nav has an Install key, and the version and star chips appear only when set", () => {
		expect(page).toMatch(/<a href="#install"[^>]*data-install-link/);
		expect(page).not.toContain("v0.1.0");
		const config: SiteConfig = {
			siteUrl: SITE,
			githubUrl: "https://github.example/org/repo",
			version: "v0.1.0",
			githubStars: 1234,
			dev: false,
		};
		const withMeta = renderPage(
			{ kind: "landing" },
			{ ...withGithub.ctx, config },
			withGithub.data,
		);
		expect(withMeta).toContain("v0.1.0");
		expect(withMeta).toContain("1.2k");
		expect(withGithub.config.githubStars).toBeNull();
		expect(html({ kind: "landing" }, withGithub)).not.toContain("1.2k");
	});

	test("the voice is the makers': no sentence about how a claim was verified or what an image is", () => {
		for (const tell of [
			"we searched",
			"own words",
			"not a screenshot",
			"dev stack",
			"parity matrix",
			"an excerpt",
			"real deployment of",
		]) {
			expect(text.toLowerCase(), tell).not.toContain(tell);
		}
	});

	test("the page moves where a story is told (the hero's Eclipse, the product replica, the deploy the visitor starts) and nowhere else", () => {
		const islands = [...page.matchAll(/data-island="([^"]+)"/g)].map(
			(m) => m[1],
		);
		expect(new Set(islands)).toEqual(
			new Set(["copy", "menu", "hero-eclipse", "console", "deploy-bench"]),
		);
	});

	test("the hero's Eclipse is one island with a box from CSS, behind the headline", () => {
		const sky = page.indexOf('data-island="hero-eclipse"');
		expect(sky).toBeGreaterThan(-1);
		expect(page.slice(sky, sky + 400)).toContain("hero-eclipse");
		expect(page.indexOf("<h1")).toBeGreaterThan(sky);
	});
});

describe("tactile keys", () => {
	const page = html({ kind: "landing" });
	const css = readFileSync(
		path.resolve(import.meta.dir, "styles/site.css"),
		"utf8",
	);
	const app = repoFile("apps/web/src/styles.css");
	const utility = (source: string, name: string) =>
		source.slice(
			source.indexOf(`@utility ${name} {`),
			source.indexOf("\n}", source.indexOf(`@utility ${name} {`)),
		);

	test("raised keys stand on their travel edge and sink onto it, on the site as in the app", () => {
		for (const source of [css, app])
			for (const name of ["btn-primary", "btn-secondary"]) {
				const recipe = utility(source, name);
				expect(recipe, name).toContain("0 var(--key-travel) 0 0");
				expect(recipe, name).toContain("translate: 0 var(--key-travel);");
				expect(recipe, name).toContain(
					"--press-scale: var(--key-press-scale);",
				);
			}
		// Ghost controls have no resting depth, so no travel.
		expect(utility(css, "btn-ghost")).not.toContain("--key-travel");
	});

	test("the page's keys are raised: the nav's Install and the hero's Copy are accent keys, the hero's links raised keys", () => {
		expect(page).toMatch(/<a href="#install"[^>]*class="btn-primary/);
		const hero = page.slice(
			page.indexOf('id="top-screen"'),
			page.indexOf('id="product"'),
		);
		expect(hero).toMatch(/<a href="\/compare" class="btn-secondary/);
		expect(hero).not.toContain("btn-ghost");
	});

	test("motion comes only through one LazyMotion key, in islands that hydrate in view", () => {
		const key = readFileSync(
			path.resolve(import.meta.dir, "islands/motion/key.tsx"),
			"utf8",
		);
		expect(key).toContain("<LazyMotion features={loadFeatures} strict>");
		expect(
			readFileSync(
				path.resolve(import.meta.dir, "islands/motion/features.ts"),
				"utf8",
			),
		).toContain("export default domAnimation;");
		expect(key).toContain("<m.button");
		const islands = readdirSync(path.resolve(import.meta.dir, "islands"))
			.filter((name) => name.endsWith(".tsx"))
			.map((name) => ({
				name,
				source: readFileSync(
					path.resolve(import.meta.dir, "islands", name),
					"utf8",
				),
			}));
		for (const { name, source } of islands)
			expect(source, name).not.toContain('from "motion/react"');
		const users = islands
			.filter(({ source }) => source.includes("./motion/key.tsx"))
			.map(({ name }) => name);
		expect(users.sort()).toEqual(["copy-button.tsx", "deploy-bench.tsx"]);
		for (const island of ["copy", "deploy-bench"])
			for (const tag of page.match(
				new RegExp(`<div[^>]*data-island="${island}"[^>]*>`, "g"),
			) ?? [])
				expect(tag, island).toContain('data-hydrate="visible"');
	});

	test("section numbers are engraved labels, never keycaps: only a shortcut looks like a key", () => {
		const heads =
			page.match(/<p class="section-head[^"]*">[\s\S]*?<\/p>/g) ?? [];
		expect(heads.length).toBeGreaterThanOrEqual(5);
		for (const head of heads) {
			expect(head).toContain('class="section-num"');
			expect(head).not.toContain("keycap");
		}
	});

	test("no hardware ornaments: the plates carry no screws", () => {
		expect(page).not.toContain('class="screws"');
		expect(css).not.toContain(".screws");
	});
});
