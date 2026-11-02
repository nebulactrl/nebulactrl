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
import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { renderWithProviders } from "#/test/render";
import {
	installDocsFixtures,
	loaders,
	pages,
	resetLoaders,
	restoreEmptyDocs,
} from "./docs-fixtures";
import type * as HelpSheetModule from "./help-sheet";

let HelpSheet: typeof HelpSheetModule.HelpSheet;
let scrolled: string[];

beforeAll(async () => {
	await installDocsFixtures();
	({ HelpSheet } = await import("./help-sheet"));
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

function open(slug: string, anchor = "") {
	return renderWithProviders(
		<HelpSheet slug={slug} anchor={anchor} open onOpenChange={() => {}} />,
	);
}

function Harness() {
	const [slug, setSlug] = useState("getting-started/overview");
	return (
		<>
			<button type="button" onClick={() => setSlug("reference/cli")}>
				help for cli
			</button>
			<HelpSheet slug={slug} anchor="" open onOpenChange={() => {}} />
		</>
	);
}

describe("HelpSheet", () => {
	test("is not on screen while closed", async () => {
		await renderWithProviders(
			<HelpSheet
				slug="getting-started/overview"
				anchor=""
				open={false}
				onOpenChange={() => {}}
			/>,
		);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("shows the page's title and body with a link to the full page", async () => {
		await open("getting-started/overview", "setup");
		const dialog = await screen.findByRole("dialog");
		expect(
			await within(dialog).findByText("Intro text.", { exact: false }),
		).toBeInTheDocument();
		expect(
			within(dialog).getByRole("heading", { name: "Overview", level: 2 }),
		).toBeInTheDocument();
		const full = within(dialog).getByRole("button", { name: "Open full page" });
		expect(full).toHaveAttribute(
			"href",
			"/docs/getting-started/overview#setup",
		);
		expect(full).toHaveAttribute("target", "_blank");
		expect(dialog).toHaveAccessibleDescription("About Overview.");
		expect(within(dialog).queryByRole("button", { name: "Back" })).toBeNull();
	});

	test("scrolls to the deep-linked heading once the page has loaded", async () => {
		await open("getting-started/overview", "setup");
		await screen.findByText("Setup body.");
		expect(scrolled).toContain("setup");
	});

	test("shows a loading state until the page chunk arrives", async () => {
		let arrive: () => void = () => {};
		const concepts = pages["getting-started/concepts"];
		if (!concepts) throw new Error("fixture page concepts is missing");
		loaders["getting-started/concepts"] = () =>
			new Promise((resolve) => {
				arrive = () => resolve({ default: concepts });
			});
		await open("getting-started/concepts");
		expect(await screen.findByText("Loading page")).toBeInTheDocument();
		arrive();
		expect(await screen.findByText("Concepts body.")).toBeInTheDocument();
		expect(screen.queryByText("Loading page")).toBeNull();
	});

	test("says so when the page cannot be loaded and links to the full page", async () => {
		loaders["getting-started/concepts"] = () =>
			Promise.reject(new Error("gone"));
		await open("getting-started/concepts", "intro");
		expect(
			await screen.findByText(/This page could not load/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "opening the full page" }),
		).toHaveAttribute("href", "/docs/getting-started/concepts#intro");
	});

	test("says a page this version does not have is not part of it", async () => {
		await open("nowhere");
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByText(/not part of this version of the docs/),
		).toBeInTheDocument();
		expect(
			within(dialog).getByRole("link", { name: "Browse the documentation" }),
		).toHaveAttribute("href", "/docs");
		expect(
			within(dialog).getByRole("heading", { name: "Documentation" }),
		).toBeInTheDocument();
		expect(dialog).toHaveAccessibleDescription(
			"A page of the NebulaCtrl documentation.",
		);
	});

	test("sends an API tag page to its own full page instead of rendering it", async () => {
		await open("reference/api/projects");
		expect(
			await screen.findByText(/easier to read on its own page/),
		).toBeInTheDocument();
		for (const role of ["button", "link"] as const) {
			expect(
				screen.getByRole(role, { name: "Open full page" }),
			).toHaveAttribute("href", "/docs/reference/api/projects");
		}
		expect(screen.queryByText("Create a project")).toBeNull();
	});

	test("follows a link to another docs page inside the sheet, and Back returns", async () => {
		await open("getting-started/overview");
		await userEvent
			.setup()
			.click(await screen.findByRole("link", { name: "Concepts" }));
		expect(await screen.findByText("Concepts body.")).toBeInTheDocument();
		expect(screen.queryByText("Setup body.")).toBeNull();
		expect(
			screen.getByRole("button", { name: "Open full page" }),
		).toHaveAttribute("href", "/docs/getting-started/concepts");

		await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));
		expect(await screen.findByText("Setup body.")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
	});

	test("opens a link to a page this version lacks in a new tab and stays put", async () => {
		const opened = spyOn(window, "open").mockImplementation(() => null);
		try {
			await open("getting-started/overview");
			await userEvent
				.setup()
				.click(await screen.findByRole("link", { name: "a gone page" }));
			expect(opened).toHaveBeenCalledWith(
				"/docs/nowhere",
				"_blank",
				"noopener",
			);
			expect(screen.getByText("Setup body.")).toBeInTheDocument();
			expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
		} finally {
			opened.mockRestore();
		}
	});

	test("an in-page anchor does not add to the trail", async () => {
		await open("getting-started/overview");
		fireEvent.click(await screen.findByRole("link", { name: "jump to setup" }));
		expect(scrolled).toContain("setup");
		expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
	});

	test("a link to the docs home that has no page of its own is left to a new tab", async () => {
		loaders["getting-started/concepts"] = () =>
			Promise.resolve({
				default: {
					kind: "md",
					slug: "getting-started/concepts",
					html: '<a href="/docs/">home</a>',
					toc: [],
				},
			});
		const opened = spyOn(window, "open").mockImplementation(() => null);
		try {
			await open("getting-started/concepts");
			fireEvent.click(await screen.findByRole("link", { name: "home" }));
			expect(opened).toHaveBeenCalledWith("/docs/", "_blank", "noopener");
		} finally {
			opened.mockRestore();
		}
	});

	test("being opened for another page starts a new trail there", async () => {
		await renderWithProviders(<Harness />);
		fireEvent.click(await screen.findByRole("link", { name: "Concepts" }));
		expect(await screen.findByText("Concepts body.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Back" })).toBeInTheDocument();

		fireEvent.click(
			screen.getByRole("button", { name: "help for cli", hidden: true }),
		);
		expect(await screen.findByText("CLI body.")).toBeInTheDocument();
		expect(screen.queryByText("Concepts body.")).toBeNull();
		expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
	});
});
