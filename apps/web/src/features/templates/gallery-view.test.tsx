import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Dialog, DialogContent } from "#/components/ui/dialog";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { GalleryView } from "./gallery-view";
import { templatesActions, useTemplatesState } from "./store";
import { resetTemplatesStore, summary } from "./test-fixtures";

afterEach(() => act(() => resetTemplatesStore()));

const catalog = {
	categories: [
		{ id: "analytics", label: "Analytics", count: 2 },
		{ id: "cms", label: "CMS", count: 1 },
		{ id: "chat", label: "Chat", count: 0 },
	],
	items: [
		summary({
			id: "umami",
			name: "Umami",
			summary: "Web analytics",
			category: "analytics",
			license: "MIT",
			licenseClass: "permissive",
		}),
		summary({
			id: "plausible",
			name: "Plausible",
			summary: "Simple stats",
			category: "analytics",
			license: "AGPL-3.0",
			licenseClass: "copyleft",
		}),
		summary({
			id: "ghost",
			name: "Ghost",
			summary: "Publishing",
			category: "cms",
			license: "BUSL-1.1",
			licenseClass: "source-available",
			tags: ["blog"],
		}),
	],
};

function Probe() {
	const { templateId } = useTemplatesState();
	return <output aria-label="selected">{templateId ?? "none"}</output>;
}

const renderGallery = () =>
	renderWithProviders(
		<>
			<Dialog open>
				<DialogContent>
					<GalleryView />
				</DialogContent>
			</Dialog>
			<Probe />
		</>,
	);

const cardNames = () =>
	screen
		.getAllByRole("listitem")
		.map((card) => within(card).getByRole("button").getAttribute("aria-label"));

describe("GalleryView", () => {
	test("lists every template once the catalog loads, with the count in the search placeholder", async () => {
		const api = stubApi({ "GET /catalog/templates": { body: catalog } });
		await renderGallery();
		expect(await screen.findByText("Umami")).toBeInTheDocument();
		expect(screen.getByText("Plausible")).toBeInTheDocument();
		expect(screen.getByText("Ghost")).toBeInTheDocument();
		expect(screen.getByLabelText("Search templates")).toHaveAttribute(
			"placeholder",
			"Search 3 apps, tags, databases, licences",
		);
		expect(api.unmatched).toEqual([]);
	});

	test("shows no result grid while the catalog is loading", async () => {
		stubApi({ "GET /catalog/templates": () => new Promise(() => {}) });
		await renderGallery();
		expect(screen.getByText("New from template")).toBeInTheDocument();
		expect(screen.queryByRole("listitem")).not.toBeInTheDocument();
		expect(
			screen.queryByRole("group", { name: "Category" }),
		).not.toBeInTheDocument();
	});

	test("category chips count what is in each category; a category without templates is disabled", async () => {
		stubApi({ "GET /catalog/templates": { body: catalog } });
		await renderGallery();
		await screen.findByText("Umami");
		const group = screen.getByRole("group", { name: "Category" });
		expect(within(group).getByRole("button", { name: "All 3" })).toBeEnabled();
		expect(
			within(group).getByRole("button", { name: "Analytics 2" }),
		).toBeEnabled();
		expect(within(group).getByRole("button", { name: "CMS 1" })).toBeEnabled();
		expect(
			within(group).getByRole("button", { name: "Chat 0" }),
		).toBeDisabled();
	});

	test("choosing a category narrows the grid and presses its chip; All brings everything back", async () => {
		const user = userEvent.setup();
		stubApi({ "GET /catalog/templates": { body: catalog } });
		await renderGallery();
		await screen.findByText("Umami");
		await user.click(screen.getByRole("button", { name: "CMS 1" }));
		expect(cardNames()).toEqual(["Ghost: Publishing"]);
		expect(screen.getByRole("button", { name: "CMS 1" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "All 3" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
		await user.click(screen.getByRole("button", { name: "All 3" }));
		expect(cardNames()).toHaveLength(3);
	});

	test("typing in the search filters the grid by name and updates the chip counts", async () => {
		const user = userEvent.setup();
		stubApi({ "GET /catalog/templates": { body: catalog } });
		await renderGallery();
		await screen.findByText("Umami");
		await user.type(screen.getByLabelText("Search templates"), "blog");
		expect(cardNames()).toEqual(["Ghost: Publishing"]);
		expect(screen.getByRole("button", { name: "All 1" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Analytics 0" })).toBeDisabled();
	});

	test("a search with no matches names the query and offers a way out", async () => {
		const user = userEvent.setup();
		stubApi({ "GET /catalog/templates": { body: catalog } });
		await renderGallery();
		await screen.findByText("Umami");
		await user.type(screen.getByLabelText("Search templates"), "zzz");
		expect(
			await screen.findByText("Nothing matches “zzz”"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/clear the category and licence filters/),
		).toBeInTheDocument();
		expect(screen.queryByRole("listitem")).not.toBeInTheDocument();
	});

	test("an empty catalog says there are no templates", async () => {
		stubApi({
			"GET /catalog/templates": { body: { categories: [], items: [] } },
		});
		await renderGallery();
		expect(await screen.findByText("No templates here")).toBeInTheDocument();
	});

	test("a catalog without lists is treated as empty", async () => {
		stubApi({
			"GET /catalog/templates": { body: { categories: null, items: null } },
		});
		await renderGallery();
		expect(await screen.findByText("No templates here")).toBeInTheDocument();
	});

	test("licence chips filter the grid and pressing the active one again clears it", async () => {
		const user = userEvent.setup();
		stubApi({ "GET /catalog/templates": { body: catalog } });
		await renderGallery();
		await screen.findByText("Umami");
		const group = screen.getByRole("group", { name: "Licence" });
		expect(
			within(group).getByRole("button", { name: "OSI-approved 2" }),
		).toBeEnabled();
		expect(
			within(group).getByRole("button", { name: "Proprietary 0" }),
		).toBeDisabled();
		await user.click(
			within(group).getByRole("button", { name: "Source-available 1" }),
		);
		expect(cardNames()).toEqual(["Ghost: Publishing"]);
		await user.click(
			within(group).getByRole("button", { name: "Source-available 1" }),
		);
		expect(cardNames()).toHaveLength(3);
	});

	test("an active filter that leaves nothing stays pressable so it can be cleared", async () => {
		const user = userEvent.setup();
		stubApi({ "GET /catalog/templates": { body: catalog } });
		await renderGallery();
		await screen.findByText("Umami");
		await user.click(
			screen.getByRole("button", { name: "Source-available 1" }),
		);
		await user.type(screen.getByLabelText("Search templates"), "umami");
		const chip = screen.getByRole("button", { name: "Source-available 0" });
		expect(chip).toBeEnabled();
		expect(chip).toHaveAttribute("aria-pressed", "true");
	});

	test("opening a card selects that template", async () => {
		const user = userEvent.setup();
		stubApi({ "GET /catalog/templates": { body: catalog } });
		await renderGallery();
		await user.click(
			await screen.findByRole("button", { name: "Plausible: Simple stats" }),
		);
		expect(screen.getByLabelText("selected")).toHaveTextContent("plausible");
	});

	test("filters set before the dialog mounted are applied from the store", async () => {
		stubApi({ "GET /catalog/templates": { body: catalog } });
		act(() => {
			templatesActions.setCategory("analytics");
			templatesActions.setQuery("stats");
		});
		await renderGallery();
		await screen.findByRole("button", { name: "Plausible: Simple stats" });
		expect(cardNames()).toEqual(["Plausible: Simple stats"]);
		expect(screen.getByLabelText("Search templates")).toHaveValue("stats");
	});

	test("a failed load says so, keeps the grid away and recovers on retry", async () => {
		const user = userEvent.setup();
		let attempts = 0;
		stubApi({
			"GET /catalog/templates": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{
								status: 500,
								title: "x",
								detail: "catalog down",
								code: "internal",
							},
							{ status: 500 },
						)
					: Response.json(catalog);
			},
		});
		await renderGallery();
		expect(
			await screen.findByText("Templates did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/catalog down Nothing was changed\./),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByText("Umami")).toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.queryByText("Templates did not load"),
			).not.toBeInTheDocument(),
		);
	});
});
