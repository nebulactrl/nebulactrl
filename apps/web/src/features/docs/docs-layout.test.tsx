import { afterAll, beforeAll, describe, expect, mock, test } from "bun:test";
import type { SearchData } from "@nebulactrl/docs/types";
import { QueryClientProvider } from "@tanstack/react-query";
import {
	createMemoryHistory,
	createRootRouteWithContext,
	createRoute,
	createRouter,
	Outlet,
	RouterProvider,
	useLocation,
} from "@tanstack/react-router";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MotionConfig } from "motion/react";
import { ConfirmProvider } from "#/components/common/confirm-dialog";
import { Toaster } from "#/components/ui/toast";
import { TooltipProvider } from "#/components/ui/tooltip";
import { testQueryClient } from "#/test/render";
import { installDocsFixtures, restoreEmptyDocs } from "./docs-fixtures";
import type * as LayoutModule from "./docs-layout";
import { useDocsScroll } from "./scroll-context";

const SEARCH: SearchData = {
	pages: [["getting-started/concepts", "Concepts", "Getting started"]],
	rows: [[0, "", "", "The mental model: projects, services and zebra stripes"]],
};

let DocsLayout: typeof LayoutModule.DocsLayout;

beforeAll(async () => {
	await installDocsFixtures();
	mock.module("virtual:docs-search", () => ({ default: SEARCH }));
	({ DocsLayout } = await import("./docs-layout"));
});
afterAll(async () => {
	mock.module("virtual:docs-search", () => ({
		default: { pages: [], rows: [] },
	}));
	await restoreEmptyDocs();
});

function PageProbe() {
	const scroll = useDocsScroll();
	const location = useLocation();
	return (
		<p>{`page ${location.pathname}; scroll root: ${scroll?.id ?? "none"}`}</p>
	);
}

/** The docs layout mounted like the app does: under /docs, with the session's user in the root context. */
async function renderLayout(path: string, user: { name: string } | null) {
	const root = createRootRouteWithContext<{ user: { name: string } | null }>()({
		component: () => (
			<MotionConfig reducedMotion="always">
				<TooltipProvider>
					<Toaster>
						<ConfirmProvider>
							<Outlet />
						</ConfirmProvider>
					</Toaster>
				</TooltipProvider>
			</MotionConfig>
		),
	});
	const docs = createRoute({
		getParentRoute: () => root,
		path: "/docs",
		component: DocsLayout,
	});
	const index = createRoute({
		getParentRoute: () => docs,
		path: "/",
		component: PageProbe,
	});
	const splat = createRoute({
		getParentRoute: () => docs,
		path: "$",
		component: PageProbe,
	});
	const router = createRouter({
		routeTree: root.addChildren([docs.addChildren([index, splat])]),
		history: createMemoryHistory({ initialEntries: [path] }),
		context: { user },
	});
	await act(async () => {
		await router.load();
		render(
			<QueryClientProvider client={testQueryClient()}>
				<RouterProvider router={router} />
			</QueryClientProvider>,
		);
	});
	return router;
}

describe("DocsLayout frame", () => {
	test("offers sign-in when nobody is signed in", async () => {
		await renderLayout("/docs", null);
		expect(screen.getByRole("button", { name: "Sign in" })).toHaveAttribute(
			"href",
			"/login",
		);
		expect(
			screen.queryByRole("button", { name: "Back to console" }),
		).toBeNull();
	});

	test("offers a way back to the console when a user is signed in", async () => {
		await renderLayout("/docs", { name: "Ada" });
		expect(
			screen.getByRole("button", { name: "Back to console" }),
		).toHaveAttribute("href", "/");
		expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
	});

	test("links the logo to the docs home and a skip link to the main content", async () => {
		await renderLayout("/docs/getting-started/concepts", null);
		expect(
			screen.getByRole("link", { name: /NebulaCtrl Docs/ }),
		).toHaveAttribute("href", "/docs");
		expect(
			screen.getByRole("link", { name: "Skip to content" }),
		).toHaveAttribute("href", "#docs-main");
	});

	test("draws the page inside the scrolling main area and tells it that element", async () => {
		await renderLayout("/docs/getting-started/concepts", null);
		const content = await screen.findByText(
			"page /docs/getting-started/concepts; scroll root: docs-main",
		);
		expect(content.closest("#docs-main")).toHaveAttribute("data-docs-scroll");
	});

	test("marks the page in the URL as current in the sidebar, trailing slash or not", async () => {
		await renderLayout("/docs/reference/cli/", null);
		const nav = screen.getAllByRole("navigation", {
			name: "Documentation",
		})[0] as HTMLElement;
		expect(within(nav).getByRole("link", { name: "CLI" })).toHaveAttribute(
			"aria-current",
			"page",
		);
	});

	test("on the docs home no page is current", async () => {
		await renderLayout("/docs", null);
		const nav = screen.getByRole("navigation", { name: "Documentation" });
		expect(
			within(nav)
				.getAllByRole("link")
				.filter((link) => link.hasAttribute("aria-current")),
		).toEqual([]);
	});
});

describe("DocsLayout menu drawer", () => {
	test("opens the sidebar in a drawer that closes when a page is chosen", async () => {
		const router = await renderLayout("/docs", null);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Open documentation menu" }));
		const drawer = await screen.findByRole("dialog");
		await userEvent
			.setup()
			.click(within(drawer).getByRole("link", { name: "Concepts" }));
		await waitFor(() =>
			expect(router.state.location.pathname).toBe(
				"/docs/getting-started/concepts",
			),
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});
});

describe("DocsLayout search", () => {
	async function openSearch() {
		await userEvent.setup().click(
			screen.getAllByRole("button", {
				name: "Search the docs",
			})[0] as HTMLElement,
		);
		return screen.findByRole("combobox");
	}

	test("opens a docs-only palette that starts from the first section's pages", async () => {
		await renderLayout("/docs", null);
		const box = await openSearch();
		expect(box).toHaveAttribute("placeholder", "Search the docs…");
		const options = await screen.findAllByRole("option");
		expect(options.map((option) => option.textContent)).toEqual([
			expect.stringContaining("Overview"),
			expect.stringContaining("Concepts"),
			expect.stringContaining("First deploy"),
			expect.stringContaining("Next steps"),
		]);
	});

	test("choosing a suggested page navigates to it", async () => {
		const router = await renderLayout("/docs", null);
		await openSearch();
		fireEvent.click(
			await screen.findByRole("option", { name: /First deploy/ }),
		);
		await waitFor(() =>
			expect(router.state.location.pathname).toBe(
				"/docs/getting-started/first-deploy",
			),
		);
	});

	test("typing searches the docs and lets you open a hit", async () => {
		const router = await renderLayout("/docs", null);
		const box = await openSearch();
		await userEvent.setup().type(box, "zebra");
		const hit = await screen.findByRole("option", { name: /Concepts/ });
		expect(hit).toHaveTextContent("Getting started");
		fireEvent.click(hit);
		await waitFor(() =>
			expect(router.state.location.pathname).toBe(
				"/docs/getting-started/concepts",
			),
		);
	});

	test("says so when no page mentions the query", async () => {
		await renderLayout("/docs", null);
		const box = await openSearch();
		await userEvent.setup().type(box, "qwertyuiop");
		expect(await screen.findByText(/Nothing matches/)).toHaveTextContent(
			"qwertyuiop",
		);
		expect(screen.queryAllByRole("option")).toEqual([]);
	});
});
