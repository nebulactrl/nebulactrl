import { installToneBandMask } from "@nebulactrl/dither/interface/tone-band-math";
import { QueryClientProvider } from "@tanstack/react-query";
import { createRouter, RouterProvider } from "@tanstack/react-router";
import { MotionConfig } from "motion/react";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { setApiHandlers } from "#/api/client-config";
import { getMySecurityQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { queryClient } from "#/api/query-client";
import { Loading } from "#/components/common/loading";
import { toast } from "#/components/ui/toast";
import "#/lib/dither-brands";
import { installRouteTransitions } from "#/lib/transitions";
import { routeTree } from "./routeTree.gen";
import "./styles.css";

// The dithered band that `tone-callout` paints on alerts and toasts reads its
// mask from a custom property on the root.
installToneBandMask();

// A route whose loader takes longer than the router's pending threshold shows
// a small spinner and a caption in its place (after a 300 ms grace, so fast
// loads show nothing), full-page at the root and in the content area inside
// the shell.
const router = createRouter({
	routeTree,
	context: { queryClient },
	defaultPendingComponent: () => <Loading label="Loading" />,
	// Hovering or focusing a link for 50 ms loads the route's code and data,
	// so the click only renders. TanStack Query decides what is fresh.
	defaultPreload: "intent",
	defaultPreloadStaleTime: 0,
});

// Every route change runs as a view transition: shared elements morph, the
// rest cross-fades. Without the API the router navigates instantly.
installRouteTransitions(router);

declare module "@tanstack/react-router" {
	interface Register {
		router: typeof router;
	}
}

// The global reactions to a failed call live here, where the router, the
// query client and the toaster all exist (see src/api/client-config.ts).
let lastForbiddenToast = 0;

/** The screens that show a refusal inline: a toast on top would say it twice. */
function onInlineErrorPage(pathname: string): boolean {
	return (
		pathname === "/login" ||
		pathname === "/setup" ||
		pathname.startsWith("/invite/")
	);
}

setApiHandlers({
	// A 401 from any session-protected call means the session ended.
	onUnauthorized: () => {
		const { pathname, search } = window.location;
		if (onInlineErrorPage(pathname)) return;
		queryClient.clear();
		void router.navigate({
			to: "/login",
			search: { returnTo: `${pathname}${search}` },
		});
	},
	onForbidden: (error) => {
		// A team that requires 2FA: the enrolment screen is the answer, not a toast.
		if (error.code === "two_factor_enrollment_required") {
			void queryClient.invalidateQueries({
				queryKey: getMySecurityQueryKey(),
			});
			return;
		}
		if (onInlineErrorPage(window.location.pathname)) return;
		const now = Date.now();
		if (now - lastForbiddenToast < 4_000) return;
		lastForbiddenToast = now;
		toast.add({
			id: "forbidden",
			type: "error",
			title: "Not allowed",
			description:
				error.message || "Your account or token does not have access to this.",
			timeout: 6_000,
		});
	},
	onConflict: (error) => {
		if (error.code !== "generation_conflict") return;
		toast.add({
			id: "generation-conflict",
			type: "warning",
			title: "Someone else changed this service — reloaded",
			description: "Review the latest values, then apply your change again.",
			timeout: 6_000,
		});
		void queryClient.invalidateQueries();
	},
});

const rootElement = document.getElementById("root");
if (!rootElement) {
	throw new Error(
		"index.html is missing the #root element; NebulaCtrl cannot mount.",
	);
}

// Every motion on the page honours the visitor's reduced-motion setting.
// iOS Safari paints `:active` only under a `touchstart` listener, and React's
// own listener covers the root but not the menus and dialogs portalled into
// the body. One empty passive listener on the body makes every key sink under
// a finger.
document.body.addEventListener("touchstart", () => {}, { passive: true });

createRoot(rootElement).render(
	<StrictMode>
		<MotionConfig reducedMotion="user">
			<QueryClientProvider client={queryClient}>
				<RouterProvider router={router} />
			</QueryClientProvider>
		</MotionConfig>
	</StrictMode>,
);
