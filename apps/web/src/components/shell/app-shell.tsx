import { type ReactNode, useRef } from "react";
import type { Me } from "#/api/models";
import { useFleetCommands } from "#/features/fleet";
import { TeamScopeBoundary } from "#/features/teams/team-scope";
import { LiveUpdates } from "./live-updates";
import { useColumnSlide } from "./rail-motion";
import { ShellActionsProvider } from "./shell-actions";
import { Sidebar } from "./sidebar";
import { TopBar } from "./top-bar";
import { useNavigationCommands } from "./use-navigation-commands";
import { useSidebar, useSidebarShortcut } from "./use-sidebar";

/** Registers the shell's ⌘K entries; renders nothing. */
function NavigationCommands() {
	useNavigationCommands();
	useFleetCommands();
	return null;
}

/**
 * The column beside the sidebar: the top bar over the screen. It is the one
 * piece of the frame that reads the collapsed state, so a collapse re-renders
 * this wrapper and the sidebar and nothing between: `children` are elements
 * the frame made once. While the narrow drawer is open the page behind it
 * is inert: focus and taps cannot reach it.
 */
function ShellColumn({ children }: { children: ReactNode }) {
	const { collapsed, narrow, drawerOpen } = useSidebar();
	const ref = useRef<HTMLDivElement>(null);
	useColumnSlide(ref, collapsed, narrow);
	return (
		<div
			ref={ref}
			inert={drawerOpen}
			className="ht-dawn flex min-w-0 flex-1 flex-col"
		>
			{children}
		</div>
	);
}

/**
 * The console frame: the collapsible sidebar on the left (an off-canvas drawer
 * below 760px), the 52px top bar beside it, and the screen beneath filling the
 * rest of the viewport. A screen scrolls inside `#main-content`, never the
 * window, so the bars never move. Route changes run as view transitions on
 * `#main-content` (see `#/lib/transitions/route-transitions`). The frame
 * itself reads no sidebar state, and is the positioned host the sidebar's
 * collapse animation draws its snapshot into.
 */
export function AppShell({
	user,
	children,
}: {
	user: Me;
	children: ReactNode;
}) {
	useSidebarShortcut();

	return (
		<ShellActionsProvider>
			<NavigationCommands />
			<LiveUpdates />
			<div className="relative flex h-svh overflow-hidden bg-surface-base">
				{/* First focusable element: lets a keyboard user skip the chrome. */}
				<a
					href="#main-content"
					className="sr-only focus:not-sr-only focus:absolute focus:top-2 focus:left-2 focus:z-50 focus:rounded-md focus:bg-surface-pop focus:px-3 focus:py-2 focus:text-ink-1 focus:shadow-pop"
				>
					Skip to content
				</a>
				<Sidebar user={user} />
				<ShellColumn>
					<TopBar />
					<main
						id="main-content"
						tabIndex={-1}
						className="relative min-h-0 min-w-0 flex-1 overflow-y-auto overflow-x-hidden outline-none"
					>
						<TeamScopeBoundary>{children}</TeamScopeBoundary>
					</main>
				</ShellColumn>
			</div>
		</ShellActionsProvider>
	);
}
