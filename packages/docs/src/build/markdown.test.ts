import { describe, expect, test } from "bun:test";
import type { LinkContext } from "./links";
import { compileMarkdown, stripHtmlComments } from "./markdown";

const ctx: LinkContext = {
	fromFile: "operations/variables.md",
	pages: new Map([["operations/networking.md", "operations/networking"]]),
	repoEntry: (p) => (p.includes("gone") ? null : "file"),
};

const compile = (body: string, title = "Title") =>
	compileMarkdown(body, { title, ctx });

describe("compileMarkdown", () => {
	test("ids headings, builds the TOC from h2 and h3 and skips the h1", () => {
		const page = compile("# Title\n\n## One\n\n### Sub\n\n## Two\n\n## One\n");
		expect(page.toc).toEqual([
			{ id: "one", text: "One", level: 2 },
			{ id: "sub", text: "Sub", level: 3 },
			{ id: "two", text: "Two", level: 2 },
			{ id: "one-1", text: "One", level: 2 },
		]);
		expect(page.html).toContain(
			'<h2 id="one">One<a class="docs-anchor" href="#one"',
		);
		expect([...page.headingIds]).toContain("title");
	});

	test("adds the title as an h1 when the file has none", () => {
		expect(compile("Just text.", "My page").html).toContain("<h1");
	});

	test("rewrites internal links and marks external ones", () => {
		const page = compile(
			"# T\n\n[a](networking.md#ports) [b](https://example.com) [c](#t)\n",
		);
		expect(page.html).toContain('href="/docs/operations/networking#ports"');
		expect(page.html).toContain('href="https://example.com"');
		expect(page.html).toContain('rel="noopener noreferrer"');
		expect(page.pageLinks).toEqual([
			{
				href: "/docs/operations/networking#ports",
				slug: "operations/networking",
				anchor: "ports",
			},
		]);
		expect(page.anchorLinks).toEqual(["t"]);
	});

	test("collects broken links as errors", () => {
		expect(compile("# T\n\n[x](gone.md)").errors[0]).toContain("gone.md");
	});

	test("never emits raw HTML", () => {
		const page = compile(
			"# T\n\n<script>alert(1)</script> and <img src=x onerror=alert(1)>\n\n<div>x</div>\n",
		);
		expect(page.html).not.toContain("<script");
		expect(page.html).not.toContain("<img");
		expect(page.html).toContain("&lt;script&gt;");
	});

	test("highlights code at build time and keeps unlabelled blocks plain", () => {
		const page = compile('# T\n\n```sh\necho "hi" # c\n```\n\n```\n<b>\n```\n');
		expect(page.html).toContain("hljs-string");
		expect(page.html).toContain("data-docs-copy");
		expect(page.html).toContain("&lt;b&gt;");
	});

	test("turns [!NOTE]-style blockquotes into callouts, plain quotes stay quotes", () => {
		const page = compile(
			"# T\n\n> [!WARNING]\n> Back up the key.\n\n> plain quote\n",
		);
		expect(page.html).toContain('class="docs-callout" data-tone="warning"');
		expect(page.html).toContain("Warning");
		expect(page.html).toContain("Back up the key.");
		expect(page.html).not.toContain("[!WARNING]");
		expect(page.html).toContain("<blockquote>");
	});

	test("wraps tables in a labelled, focusable scroll frame", () => {
		const html = compile("# T\n\n| a | b |\n|---|---|\n| 1 | 2 |\n").html;
		expect(html).toContain(
			'<div class="docs-table-wrap" role="region" aria-label="Table: a, b" tabindex="0">',
		);
	});

	test("tags a column of sentences as text and a column of short values as fit", () => {
		const html = compile(
			[
				"# T",
				"",
				"| Operation | Endpoint | What it does |",
				"|---|---|---|",
				"| `getUptime` | `GET /services/{serviceId}/uptime?range=1h` | Returns the state, since when, the last check and the latency of the service. |",
				"| `putUptime` | `PUT /services/{serviceId}/uptime-monitor` | Replaces the monitor. |",
				"",
			].join("\n"),
		).html;
		// Sentences: every cell of the column, header included, is tagged.
		expect(html).toContain('<th class="docs-cell-text">What it does</th>');
		expect(html.match(/<td class="docs-cell-text">/g)).toHaveLength(2);
		// Short code values stay on one line; a long endpoint is left to wrap.
		expect(html.match(/<td class="docs-cell-fit">/g)).toHaveLength(2);
		expect(html).toContain("<th>Endpoint</th>");
	});

	test("splits the plain text into sections for the search index", () => {
		const page = compile(
			"# T\n\nLead text.\n\n## Syntax\n\nUse `docker run` here.\n",
		);
		expect(page.sections).toEqual([
			{ anchor: "", heading: "", text: "Lead text." },
			{ anchor: "syntax", heading: "Syntax", text: "Use docker run here." },
		]);
	});
});

describe("stripHtmlComments", () => {
	test("removes comments outside fences only", () => {
		const out = stripHtmlComments(
			"a <!-- x --> b\n<!--\nmulti\n-->\n```\n<!-- keep -->\n```\n",
		);
		expect(out).toContain("a  b");
		expect(out).not.toContain("multi");
		expect(out).toContain("<!-- keep -->");
	});
});

describe("links into the repository with no public repository", () => {
	const hidden = (body: string) =>
		compileMarkdown(body, { title: "T", ctx: { ...ctx, repoUrl: null } });

	test("render as a span with the text, not as an anchor", () => {
		const page = hidden(
			"# T\n\nSee [the notice](../../NOTICE) and [Loki](https://github.com/grafana/loki).\n",
		);
		expect(page.errors).toEqual([]);
		expect(page.html).toContain(
			'<span class="docs-unlinked">the notice</span>',
		);
		expect(page.html).toContain('<a href="https://github.com/grafana/loki"');
		expect(page.html).not.toContain("github.com/nebulactrl");
	});

	test("a missing file is still an error", () => {
		expect(hidden("# T\n\n[x](../../gone.md)\n").errors.length).toBe(1);
	});
});
