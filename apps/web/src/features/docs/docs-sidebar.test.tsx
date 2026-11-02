import { afterAll, beforeAll, describe, expect, spyOn, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { renderWithProviders } from "#/test/render";
import { installDocsFixtures, restoreEmptyDocs } from "./docs-fixtures";
import type * as SidebarModule from "./docs-sidebar";

let DocsSidebar: typeof SidebarModule.DocsSidebar;

beforeAll(async () => {
	await installDocsFixtures();
	({ DocsSidebar } = await import("./docs-sidebar"));
});
afterAll(restoreEmptyDocs);

function section(title: string) {
	return screen.getByRole("button", { name: title });
}

function Harness() {
	const [current, setCurrent] = useState("getting-started/concepts");
	return (
		<>
			<button type="button" onClick={() => setCurrent("reference/cli")}>
				go to cli
			</button>
			<DocsSidebar current={current} />
		</>
	);
}

describe("DocsSidebar", () => {
	test("lists every section with its pages as links to /docs/<slug>", async () => {
		await renderWithProviders(<DocsSidebar current="" />);
		const nav = screen.getByRole("navigation", { name: "Documentation" });
		expect(
			within(nav)
				.getAllByRole("button")
				.map((button) => button.textContent),
		).toEqual(["Getting started", "Applications", "Reference", "Internals"]);
		const overview = within(nav).getByRole("link", { name: "Overview" });
		expect(overview).toHaveAttribute("href", "/docs/getting-started/overview");
		expect(within(nav).getByRole("link", { name: "CLI" })).toHaveAttribute(
			"href",
			"/docs/reference/cli",
		);
	});

	test("marks only the open page as current", async () => {
		await renderWithProviders(
			<DocsSidebar current="getting-started/concepts" />,
		);
		const current = screen
			.getAllByRole("link")
			.filter((link) => link.getAttribute("aria-current") === "page");
		expect(current.map((link) => link.textContent)).toEqual(["Concepts"]);
	});

	test("shows an API tag page's siblings nested under the reference index only while it is open", async () => {
		const { unmount } = await renderWithProviders(
			<DocsSidebar current="reference/cli" />,
		);
		expect(screen.queryByRole("link", { name: "Projects" })).toBeNull();
		unmount();

		await renderWithProviders(<DocsSidebar current="reference/api/projects" />);
		expect(screen.getByRole("link", { name: "Projects" })).toHaveAttribute(
			"aria-current",
			"page",
		);
		expect(screen.getByRole("link", { name: "Servers" })).toHaveAttribute(
			"href",
			"/docs/reference/api/servers",
		);
	});

	test("opens the nested pages while the index itself is open", async () => {
		await renderWithProviders(<DocsSidebar current="reference/api" />);
		expect(screen.getByRole("link", { name: "API reference" })).toHaveAttribute(
			"aria-current",
			"page",
		);
		expect(screen.getByRole("link", { name: "Servers" })).toBeInTheDocument();
	});

	test("collapses and reopens a section that does not hold the current page", async () => {
		await renderWithProviders(
			<DocsSidebar current="getting-started/overview" />,
		);
		const applications = section("Applications");
		expect(applications).toHaveAttribute("aria-expanded", "true");
		expect(
			screen.getByRole("link", { name: "App page 1" }),
		).toBeInTheDocument();

		await userEvent.setup().click(applications);
		expect(applications).toHaveAttribute("aria-expanded", "false");
		await waitFor(() =>
			expect(screen.queryByRole("link", { name: "App page 1" })).toBeNull(),
		);

		await userEvent.setup().click(applications);
		expect(applications).toHaveAttribute("aria-expanded", "true");
		expect(
			await screen.findByRole("link", { name: "App page 1" }),
		).toBeInTheDocument();
	});

	test("keeps the section of the current page open when its header is clicked", async () => {
		await renderWithProviders(
			<DocsSidebar current="getting-started/overview" />,
		);
		await userEvent.setup().click(section("Getting started"));
		expect(section("Getting started")).toHaveAttribute("aria-expanded", "true");
		expect(screen.getByRole("link", { name: "Overview" })).toBeInTheDocument();
	});

	test("tells the caller when a page link is followed (to close the drawer)", async () => {
		const followed: string[] = [];
		await renderWithProviders(
			<DocsSidebar current="" onNavigate={() => followed.push("go")} />,
		);
		fireEvent.click(screen.getByRole("link", { name: "Concepts" }));
		expect(followed).toEqual(["go"]);
	});

	test("scrolls the current page into view, again when it changes", async () => {
		const seen: (string | null)[] = [];
		const scroll = spyOn(
			Element.prototype,
			"scrollIntoView",
		).mockImplementation(function (this: HTMLElement) {
			seen.push(this.textContent);
		});
		try {
			await renderWithProviders(<Harness />);
			expect(seen).toEqual(["Concepts"]);
			fireEvent.click(screen.getByRole("button", { name: "go to cli" }));
			expect(seen).toEqual(["Concepts", "CLI"]);
		} finally {
			scroll.mockRestore();
		}
	});
});
