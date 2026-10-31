import { useRouterState } from "@tanstack/react-router";
import { cn } from "cn";
import { MenuIcon, SearchIcon } from "lucide-react";
import { Kbd } from "#/components/ui/kbd";
import { DeployButton } from "#/features/deploy-button";
import { useCurrentTeam } from "#/features/teams/queries";
import { MOD_KEY, PALETTE_LABEL, PALETTE_PROMPT } from "./command-palette";
import { EnvironmentSwitcher } from "./environment-switcher";
import { pageCrumb } from "./page-crumb";
import { ProjectSwitcher } from "./project-switcher";
import { useShellActions } from "./shell-actions";
import { useProjectContext } from "./use-project-context";
import { useSidebarDrawer } from "./use-sidebar";

function Slash({ className }: { className?: string }) {
	return (
		<span
			aria-hidden
			className={cn("shrink-0 text-[15px] text-ink-4 leading-none", className)}
		>
			/
		</span>
	);
}

/**
 * The 52px bar over every screen of the console, beside the sidebar. Left: a
 * menu button below 760px (the sidebar is a drawer there); then, inside a
 * project, the project switcher, a slash and the environment switcher;
 * elsewhere, where the page applies (see `pageCrumb`). Middle: the ⌘K search
 * field. Right: Deploy · N for the environment on screen. Under 640px the
 * switchers and Deploy shrink to icons and a short name so the bar never
 * overflows.
 */
export function TopBar() {
	const { openPalette } = useShellActions();
	const context = useProjectContext();
	const { narrow, drawerOpen, openDrawer } = useSidebarDrawer();
	const pathname = useRouterState({
		select: (state) => state.location.pathname,
	});
	const { team } = useCurrentTeam();

	return (
		<header className="@container relative z-40 flex h-[52px] shrink-0 items-center gap-2.5 border-line border-b bg-linear-to-b from-[color-mix(in_oklab,var(--color-surface-float),white_3%)] to-surface-float px-3.5 shadow-[inset_0_1px_0_rgb(255_255_255/.08),0_6px_18px_-8px_rgb(2_4_10/.5)] max-[759px]:gap-2 max-[759px]:px-2.5">
			{narrow ? (
				<button
					type="button"
					onClick={openDrawer}
					aria-label="Open navigation"
					aria-expanded={drawerOpen}
					aria-controls="primary-navigation"
					aria-keyshortcuts="Control+B Meta+B"
					className="btn-secondary pressable grid size-10 shrink-0 cursor-pointer place-items-center rounded-ctl outline-none focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2"
				>
					<MenuIcon aria-hidden className="size-4" />
				</button>
			) : null}
			{context ? (
				<>
					<ProjectSwitcher context={context} />
					{/* The project's settings sit outside any environment. */}
					{context.envSlug === undefined ? null : (
						<>
							<Slash className="@max-[759px]:hidden" />
							<EnvironmentSwitcher context={context} />
						</>
					)}
				</>
			) : (
				<span className="min-w-0 truncate px-1 font-semibold text-[14px] text-ink-1 leading-none">
					{pageCrumb(pathname, team?.name)}
				</span>
			)}
			<div className="min-w-0 flex-1" />
			{/* Below 900px the icon takes over so a narrower window never crowds
			    the switchers. */}
			<button
				type="button"
				onClick={openPalette}
				aria-label={PALETTE_LABEL}
				aria-keyshortcuts="Control+K Meta+K"
				className="field pressable hidden h-[34px] w-full max-w-[440px] min-w-0 flex-[0_1_440px] cursor-pointer items-center gap-2.5 rounded-ctl pr-2 pl-3 text-[13px] text-ink-4 leading-none min-[900px]:flex"
			>
				<SearchIcon aria-hidden className="size-4 shrink-0" />
				<span className="min-w-0 flex-1 truncate text-left">
					{PALETTE_PROMPT}
				</span>
				{/* One cap for the chord reads calmer than two in a 34px field. */}
				<Kbd aria-hidden className="mb-0 h-5 min-w-0 px-1.5 text-[11px]">
					{MOD_KEY}K
				</Kbd>
			</button>
			<button
				type="button"
				onClick={openPalette}
				aria-label={PALETTE_LABEL}
				className="btn-secondary pressable grid size-[34px] shrink-0 cursor-pointer place-items-center rounded-ctl outline-none focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2 max-[759px]:size-10 min-[900px]:hidden"
			>
				<SearchIcon aria-hidden className="size-3.5" />
			</button>
			<DeployButton />
		</header>
	);
}
