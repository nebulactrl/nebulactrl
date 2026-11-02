// Test-only: mounts the app's real route tree (guards, loaders, validated
// search, redirects) on a memory history, the way main.tsx mounts it in the
// browser.

import { mock, setDefaultTimeout } from "bun:test";
import { type QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	type AnyRouter,
	createMemoryHistory,
	createRouter,
	isNotFound,
	RouterProvider,
} from "@tanstack/react-router";
import {
	act,
	configure,
	type RenderResult,
	render,
} from "@testing-library/react";
import { MotionConfig } from "motion/react";
import { type ApiStub, stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { testQueryClient } from "#/test/render";

// Vite's docs plugin provides these at build time; outside Vite the docs
// pages get an empty site. They must exist before the route tree is imported.
mock.module("virtual:docs-loaders", () => ({ loaders: {} }));
mock.module("virtual:docs-manifest", () => ({ metas: [], nav: [] }));

/**
 * Whole-app renders mount the shell and answer a dozen requests: give each
 * test and each wait more room than the 5 s / 1 s defaults. Call at the top
 * of a test file.
 */
export function allowSlowRoutes(): void {
	setDefaultTimeout(30_000);
	configure({ asyncUtilTimeout: 8_000 });
}

export const ME = make("MeBody", {
	id: "user-1",
	name: "Ada Admin",
	email: "ada@example.com",
	createdAt: "2026-01-01T00:00:00Z",
	instanceAdmin: true,
	workspaceId: "ws-1",
	principal: { type: "user", label: "ada", role: "admin", scopes: [] },
});

/** Every call the shell makes around a page, answered with an empty list. */
export const SHELL_ROUTES = {
	"GET /setup": {
		body: make("SetupStatusOutputBody", {
			needsSetup: false,
			tokenRequired: false,
		}),
	},
	"GET /me": { body: ME },
	"GET /projects": { body: make("PageProject", { items: [] }) },
	"GET /servers": { body: make("PageServer", { items: [] }) },
	"GET /system": { body: make("SystemInfo", { version: "1.2.3" }) },
	"GET /me/security": { body: make("SecurityBody") },
	"GET /teams": { body: { items: [] } },
	// The live-updates feed: open and silent.
	"GET /events": () =>
		new Response(new ReadableStream(), {
			headers: { "Content-Type": "text/event-stream" },
		}),
};

/** A control plane that is set up, with a signed-in user and nothing else. */
export function signedInApi(
	extra: Parameters<typeof stubApi>[0] = {},
): ApiStub {
	return stubApi({ ...SHELL_ROUTES, ...extra });
}

async function createAppRouter(
	path: string,
	queryClient: QueryClient,
): Promise<AnyRouter> {
	const { routeTree } = await import("#/routeTree.gen");
	return createRouter({
		routeTree,
		context: { queryClient },
		history: createMemoryHistory({ initialEntries: [path] }),
	});
}

/** Where the router ended up, with the search string: `/login?returnTo=%2Fx`. */
export function hrefOf(router: AnyRouter): string {
	return router.state.location.href;
}

/**
 * What a route's `validateSearch` made of the address: only what it accepted,
 * without the parents' raw values the router merges into `match.search`.
 */
export function validatedSearch(
	router: AnyRouter,
	routeId: string,
): Record<string, unknown> {
	const match = router.state.matches.find((m) => m.routeId === routeId);
	// `_strictSearch` is where the router keeps a route's own validated search.
	const strict: unknown = Reflect.get(match ?? {}, "_strictSearch");
	return typeof strict === "object" && strict !== null
		? Object.fromEntries(Object.entries(strict))
		: {};
}

/** Whether the page the router ended on is the app's not-found page. */
export function isNotFoundPage(router: AnyRouter): boolean {
	return router.state.matches.some((m) => isNotFound(m.error));
}

/** Loads `path` without drawing it: guards and loaders run, redirects are followed. */
export async function loadRoute(
	path: string,
	queryClient = testQueryClient(),
): Promise<AnyRouter> {
	const router = await createAppRouter(path, queryClient);
	await act(async () => {
		await router.load();
	});
	return router;
}

/** Draws the app at `path`, as the browser would after navigating there. */
export async function renderApp(
	path: string,
	queryClient = testQueryClient(),
): Promise<RenderResult & { router: AnyRouter; queryClient: QueryClient }> {
	const router = await createAppRouter(path, queryClient);
	let result: RenderResult | undefined;
	await act(async () => {
		await router.load();
		result = render(
			<MotionConfig reducedMotion="always">
				<QueryClientProvider client={queryClient}>
					<RouterProvider router={router} />
				</QueryClientProvider>
			</MotionConfig>,
		);
	});
	if (!result) throw new Error("renderApp: nothing rendered");
	return { ...result, router, queryClient };
}
