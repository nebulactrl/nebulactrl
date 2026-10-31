import { useQuery } from "@tanstack/react-query";
import {
	Link,
	type LinkComponentProps,
	useRouterState,
} from "@tanstack/react-router";
import { cn } from "cn";
import {
	FolderKanbanIcon,
	GitPullRequestIcon,
	KeyRoundIcon,
	PanelLeftCloseIcon,
	PanelLeftOpenIcon,
	RadioTowerIcon,
	ShieldIcon,
	SlidersHorizontalIcon,
	XIcon,
} from "lucide-react";
import { type KeyboardEvent, type ReactNode, useEffect, useRef } from "react";
import { projectsQuery, systemQuery } from "#/api/hooks";
import type { Me } from "#/api/models";
import { LogoMark } from "#/components/brand/logo-mark";
import { ActivityIcon } from "#/components/icons/animated/activity-icon";
import type { AnimatedIconProps } from "#/components/icons/animated/icon-base";
import { LayoutGridIcon } from "#/components/icons/animated/layout-grid-icon";
import { RocketIcon } from "#/components/icons/animated/rocket-icon";
import { ServerIcon } from "#/components/icons/animated/server-icon";
import { SettingsIcon } from "#/components/icons/animated/settings-icon";
import { useHoverPlay } from "#/components/icons/animated/use-hover-play";
import { Kbd } from "#/components/ui/kbd";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import { useServers } from "#/features/canvas/data";
import { useEnvDeploymentCount } from "#/features/env-deployments/queries";
import { useAdminAttention } from "#/features/settings/admin-attention";
import { TeamSwitcher } from "#/features/teams/team-switcher";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import { vt } from "#/lib/transitions";
import { MOD_KEY } from "./command-palette";
import { useNavIndicator } from "./nav-indicator";
import { stageRailGhost, useRailReveal } from "./rail-motion";
import { useSidebarEnvironment } from "./use-last-environment";
import { useProjectContext } from "./use-project-context";
import { useSidebar } from "./use-sidebar";
import { UserMenu } from "./user-menu";

const ITEM =
	"group/nav relative flex h-8 min-w-0 items-center gap-2.5 rounded-ctl border border-transparent px-2.5 font-medium text-[13px] text-ink-3 leading-none outline-none transition-colors duration-quick ease-out hover:text-ink-1 focus-visible:text-ink-1 focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2 data-[current]:text-ink-1 aria-disabled:cursor-not-allowed aria-disabled:opacity-45 aria-disabled:hover:text-ink-3 max-[759px]:h-10";

const ICON =
	"size-4 shrink-0 transition-[color,filter] duration-quick group-data-[current]/nav:text-accent-ink group-data-[current]/nav:drop-shadow-[0_0_6px_oklch(0.75_0.14_278/.5)]";

/**
 * A row outside the list the sliding plate serves (Admin, at the foot): the
 * router marks it with `aria-current`, and it keeps its own plate.
 */
const ROUTED_ITEM =
	"aria-[current=page]:border-line aria-[current=page]:bg-surface-well aria-[current=page]:text-ink-1 aria-[current=page]:shadow-well";

const ROUTED_ICON =
	"group-aria-[current=page]/nav:text-accent-ink group-aria-[current=page]/nav:drop-shadow-[0_0_6px_oklch(0.75_0.14_278/.5)]";

type NavTarget = Pick<
	LinkComponentProps,
	"to" | "params" | "search" | "activeOptions"
>;

function NavCount({ value }: { value: string | undefined }) {
	if (value === undefined) return null;
	return (
		<span className="ml-auto shrink-0 font-medium font-mono text-[11px] text-ink-4 leading-none tabular-nums">
			{value}
		</span>
	);
}

type NavIcon = (props: AnimatedIconProps) => ReactNode;

/** Shared variables; a static icon (the rail's animated set has no key). */
const VariablesIcon: NavIcon = ({ className }) => (
	<KeyRoundIcon aria-hidden className={className} />
);

/** Pull-request previews; a static icon like Variables. */
const PreviewsIcon: NavIcon = ({ className }) => (
	<GitPullRequestIcon aria-hidden className={className} />
);

/** The team's project list; a static icon like Previews. */
const ProjectsIcon: NavIcon = ({ className }) => (
	<FolderKanbanIcon aria-hidden className={className} />
);

/** The project's own settings; a static icon, apart from the team's gear. */
const ProjectSettingsIcon: NavIcon = ({ className }) => (
	<SlidersHorizontalIcon aria-hidden className={className} />
);

/** This installation's own settings; a static icon like Projects. */
const AdminIcon: NavIcon = ({ className }) => (
	<ShieldIcon aria-hidden className={className} />
);

/** Public status pages; a static icon like Projects. */
const StatusPagesIcon: NavIcon = ({ className }) => (
	<RadioTowerIcon aria-hidden className={className} />
);

/**
 * One sidebar row. Collapsed, the label moves into a tooltip on the icon.
 * The icon plays once while a mouse rests on the row; see `useHoverPlay`.
 */
function NavItem({
	icon: Icon,
	label,
	count,
	attention,
	collapsed,
	target,
	disabledReason,
	vtName,
	routed = false,
	current = false,
}: {
	icon: NavIcon;
	label: string;
	count?: string | undefined;
	/** Why the row asks for a look ("update available"): a dot, and words for assistive tech. */
	attention?: string | undefined;
	collapsed: boolean;
	/** Omitted for an item whose destination is not known yet. */
	target: NavTarget | undefined;
	disabledReason?: string;
	/** The shared-element name when the label alone would collide with another row's. */
	vtName?: string;
	/** A row outside the sliding plate's list: the router marks it, and it draws its own plate. */
	routed?: boolean;
	/** Marks the row current when its destination is a page inside a wider area (Admin opens a section). */
	current?: boolean;
}) {
	// A row that asks for a look is named with what it asks: "Admin, update available".
	const accessible = [label, count, attention]
		.filter((part) => part !== undefined)
		.join(", ");
	const hover = useHoverPlay();
	const content: ReactNode = (
		<>
			<Icon
				animate={target !== undefined && hover.playing}
				className={cn(ICON, routed && ROUTED_ICON)}
			/>
			<span
				className={cn("truncate", collapsed && "sr-only")}
				aria-hidden={collapsed || undefined}
				// The label becomes the page title it leads to (see PageFrame).
				{...(collapsed ? {} : vt("nav", vtName ?? label.toLowerCase()))}
			>
				{label}
			</span>
			{collapsed ? <span className="sr-only">{accessible}</span> : null}
			{collapsed ? null : <NavCount value={count} />}
			{attention === undefined ? null : (
				<span
					aria-hidden
					className={cn(
						"size-1.5 shrink-0 rounded-full bg-accent-ink",
						collapsed ? "absolute top-2 right-2" : "ml-auto",
					)}
				/>
			)}
		</>
	);
	const className = cn(
		ITEM,
		routed && ROUTED_ITEM,
		collapsed && "size-10 justify-center px-0",
	);

	const node = target ? (
		<Link
			{...target}
			aria-label={attention === undefined ? undefined : accessible}
			aria-current={current ? "page" : undefined}
			className={className}
			{...hover.handlers}
		>
			{content}
		</Link>
	) : (
		<button
			type="button"
			aria-disabled="true"
			title={disabledReason}
			className={cn(className, "w-full text-left")}
		>
			{content}
		</button>
	);
	if (!(collapsed || attention !== undefined)) return node;
	return (
		<Tooltip>
			<TooltipTrigger render={node} />
			<TooltipContent side="right">
				{collapsed ? accessible : attention}
				{collapsed && !target && disabledReason ? ` — ${disabledReason}` : ""}
			</TooltipContent>
		</Tooltip>
	);
}

function SectionHeading({
	children,
	collapsed,
}: {
	children: string;
	collapsed: boolean;
}) {
	if (collapsed) {
		// The rail has no room for the group's name: a rule keeps the groups apart.
		return <div aria-hidden className="my-3 h-px w-6 shrink-0 bg-line" />;
	}
	return (
		<h2 className="mt-4 mb-2 px-2.5 font-mono font-semibold text-[10.5px] text-ink-4 uppercase leading-none tracking-[0.12em]">
			{children}
		</h2>
	);
}

/**
 * The console's left rail: brand, team switcher, the project's pages (Canvas,
 * Deployments, Variables, Previews, Observability, Project settings), the
 * team's (Projects, Servers, Status pages, Team settings), and at the foot
 * Admin (the instance admin only), Collapse and the signed-in person. Docs and
 * Account settings are in the person's menu. It collapses to an icon rail (⌘B)
 * and remembers which it was. Below 760px it is an off-canvas drawer instead:
 * shut by default, opened by the top bar's menu button or ⌘B, trapped while
 * open, closed by Escape, the scrim, the close button or a link.
 */
export function Sidebar({ user }: { user: Me }) {
	const { collapsed, animate, narrow, drawerOpen, closeDrawer, toggle } =
		useSidebar();
	const context = useProjectContext();
	const target = useSidebarEnvironment(context);
	const environmentId = context?.environment?.id;
	const servers = useServers();
	const deployments = useEnvDeploymentCount(environmentId);
	const attention = useAdminAttention();
	const projects = useQuery(projectsQuery());
	const adminAttention = attention.update
		? "Update available"
		: attention.keyReminder
			? "Secret key not confirmed stored"
			: undefined;
	const system = useQuery(systemQuery());
	const asideRef = useRef<HTMLElement>(null);
	useRailReveal(asideRef, collapsed, narrow);
	const scrollRef = useRef<HTMLDivElement>(null);
	const indicatorRef = useRef<HTMLSpanElement>(null);
	const nav = useNavIndicator(
		scrollRef,
		indicatorRef,
		`${collapsed}:${narrow}`,
	);
	const closeRef = useRef<HTMLButtonElement>(null);
	const pathname = useRouterState({
		select: (router) => router.location.pathname,
	});

	const params = target
		? { projectId: target.projectId, envSlug: target.envSlug }
		: undefined;
	// A team that has no project yet is told what to do, not what to open.
	const needsProject =
		projects.data?.length === 0
			? "Create a project first"
			: "Open a project first";
	// The dot leads to the page that holds what it asks for.
	const adminTarget: NavTarget = attention.update
		? { to: "/admin/$section", params: { section: "updates" } }
		: attention.keyReminder
			? { to: "/admin/$section", params: { section: "backup" } }
			: { to: "/admin" };
	const toggleLabel = collapsed ? "Expand sidebar" : "Collapse sidebar";
	const ToggleIcon = collapsed ? PanelLeftOpenIcon : PanelLeftCloseIcon;

	// Growing past the breakpoint leaves no open drawer behind to reappear
	// when the window narrows again.
	useEffect(() => {
		if (!narrow) closeDrawer();
	}, [narrow, closeDrawer]);

	// A new page closes the drawer; a link to the page already open does too
	// (see the click handler below).
	// biome-ignore lint/correctness/useExhaustiveDependencies: the path is the trigger
	useEffect(() => {
		closeDrawer();
	}, [pathname, closeDrawer]);

	// While the drawer is open: focus moves in, Escape closes it, and focus
	// returns to where it was (the menu button) once it is shut.
	useEffect(() => {
		if (!drawerOpen) return;
		const returnTo = document.activeElement;
		closeRef.current?.focus({ preventScroll: true });
		const onKeyDown = (event: globalThis.KeyboardEvent) => {
			if (event.key !== "Escape" || event.defaultPrevented) return;
			// A menu, dialog or palette open over the drawer takes Escape first.
			if (
				event.target instanceof Element &&
				event.target.closest('[role="menu"],[role="listbox"],[role="dialog"]')
			) {
				return;
			}
			event.preventDefault();
			closeDrawer();
		};
		document.addEventListener("keydown", onKeyDown);
		return () => {
			document.removeEventListener("keydown", onKeyDown);
			if (returnTo instanceof HTMLElement && returnTo.isConnected) {
				returnTo.focus({ preventScroll: true });
			}
		};
	}, [drawerOpen, closeDrawer]);

	// Tab wraps inside the open drawer; the page behind it is inert.
	const trapTab = (event: KeyboardEvent<HTMLElement>) => {
		const aside = asideRef.current;
		if (
			!(drawerOpen && event.key === "Tab") ||
			!aside ||
			!(event.target instanceof Node) ||
			!aside.contains(event.target)
		) {
			return;
		}
		const focusable = [
			...aside.querySelectorAll<HTMLElement>(
				'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])',
			),
		].filter((element) => element.getAttribute("aria-disabled") !== "true");
		const first = focusable[0];
		const last = focusable.at(-1);
		if (!first || !last) return;
		if (event.shiftKey && document.activeElement === first) {
			event.preventDefault();
			last.focus();
		} else if (!event.shiftKey && document.activeElement === last) {
			event.preventDefault();
			first.focus();
		}
	};

	return (
		<>
			{narrow ? (
				<button
					type="button"
					tabIndex={-1}
					aria-label="Close navigation"
					onClick={closeDrawer}
					className={cn(
						"fixed inset-0 z-[44] bg-scrim",
						animate &&
							"transition-opacity ease-smooth-out motion-reduce:transition-none",
						animate && (drawerOpen ? "duration-fast" : "duration-quick"),
						drawerOpen ? "opacity-100" : "pointer-events-none opacity-0",
					)}
					// A closed scrim must not be reachable by assistive tech.
					aria-hidden={drawerOpen ? undefined : true}
				/>
			) : null}
			<aside
				ref={asideRef}
				id="primary-navigation"
				aria-label="Primary"
				data-collapsed={collapsed || undefined}
				data-drawer={narrow ? (drawerOpen ? "open" : "closed") : undefined}
				inert={narrow && !drawerOpen}
				onKeyDown={trapTab}
				onClick={(event) => {
					if (
						narrow &&
						event.target instanceof Element &&
						event.target.closest("a[href]")
					) {
						closeDrawer();
					}
				}}
				className={cn(
					"ht-foot flex h-full shrink-0 flex-col overflow-hidden border-line border-r bg-linear-to-b from-[color-mix(in_oklab,var(--color-surface-base),white_1.2%)] to-surface-base",
					narrow
						? "fixed inset-y-0 left-0 z-[45] w-[272px] max-w-[calc(100vw-56px)] shadow-pop"
						: collapsed
							? "relative z-30 w-16"
							: "relative z-30 w-[220px]",
					// Entering is slower than leaving. A keyboard open or close skips it.
					narrow &&
						animate &&
						"transition-[transform,visibility] ease-smooth-out motion-reduce:transition-none",
					narrow &&
						animate &&
						(drawerOpen ? "duration-fast" : "duration-quick"),
					narrow && !drawerOpen && "invisible -translate-x-full",
				)}
			>
				<div
					className={cn(
						"flex h-[52px] shrink-0 items-center",
						collapsed ? "justify-center" : "px-[18px]",
						narrow && "justify-between pr-3",
					)}
				>
					<Link
						data-nomorph
						to="/"
						aria-label="NebulaCtrl — all projects"
						className={cn(
							"flex min-w-0 items-center gap-2.5 rounded-ctl outline-none focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2",
							// The rail's mark is a 24px glyph; its target is a 40px key.
							collapsed && "size-10 justify-center",
						)}
					>
						<LogoMark size={24} />
						{collapsed ? null : (
							<span className="truncate font-display font-semibold text-[15px] text-ink-1 leading-none tracking-[-0.01em]">
								NebulaCtrl
							</span>
						)}
					</Link>
					{narrow ? (
						<button
							ref={closeRef}
							type="button"
							aria-label="Close navigation"
							onClick={closeDrawer}
							className="pressable grid size-10 shrink-0 cursor-pointer place-items-center rounded-ctl text-ink-3 outline-none hover:text-ink-1 focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2"
						>
							<XIcon aria-hidden className="size-4" />
						</button>
					) : null}
				</div>

				<div
					ref={scrollRef}
					onPointerDownCapture={nav.press}
					onClickCapture={nav.press}
					className={cn(
						"relative flex min-h-0 flex-1 flex-col overflow-y-auto overflow-x-hidden overscroll-contain pb-2",
						collapsed ? "items-center px-3" : "px-3",
					)}
				>
					{/* The active row's plate: it slides on `transform` alone (see
					    nav-indicator.ts) under the rows, which are positioned too and
					    come later in the order. */}
					<span
						ref={indicatorRef}
						aria-hidden
						className="pointer-events-none absolute top-0 left-0 rounded-ctl border border-line bg-surface-well opacity-0 shadow-well transition-opacity duration-quick ease-out motion-reduce:transition-none"
					/>
					<div className={cn("mt-1 mb-3", collapsed ? "" : "w-full")}>
						<TeamSwitcher collapsed={collapsed} />
					</div>

					<SectionHeading collapsed={collapsed}>Project</SectionHeading>
					<nav aria-label="Project" className="flex w-full flex-col gap-0.5">
						<NavItem
							icon={LayoutGridIcon}
							label="Canvas"
							collapsed={collapsed}
							target={
								params
									? {
											to: "/p/$projectId/$envSlug",
											params,
											activeOptions: { exact: true, includeSearch: false },
										}
									: undefined
							}
							disabledReason={needsProject}
						/>
						<NavItem
							icon={RocketIcon}
							label="Deployments"
							count={deployments}
							collapsed={collapsed}
							target={
								params
									? { to: "/p/$projectId/$envSlug/deployments", params }
									: undefined
							}
							disabledReason={needsProject}
						/>
						<NavItem
							icon={VariablesIcon}
							label="Variables"
							collapsed={collapsed}
							target={
								params
									? { to: "/p/$projectId/$envSlug/variables", params }
									: undefined
							}
							disabledReason={needsProject}
						/>
						<NavItem
							icon={PreviewsIcon}
							label="Previews"
							collapsed={collapsed}
							target={
								params
									? { to: "/p/$projectId/$envSlug/previews", params }
									: undefined
							}
							disabledReason={needsProject}
						/>
						<NavItem
							icon={ActivityIcon}
							label="Observability"
							collapsed={collapsed}
							target={
								params
									? { to: "/p/$projectId/$envSlug/observability", params }
									: undefined
							}
							disabledReason={needsProject}
						/>
						{/* Not per environment, so it needs only the project. Its own
						    view-transition name keeps it apart from the team's Settings. */}
						<NavItem
							icon={ProjectSettingsIcon}
							label="Project settings"
							vtName="project-settings"
							collapsed={collapsed}
							target={
								target
									? {
											to: "/p/$projectId/settings",
											params: { projectId: target.projectId },
										}
									: undefined
							}
							disabledReason={needsProject}
						/>
					</nav>

					{/* Servers belong to the team, not to one project. */}
					<SectionHeading collapsed={collapsed}>Team</SectionHeading>
					<nav aria-label="Team" className="flex w-full flex-col gap-0.5">
						<NavItem
							icon={ProjectsIcon}
							label="Projects"
							collapsed={collapsed}
							target={{ to: "/", activeOptions: { exact: true } }}
						/>
						<NavItem
							icon={ServerIcon}
							label="Servers"
							// "0" is noise on a team that has not added a host yet.
							count={
								servers.data === undefined || servers.data.length === 0
									? undefined
									: String(servers.data.length)
							}
							collapsed={collapsed}
							target={{ to: "/servers" }}
						/>
						<NavItem
							icon={StatusPagesIcon}
							label="Status pages"
							collapsed={collapsed}
							target={{ to: "/status-pages" }}
						/>
						<NavItem
							icon={SettingsIcon}
							label="Team settings"
							vtName="team-settings"
							collapsed={collapsed}
							// The area's index opens its first page; every page under it
							// keeps this row current.
							target={{ to: "/settings" }}
						/>
					</nav>
				</div>

				<div
					className={cn(
						"flex shrink-0 flex-col gap-2 border-line border-t p-3",
						collapsed && "items-center",
					)}
				>
					{user.instanceAdmin ? (
						<nav aria-label="Instance" className="flex w-full flex-col">
							<NavItem
								icon={AdminIcon}
								label="Admin"
								collapsed={collapsed}
								attention={adminAttention}
								routed
								current={
									pathname === "/admin" || pathname.startsWith("/admin/")
								}
								target={adminTarget}
							/>
						</nav>
					) : null}
					{narrow ? null : (
						<Tooltip>
							<TooltipTrigger
								render={
									<button
										type="button"
										aria-label={toggleLabel}
										aria-keyshortcuts="Control+B Meta+B"
										onClick={() => {
											// A keyboard activation is instant, like ⌘B; a pointer
											// collapse takes a snapshot of the labels it is taking away.
											const viaKeyboard = lastInputWasKeyboard();
											if (!(collapsed || viaKeyboard)) {
												stageRailGhost(asideRef.current);
											}
											toggle(viaKeyboard);
										}}
										className={cn(
											"flex h-8 cursor-pointer items-center gap-2.5 rounded-ctl border border-transparent px-2.5 font-medium text-[13px] text-ink-3 leading-none outline-none transition-colors duration-quick ease-out hover:text-ink-1 focus-visible:text-ink-1 focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2",
											collapsed ? "size-10 justify-center px-0" : "w-full",
										)}
									/>
								}
							>
								<ToggleIcon aria-hidden className="size-4 shrink-0" />
								{collapsed ? null : (
									<>
										<span className="truncate">Collapse</span>
										<Kbd className="ml-auto">{MOD_KEY}B</Kbd>
									</>
								)}
							</TooltipTrigger>
							<TooltipContent side="right">
								{toggleLabel} ({MOD_KEY}B)
							</TooltipContent>
						</Tooltip>
					)}
					<UserMenu
						user={user}
						version={system.data?.version}
						versionPending={system.isPending}
						collapsed={collapsed}
					/>
				</div>
			</aside>
		</>
	);
}
