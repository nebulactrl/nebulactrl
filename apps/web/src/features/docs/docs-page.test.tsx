import {
	afterAll,
	afterEach,
	beforeAll,
	beforeEach,
	describe,
	expect,
	spyOn,
	test,
} from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { renderWithProviders } from "#/test/render";
import {
	installDocsFixtures,
	loaders,
	pages,
	resetLoaders,
	restoreEmptyDocs,
} from "./docs-fixtures";
import type * as PageModule from "./docs-page";

let DocsPageRoute: typeof PageModule.DocsPageRoute;
let scrolled: string[];
let load = 0;

/** docs-page reads VITE_GITHUB_URL once, when it loads: each call loads its own copy. */
async function loadPage(
	repo: string | undefined,
): Promise<typeof PageModule.DocsPageRoute> {
	const saved = process.env.VITE_GITHUB_URL;
	if (repo === undefined) delete process.env.VITE_GITHUB_URL;
	else process.env.VITE_GITHUB_URL = repo;
	try {
		const module = (await import(
			`./docs-page.tsx?case=${load++}`
		)) as typeof PageModule;
		return module.DocsPageRoute;
	} finally {
		if (saved === undefined) delete process.env.VITE_GITHUB_URL;
		else process.env.VITE_GITHUB_URL = saved;
	}
}

beforeAll(async () => {
	await installDocsFixtures();
	DocsPageRoute = await loadPage(undefined);
});
afterAll(restoreEmptyDocs);

beforeEach(() => {
	resetLoaders();
	scrolled = [];
	spyOn(Element.prototype, "scrollIntoView").mockImplementation(function (
		this: HTMLElement,
	) {
		scrolled.push(this.id);
	});
});
afterEach(() => {
	spyOn(Element.prototype, "scrollIntoView").mockRestore();
});

function openDoc(path: string, Route = DocsPageRoute) {
	return renderWithProviders(<Route />, { path, routePath: "/docs/$" });
}

describe("DocsPageRoute: unknown page", () => {
	test("says which address has no page and links back to the overview", async () => {
		await openDoc("/docs/nowhere/at-all");
		expect(
			screen.getByRole("heading", {
				level: 1,
				name: "No page at /docs/nowhere/at-all",
			}),
		).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "overview" })).toHaveAttribute(
			"href",
			"/docs",
		);
		await waitFor(() =>
			expect(document.title).toBe("Page not found · Docs · NebulaCtrl"),
		);
	});
});

describe("DocsPageRoute: a page", () => {
	test("draws the compiled body under its section, titles the tab and offers the markdown copy", async () => {
		await openDoc("/docs/getting-started/concepts");
		expect(await screen.findByText("Concepts body.")).toBeInTheDocument();
		expect(
			screen.getByText("Getting started", { selector: ".type-micro" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Copy page as Markdown" }),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(document.title).toBe("Concepts · Docs · NebulaCtrl"),
		);
	});

	test("ignores a trailing slash in the address", async () => {
		await openDoc("/docs/getting-started/concepts/");
		expect(await screen.findByText("Concepts body.")).toBeInTheDocument();
	});

	test("shows a loading state while the page chunk is fetched", async () => {
		let arrive: () => void = () => {};
		const concepts = pages["getting-started/concepts"];
		if (!concepts) throw new Error("fixture page concepts is missing");
		loaders["getting-started/concepts"] = () =>
			new Promise((resolve) => {
				arrive = () => resolve({ default: concepts });
			});
		await openDoc("/docs/getting-started/concepts");
		expect(await screen.findByText("Loading page")).toBeInTheDocument();
		expect(screen.queryByText("Concepts body.")).toBeNull();
		arrive();
		expect(await screen.findByText("Concepts body.")).toBeInTheDocument();
		expect(screen.queryByText("Loading page")).toBeNull();
	});

	test("tells the reader to reload when the chunk is gone, and keeps the footer", async () => {
		loaders["getting-started/concepts"] = () =>
			Promise.reject(new Error("404"));
		await openDoc("/docs/getting-started/concepts");
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("This page could not load");
		expect(alert).toHaveTextContent("Reload the page to try again");
		expect(
			screen.getByRole("link", { name: /First deploy/ }),
		).toBeInTheDocument();
	});

	test("lists the page's headings in an outline", async () => {
		await openDoc("/docs/getting-started/overview");
		const outline = await screen.findByRole("navigation", {
			name: "On this page",
		});
		expect(
			within(outline)
				.getAllByRole("link")
				.map((link) => [link.textContent, link.getAttribute("href")]),
		).toEqual([
			["Setup", "#setup"],
			["Deep", "#deep"],
		]);
	});

	test("scrolls to the heading a deep link names", async () => {
		await openDoc("/docs/getting-started/overview#setup");
		await screen.findByText("Setup body.");
		expect(scrolled).toContain("setup");
	});

	test("follows an in-page link by scrolling and an app link by navigating", async () => {
		await openDoc("/docs/getting-started/overview");
		fireEvent.click(await screen.findByRole("link", { name: "jump to setup" }));
		expect(scrolled).toContain("setup");
		expect(screen.getByText("Setup body.")).toBeInTheDocument();

		fireEvent.click(screen.getByRole("link", { name: "Concepts" }));
		expect(await screen.findByText("Concepts body.")).toBeInTheDocument();
		expect(screen.queryByText("Setup body.")).toBeNull();
	});
});

describe("DocsPageRoute: neighbours", () => {
	test("links the previous and the next page in reading order", async () => {
		await openDoc("/docs/getting-started/concepts");
		await screen.findByText("Concepts body.");
		const footer = screen.getByRole("contentinfo");
		const links = within(footer).getAllByRole("link");
		expect(
			links.map((link) => [link.textContent, link.getAttribute("href")]),
		).toEqual([
			["PreviousOverview", "/docs/getting-started/overview"],
			["NextFirst deploy", "/docs/getting-started/first-deploy"],
		]);
	});

	test("the first page has no previous link", async () => {
		await openDoc("/docs/getting-started/overview");
		await screen.findByText("Setup body.");
		const footer = screen.getByRole("contentinfo");
		expect(within(footer).queryByText("Previous")).toBeNull();
		expect(
			within(footer).getByRole("link", { name: /Next.*Concepts/ }),
		).toBeInTheDocument();
	});

	test("the last page has no next link", async () => {
		await openDoc("/docs/internals/virtual");
		await screen.findByText("Virtual body.");
		const footer = screen.getByRole("contentinfo");
		expect(within(footer).queryByText("Next")).toBeNull();
		expect(
			within(footer).getByRole("link", { name: /Previous.*CLI/ }),
		).toBeInTheDocument();
	});

	test("an API tag page sits between its siblings in the reference", async () => {
		await openDoc("/docs/reference/api/projects");
		await screen.findByRole("heading", { name: "Projects API" });
		const footer = screen.getByRole("contentinfo");
		expect(
			within(footer).getByRole("link", { name: /Previous.*API reference/ }),
		).toBeInTheDocument();
		expect(
			within(footer).getByRole("link", { name: /Next.*Servers/ }),
		).toBeInTheDocument();
	});
});

describe("DocsPageRoute: footer note", () => {
	test("a page written by hand links to its source on GitHub when the repository is known", async () => {
		const Route = await loadPage("https://github.example/org/repo//");
		await openDoc("/docs/getting-started/concepts", Route);
		await screen.findByText("Concepts body.");
		expect(
			screen.getByRole("link", { name: "Edit this page on GitHub" }),
		).toHaveAttribute(
			"href",
			"https://github.example/org/repo/edit/main/docs/getting-started/concepts.md",
		);
	});

	test("a generated page says so instead of offering an edit link", async () => {
		const Route = await loadPage("https://github.example/org/repo");
		await openDoc("/docs/reference/cli", Route);
		await screen.findByText("CLI body.");
		expect(
			screen.queryByRole("link", { name: "Edit this page on GitHub" }),
		).toBeNull();
		expect(
			screen.getByText(
				"Generated from the source code (cli/cmd/root.go); do not edit by hand.",
			),
		).toBeInTheDocument();
	});

	test("an API tag page names the OpenAPI spec as its source", async () => {
		const Route = await loadPage("https://github.example/org/repo");
		await openDoc("/docs/reference/api/projects", Route);
		await screen.findByRole("heading", { name: "Projects API" });
		expect(
			screen.getByText(
				"Generated from the OpenAPI spec (openapi/openapi.json).",
			),
		).toBeInTheDocument();
	});

	test("a page with no source file shows no source note", async () => {
		const Route = await loadPage("https://github.example/org/repo");
		await openDoc("/docs/internals/virtual", Route);
		await screen.findByText("Virtual body.");
		expect(screen.queryByText(/Generated from/)).toBeNull();
		expect(screen.queryByRole("link", { name: /Edit this page/ })).toBeNull();
	});
});

describe("DocsPageRoute: API reference", () => {
	test("the reference index lists its tag pages under the body", async () => {
		await openDoc("/docs/reference/api");
		expect(await screen.findByText("Conventions body.")).toBeInTheDocument();
		const card = screen.getByRole("link", { name: /^Projects/ });
		expect(card).toHaveAttribute("href", "/docs/reference/api/projects");
		expect(card).toHaveTextContent("Create and list projects");
		expect(screen.getByRole("link", { name: /^Servers/ })).toHaveAttribute(
			"href",
			"/docs/reference/api/servers",
		);
	});

	test("a tag page draws its operations and outlines them by method and path", async () => {
		await openDoc("/docs/reference/api/projects");
		expect(
			await screen.findByRole("heading", { name: "Projects API" }),
		).toBeInTheDocument();
		expect(screen.getByText("Create a project")).toBeInTheDocument();
		const outline = screen.getByRole("navigation", { name: "On this page" });
		expect(
			within(outline)
				.getAllByRole("link")
				.map((link) => [link.textContent, link.getAttribute("href")]),
		).toEqual([
			["POST /projects", "#op-createProject"],
			["GET /projects", "#op-listProjects"],
		]);
	});

	test("a tag page scrolls to the operation a deep link names", async () => {
		await openDoc("/docs/reference/api/projects#op-createProject");
		await screen.findByRole("heading", { name: "Projects API" });
		await waitFor(() => expect(scrolled).toContain("op-createProject"));
	});
});
