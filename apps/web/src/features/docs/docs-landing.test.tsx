import { afterAll, beforeAll, describe, expect, spyOn, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { renderWithProviders } from "#/test/render";
import { installDocsFixtures, restoreEmptyDocs } from "./docs-fixtures";
import type * as LandingModule from "./docs-landing";

let DocsLanding: typeof LandingModule.DocsLanding;

beforeAll(async () => {
	await installDocsFixtures();
	({ DocsLanding } = await import("./docs-landing"));
});
afterAll(restoreEmptyDocs);

function sectionOf(title: string) {
	return screen
		.getByRole("heading", { level: 2, name: title })
		.closest("section") as HTMLElement;
}

describe("DocsLanding", () => {
	test("introduces the docs and titles the tab", async () => {
		await renderWithProviders(<DocsLanding />);
		expect(
			screen.getByRole("heading", {
				level: 1,
				name: "NebulaCtrl documentation",
			}),
		).toBeInTheDocument();
		await waitFor(() => expect(document.title).toBe("Docs · NebulaCtrl"));
	});

	test("offers the first three pages of the first section as numbered steps", async () => {
		await renderWithProviders(<DocsLanding />);
		const steps = screen
			.getAllByRole("link")
			.filter((link) => /^Step \d/.test(link.textContent ?? ""));
		expect(steps.map((link) => link.textContent)).toEqual([
			"Step 1OverviewAbout Overview.",
			"Step 2ConceptsAbout Concepts.",
			"Step 3First deployAbout First deploy.",
		]);
		expect(steps.map((link) => link.getAttribute("href"))).toEqual([
			"/docs/getting-started/overview",
			"/docs/getting-started/concepts",
			"/docs/getting-started/first-deploy",
		]);
	});

	test("lists every section with its blurb and pages", async () => {
		await renderWithProviders(<DocsLanding />);
		const started = sectionOf("Getting started");
		expect(
			within(started).getByText(
				"What NebulaCtrl is, the ideas it is built on, and your first deploy.",
			),
		).toBeInTheDocument();
		expect(
			within(started)
				.getAllByRole("link")
				.map((link) => link.textContent),
		).toEqual(["Overview", "Concepts", "First deploy", "Next steps"]);
		const reference = sectionOf("Reference");
		expect(
			within(reference).getByText("Every CLI command and API endpoint."),
		).toBeInTheDocument();
		expect(
			within(reference).getByRole("link", { name: "CLI" }),
		).toHaveAttribute("href", "/docs/reference/cli");
	});

	test("shows six pages of a long section and counts the rest", async () => {
		await renderWithProviders(<DocsLanding />);
		const applications = sectionOf("Applications");
		expect(within(applications).getAllByRole("link")).toHaveLength(6);
		expect(
			within(applications).queryByRole("link", { name: "App page 7" }),
		).toBeNull();
		expect(
			within(applications).getByText("and 2 more in the sidebar"),
		).toBeInTheDocument();
		expect(
			within(sectionOf("Getting started")).queryByText(/more in the sidebar/),
		).toBeNull();
	});

	test("points AI agents at llms.txt, the full text and the OpenAPI document", async () => {
		await renderWithProviders(<DocsLanding />);
		const agents = screen.getByRole("region", { name: "For AI agents" });
		expect(
			within(agents).getByText(`${window.location.origin}/llms.txt`),
		).toBeInTheDocument();
		expect(
			within(agents).getByRole("link", { name: "llms-full.txt" }),
		).toHaveAttribute("href", "/llms-full.txt");
		expect(
			within(agents).getByRole("link", { name: "OpenAPI document" }),
		).toHaveAttribute("href", "/api/v1/openapi.json");
	});

	test("copies the llms.txt address", async () => {
		const writeText = spyOn(
			navigator.clipboard,
			"writeText",
		).mockResolvedValue();
		await renderWithProviders(<DocsLanding />);
		const agents = screen.getByRole("region", { name: "For AI agents" });
		fireEvent.click(within(agents).getByRole("button", { name: /Copy/ }));
		await waitFor(() =>
			expect(writeText).toHaveBeenCalledWith(
				`${window.location.origin}/llms.txt`,
			),
		);
		writeText.mockRestore();
	});

	test("without a public site URL there is no 'latest docs' note", async () => {
		await renderWithProviders(<DocsLanding />);
		expect(screen.queryByText(/Latest at/)).toBeNull();
	});
});
