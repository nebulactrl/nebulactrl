import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	createMemoryHistory,
	createRootRoute,
	createRoute,
	createRouter,
	Outlet,
	RouterProvider,
} from "@tanstack/react-router";
import { act, type RenderResult, render } from "@testing-library/react";
import { MotionConfig } from "motion/react";
import type { ReactNode } from "react";
import { ConfirmProvider } from "#/components/common/confirm-dialog";
import { Toaster } from "#/components/ui/toast";
import { TooltipProvider } from "#/components/ui/tooltip";

export interface RenderOptions {
	/** The URL the page opens at. */
	path?: string;
	/**
	 * The route pattern the component is mounted under, for components that read
	 * params (`useParams`). Defaults to a splat that matches any path.
	 */
	routePath?: string;
	/** Seed or inspect cached queries; a fresh client per render otherwise. */
	queryClient?: QueryClient;
}

/** A query client for tests: no retries, so a failed call shows its error at once. */
export function testQueryClient(): QueryClient {
	return new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
}

/**
 * Renders `ui` inside the providers the app's root gives every page (query
 * client, tooltips, toasts, confirm dialog), under a memory router at `path`,
 * with motion off. Resolves once the router has rendered the page.
 */
export async function renderWithProviders(
	ui: ReactNode,
	{
		path = "/",
		routePath = "$",
		queryClient = testQueryClient(),
	}: RenderOptions = {},
): Promise<RenderResult & { queryClient: QueryClient }> {
	const rootRoute = createRootRoute({
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
	const page = createRoute({
		getParentRoute: () => rootRoute,
		path: routePath,
		component: () => ui,
	});
	const router = createRouter({
		routeTree: rootRoute.addChildren([page]),
		history: createMemoryHistory({ initialEntries: [path] }),
	});
	let result: RenderResult | undefined;
	await act(async () => {
		await router.load();
		result = render(
			<QueryClientProvider client={queryClient}>
				<RouterProvider router={router} />
			</QueryClientProvider>,
		);
	});
	if (!result) throw new Error("renderWithProviders: nothing rendered");
	return { ...result, queryClient };
}
