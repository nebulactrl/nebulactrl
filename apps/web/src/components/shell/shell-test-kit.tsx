// Test-only helpers for the shell's tests: a router whose params the shell
// reads (`/p/$projectId/$envSlug`), canned API fixtures, and a controllable
// matchMedia. Not imported by the app.
import { QueryClientProvider } from "@tanstack/react-query";
import {
	createMemoryHistory,
	createRootRoute,
	createRoute,
	createRouter,
	RouterProvider,
} from "@tanstack/react-router";
import { act, type RenderResult, render } from "@testing-library/react";
import { MotionConfig } from "motion/react";
import type { ReactNode } from "react";
import { ConfirmProvider } from "#/components/common/confirm-dialog";
import { Toaster } from "#/components/ui/toast";
import { TooltipProvider } from "#/components/ui/tooltip";
import { type ApiRoutes, type ApiStub, stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { testQueryClient } from "#/test/render";

export const ME = {
	id: "u1",
	name: "Ada Lovelace",
	email: "ada@example.com",
	createdAt: "2026-01-01T00:00:00Z",
	instanceAdmin: false,
	workspaceId: "w1",
	principal: { type: "user", label: "ada", role: "owner", scopes: [] },
};

export const ADMIN = { ...ME, instanceAdmin: true };

const TEAM = {
	id: "t1",
	name: "Acme",
	slug: "acme",
	current: true,
	role: "owner",
	memberCount: 3,
	createdAt: "2026-01-01T00:00:00Z",
};

function project(id: string, name: string, slug = name.toLowerCase()) {
	return {
		id,
		name,
		slug,
		description: "",
		system: false,
		buildLocation: "inherit",
		buildServerId: "",
		registryId: "",
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
	};
}

export function environment(
	projectId: string,
	slug: string,
	extra: Record<string, unknown> = {},
) {
	return {
		id: `${projectId}-${slug}`,
		projectId,
		slug,
		name: slug,
		kind: "standard",
		isProduction: false,
		parentEnvironmentId: null,
		prNumber: null,
		prTitle: "",
		createdAt: "2026-01-01T00:00:00Z",
		...extra,
	};
}

const PROJECTS = [project("p1", "Shop"), project("p2", "Blog")];
const P1_ENVS = [
	environment("p1", "staging"),
	environment("p1", "production", { isProduction: true, name: "Production" }),
];

/** An open /events feed that has said hello and then stays quiet. */
function eventsFeed(): Response {
	const encoder = new TextEncoder();
	return new Response(
		new ReadableStream({
			start(controller) {
				controller.enqueue(encoder.encode('data: {"type":"hello"}\n\n'));
			},
		}),
		{ headers: { "Content-Type": "text/event-stream" } },
	);
}

const list = (items: unknown[]) => ({ status: 200, body: { items } });

/** The calls every shell screen makes; `overrides` replace or add routes. */
export function shellApi(overrides: ApiRoutes = {}): ApiStub {
	return stubApi({
		"GET /events": eventsFeed,
		"GET /me": { body: ME },
		"GET /teams": list([TEAM]),
		"GET /projects": list(PROJECTS),
		"GET /projects/:id/environments": (request) => {
			const id = new URL(request.url).pathname.split("/")[4] ?? "";
			const items = id === "p1" ? P1_ENVS : [environment(id, "production")];
			return Response.json({ items });
		},
		"GET /projects/:id/previews": list([]),
		"GET /servers": list([]),
		"GET /instance": {
			body: make("InstanceSettings", {
				allowTeamCreation: false,
				canManage: false,
			}),
		},
		"GET /system": {
			body: make("SystemInfo", { version: "v1.2.3", mcpEnabled: false }),
		},
		"GET /environments/:id/deployments": list([]),
		...overrides,
	});
}

/**
 * Renders `ui` at the root of a memory router that knows the console's
 * project URLs, so `useParams` sees `projectId` and `envSlug` and every
 * navigation keeps `ui` mounted. `router.state.location` tells where it went.
 */
export async function renderAt(
	ui: ReactNode,
	{
		path = "/",
		queryClient = testQueryClient(),
	}: { path?: string; queryClient?: ReturnType<typeof testQueryClient> } = {},
) {
	const rootRoute = createRootRoute({
		component: () => (
			<MotionConfig reducedMotion="always">
				<TooltipProvider>
					<Toaster>
						<ConfirmProvider>{ui}</ConfirmProvider>
					</Toaster>
				</TooltipProvider>
			</MotionConfig>
		),
	});
	const routes = [
		"$",
		"/p/$projectId/settings",
		"/p/$projectId/$envSlug/$",
	].map((routePath) =>
		createRoute({ getParentRoute: () => rootRoute, path: routePath }),
	);
	const router = createRouter({
		routeTree: rootRoute.addChildren(routes),
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
	if (!result) throw new Error("renderAt: nothing rendered");
	return { ...result, router, queryClient };
}

/** "/servers?enroll=true" style location of the router, for assertions. */
export function where(router: {
	state: { location: { pathname: string; searchStr: string } };
}) {
	const { pathname, searchStr } = router.state.location;
	return `${pathname}${searchStr}`;
}

/**
 * Replaces matchMedia with one the test controls: `set(query, matches)` flips
 * a query and tells its listeners. Returns the restore.
 */
export function stubMedia(initial: Record<string, boolean> = {}) {
	const real = window.matchMedia;
	// Tests run with reduced motion (see src/test/setup.ts); keep it unless asked.
	const state = new Map(
		Object.entries({ "(prefers-reduced-motion: reduce)": true, ...initial }),
	);
	const listeners = new Map<string, Set<() => void>>();
	window.matchMedia = ((query: string) => {
		const set = listeners.get(query) ?? new Set<() => void>();
		listeners.set(query, set);
		return {
			get matches() {
				return state.get(query) ?? false;
			},
			media: query,
			onchange: null,
			addEventListener: (_type: string, fn: () => void) => set.add(fn),
			removeEventListener: (_type: string, fn: () => void) => set.delete(fn),
			addListener: () => {},
			removeListener: () => {},
			dispatchEvent: () => false,
		};
	}) as unknown as typeof window.matchMedia;
	return {
		set(query: string, matches: boolean) {
			state.set(query, matches);
			act(() => {
				for (const fn of listeners.get(query) ?? []) fn();
			});
		},
		restore() {
			window.matchMedia = real;
		},
	};
}
