import { DitherNotFound } from "@nebulactrl/dither/interface/not-found";
import type { QueryClient } from "@tanstack/react-query";
import {
	createRootRouteWithContext,
	Link,
	Outlet,
	redirect,
	useLocation,
	useRouter,
} from "@tanstack/react-router";
import type { ReactNode } from "react";
import { errorStatus } from "#/api/client-config";
import { getMySecurityOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	meQuery,
	projectsQuery,
	serversQuery,
	setupQuery,
	systemQuery,
} from "#/api/hooks";
import type { Me } from "#/api/models";
import { EnrolmentGate } from "#/components/auth/enrolment-gate";
import { ConfirmProvider } from "#/components/common/confirm-dialog";
import { StatePanel } from "#/components/common/state-panel";
import { AppShell } from "#/components/shell/app-shell";
import { Button } from "#/components/ui/button";
import { Toaster } from "#/components/ui/toast";
import { TooltipProvider } from "#/components/ui/tooltip";
import { ThresholdGate } from "#/features/threshold/threshold-screen";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";

/** The pages of the forgot-password flow: reachable signed out, never in the console frame. */
function isResetPath(path: string): boolean {
	return path === "/forgot-password" || path === "/reset-password";
}

/** The documentation: public, so it never waits on setup or a session. */
function isDocsPath(path: string): boolean {
	return path === "/docs" || path.startsWith("/docs/");
}

interface RouterContext {
	queryClient: QueryClient;
}

export const Route = createRootRouteWithContext<RouterContext>()({
	// The session gate. A fresh install goes to /setup until the first admin
	// exists; after that every page but /login needs a session.
	beforeLoad: async ({ context, location }) => {
		const path = location.pathname;
		if (isDocsPath(path)) {
			// Only to know whether to offer "Back to console" or "Sign in".
			let docsUser: Me | undefined;
			try {
				docsUser = await context.queryClient.ensureQueryData(meQuery());
			} catch {
				// Signed out, or the control plane is unreachable: the docs still open.
			}
			return { user: docsUser };
		}
		const setup = await context.queryClient.ensureQueryData(setupQuery());
		if (setup.needsSetup) {
			if (path !== "/setup") throw redirect({ to: "/setup" });
			return { user: undefined as Me | undefined };
		}
		if (path === "/setup")
			throw redirect({ to: "/login", search: { notice: "already-set-up" } });

		let user: Me | undefined;
		try {
			user = await context.queryClient.ensureQueryData(meQuery());
		} catch (error) {
			if (errorStatus(error) !== 401) throw error;
		}
		if (!user) {
			// Login, invite and password-reset links are reachable signed out.
			if (path === "/login" || path.startsWith("/invite/") || isResetPath(path))
				return { user: undefined as Me | undefined };
			throw redirect({
				to: "/login",
				search: { returnTo: `${location.pathname}${location.searchStr}` },
			});
		}
		if (path === "/login") throw redirect({ to: "/" });
		// The sidebar's counts and version read these on every page. Settling
		// them here means no page ever paints the rail half-filled; a failure
		// only leaves the rail's own fallback copy.
		await Promise.all(
			[
				context.queryClient.ensureQueryData(projectsQuery()),
				context.queryClient.ensureQueryData(serversQuery()),
				context.queryClient.ensureQueryData(systemQuery()),
				context.queryClient.ensureQueryData(getMySecurityOptions()),
			].map((load) => load.catch(() => undefined)),
		);
		return { user: user as Me | undefined };
	},
	component: RootComponent,
	notFoundComponent: NotFoundComponent,
	errorComponent: ErrorComponent,
});

function RootComponent() {
	// Select the user alone: the route context is rebuilt on every navigation
	// (beforeLoad re-runs), so reading all of it re-rendered the whole shell on
	// each `?service=` change. The user object itself is the cached query result.
	const user = Route.useRouteContext({ select: (context) => context.user });
	// The invite and password-reset pages are auth cards, so they
	// never sit inside the console frame, signed in or not.
	const bare = useLocation({
		select: (location) =>
			location.pathname.startsWith("/invite/") ||
			isDocsPath(location.pathname) ||
			isResetPath(location.pathname),
	});
	return (
		<TooltipProvider>
			<Toaster>
				<ConfirmProvider>
					{user && !bare ? (
						<ThresholdGate>
							<AppShell user={user}>
								<EnrolmentGate>
									<Outlet />
								</EnrolmentGate>
							</AppShell>
						</ThresholdGate>
					) : (
						<Outlet />
					)}
				</ConfirmProvider>
			</Toaster>
		</TooltipProvider>
	);
}

function NotFoundComponent() {
	const router = useRouter();
	useDocumentTitle(["Page not found"]);
	return (
		<div className="flex min-h-full items-center justify-center p-4">
			<div className="w-full max-w-[720px]">
				<DitherNotFound
					actions={
						<>
							<Button
								variant="secondary"
								nativeButton={false}
								render={<Link to="/" />}
							>
								Go to projects
							</Button>
							<Button variant="ghost" onClick={() => router.history.back()}>
								Go back
							</Button>
						</>
					}
				/>
			</div>
		</div>
	);
}

// Renders in place of the whole root — shell, toaster and tooltip provider
// included — so it leans on nothing but plain buttons and a link.
function ErrorComponent({
	error,
	reset,
}: {
	error: unknown;
	reset: () => void;
}) {
	const router = useRouter();
	const message = error instanceof Error ? error.message : String(error);
	return (
		<Centered>
			<StatePanel
				tone="danger"
				title="NebulaCtrl could not load this page"
				body="The control plane did not answer or the page failed to draw. Nothing was changed; check that the server is running, then try again."
				detail={message}
				actions={
					<Button
						variant="secondary"
						onClick={() => {
							reset();
							void router.invalidate();
						}}
					>
						Try again
					</Button>
				}
			/>
		</Centered>
	);
}

function Centered({ children }: { children: ReactNode }) {
	return (
		<div className="flex min-h-full items-center justify-center bg-ambient p-4">
			<div className="w-full max-w-[720px]">{children}</div>
		</div>
	);
}
