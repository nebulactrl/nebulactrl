import { afterAll, beforeAll, describe, expect, mock, test } from "bun:test";
import type { SearchData } from "@nebulactrl/docs/types";
import { useLocation } from "@tanstack/react-router";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { renderWithProviders } from "#/test/render";
import type * as SourceModule from "./docs-search-source";

const many = Array.from({ length: 8 }, (_, index) => index);

const DATA: SearchData = {
	pages: [
		["operations/variables", "Variables", "Applications"],
		["decisions/0001-go-modular-monolith", "Decision log", "Internals"],
		["architecture/overview", "Architecture", "Internals"],
		...many.map((index): [string, string, string] => [
			`guides/page-${index}`,
			`Guide ${index}`,
			"Guides",
		]),
	],
	rows: [
		[0, "", "", "Set environment values for a service"],
		[0, "secrets", "Secrets", "Mark a value as secret and hide it"],
		[0, "variables", "Variables", "Everything about the variables of an app"],
		[1, "context", "Context", "The secret was stored encrypted"],
		[2, "", "", "How a secret travels through the architecture"],
		...many.map((index): [number, string, string, string] => [
			3 + index,
			"",
			"",
			"A guide that mentions zebra stripes",
		]),
	],
};

const EMPTY: SearchData = { pages: [], rows: [] };

beforeAll(() => {
	mock.module("virtual:docs-search", () => ({ default: DATA }));
});
afterAll(() => {
	mock.module("virtual:docs-search", () => ({ default: EMPTY }));
});

let fresh = 0;
/** A new copy of the module: it keeps the loaded index in module state. */
async function load(): Promise<typeof SourceModule> {
	// The query string makes Bun evaluate the module again, so each test starts without a cached index.
	return (await import(
		`./docs-search-source.ts?case=${fresh++}`
	)) as typeof SourceModule;
}

function Palette({
	source,
	query,
	startActive,
}: {
	source: typeof SourceModule;
	query: string;
	startActive: boolean;
}) {
	const [active, setActive] = useState(startActive);
	const items = source.useDocsQueryItems(active)(query);
	const location = useLocation();
	return (
		<>
			<button type="button" onClick={() => setActive(true)}>
				open palette
			</button>
			<output aria-label="location">{`${location.pathname}${location.hash ? `#${location.hash}` : ""}`}</output>
			<ul>
				{items.map((item) => (
					<li key={item.id}>
						<button
							type="button"
							onClick={() => "onSelect" in item && item.onSelect()}
						>
							{item.label}
						</button>
						<span data-testid={item.id}>
							{[
								item.group,
								item.hint,
								item.icon?.displayName ?? item.icon?.name,
							].join("|")}
						</span>
					</li>
				))}
			</ul>
		</>
	);
}

async function show(query: string, startActive = true) {
	const source = await load();
	await renderWithProviders(
		<Palette source={source} query={query} startActive={startActive} />,
	);
}

function labels() {
	return screen
		.queryAllByRole("listitem")
		.map((item) => item.querySelector("button")?.textContent);
}

describe("useDocsQueryItems", () => {
	test("offers nothing until the palette is opened, then loads the index", async () => {
		await show("secret", false);
		expect(labels()).toEqual([]);
		fireEvent.click(screen.getByRole("button", { name: "open palette" }));
		await waitFor(() => expect(labels().length).toBeGreaterThan(0));
	});

	test("names a heading hit after its page and a page hit by the page alone", async () => {
		await show("secret");
		await waitFor(() => expect(labels()).toContain("Variables: Secrets"));
		const row = screen.getByTestId("docs:operations/variables#secrets");
		expect(row.textContent?.startsWith("Docs|Applications|")).toBe(true);
	});

	test("labels a page's lead with the page title and a different icon than a heading", async () => {
		await show("environment");
		await waitFor(() => expect(labels()).toEqual(["Variables"]));
		const lead = screen.getByTestId("docs:operations/variables#").textContent;
		await show("secret");
		const heading = (
			await screen.findByTestId("docs:operations/variables#secrets")
		).textContent;
		expect(lead?.split("|")[2]).not.toBe(heading?.split("|")[2]);
	});

	test("does not repeat the page title when the heading is the title", async () => {
		await show("everything");
		await waitFor(() => expect(labels()).toEqual(["Variables"]));
		expect(
			screen.getByTestId("docs:operations/variables#variables"),
		).toBeInTheDocument();
	});

	test("keeps the decision log and architecture notes out of the answers", async () => {
		await show("secret");
		await waitFor(() => expect(labels().length).toBeGreaterThan(0));
		expect(labels()).toEqual(["Variables: Secrets"]);
	});

	test("shows at most six rows", async () => {
		await show("zebra");
		await waitFor(() => expect(labels()).toHaveLength(6));
	});

	test("finds nothing for a query no page mentions", async () => {
		await show("qwertyuiop");
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "open palette" }),
			).toBeInTheDocument(),
		);
		expect(labels()).toEqual([]);
	});

	test("selecting a row goes to the page and its anchor in the app", async () => {
		await show("secret");
		fireEvent.click(
			await screen.findByRole("button", { name: "Variables: Secrets" }),
		);
		await waitFor(() =>
			expect(screen.getByLabelText("location")).toHaveTextContent(
				"/docs/operations/variables#secrets",
			),
		);
	});

	test("selecting a page-level row goes to the page with no anchor", async () => {
		await show("environment");
		fireEvent.click(await screen.findByRole("button", { name: "Variables" }));
		await waitFor(() =>
			expect(screen.getByLabelText("location")).toHaveTextContent(
				/^\/docs\/operations\/variables$/,
			),
		);
	});
});
