import { docHref } from "@nebulactrl/docs/types";
import {
	Link,
	Outlet,
	useLocation,
	useRouteContext,
	useRouter,
} from "@tanstack/react-router";
import { ArrowLeftIcon, LogInIcon, MenuIcon, SearchIcon } from "lucide-react";
import { useMemo, useState } from "react";
import { LogoMark } from "#/components/brand/logo-mark";
import type { CommandItem } from "#/components/motion/command-palette";
import { CommandPalette } from "#/components/motion/command-palette";
import { MOD_KEY } from "#/components/shell/command-palette";
import { Button } from "#/components/ui/button";
import { Kbd } from "#/components/ui/kbd";
import {
	Sheet,
	SheetContent,
	SheetDescription,
	SheetTitle,
} from "#/components/ui/sheet";
import { docMeta, docNav } from "./docs-data";
import { useDocsQueryItems } from "./docs-search-source";
import { DocsSidebar } from "./docs-sidebar";
import { DocsScrollContext } from "./scroll-context";
import "./docs.css";

const SEARCH_PLACEHOLDER = "Search the docs…";

/**
 * The docs frame: its own top bar (the console's sidebar is not mounted, so
 * /docs works signed out), the section sidebar on the left (a drawer below
 * 1024px), and the scrolling page beside it. ⌘K opens a docs-only search.
 */
export function DocsLayout() {
	const { user } = useRouteContext({ from: "__root__" });
	const router = useRouter();
	const pathname = useLocation({ select: (location) => location.pathname });
	const slug = pathname.replace(/^\/docs\/?/, "").replace(/\/$/, "");
	const [scroll, setScroll] = useState<HTMLElement | null>(null);
	const [searchOpen, setSearchOpen] = useState(false);
	const [navOpen, setNavOpen] = useState(false);
	const queryItems = useDocsQueryItems(searchOpen);

	const suggested = useMemo<CommandItem[]>(
		() =>
			(docNav[0]?.items ?? []).flatMap((item) => {
				const meta = docMeta(item.slug);
				return meta
					? [
							{
								id: `docs-start:${meta.slug}`,
								label: meta.title,
								hint: meta.section,
								group: "Start here",
								onSelect: () => router.history.push(docHref(meta.slug)),
							} satisfies CommandItem,
						]
					: [];
			}),
		[router],
	);

	return (
		<div className="flex h-svh flex-col bg-surface-base">
			<a
				href="#docs-main"
				className="sr-only focus:not-sr-only focus:absolute focus:top-2 focus:left-2 focus:z-50 focus:rounded-md focus:bg-surface-pop focus:px-3 focus:py-2 focus:text-ink-1"
			>
				Skip to content
			</a>
			<header className="relative z-40 flex h-[52px] shrink-0 items-center gap-3 border-line border-b bg-linear-to-b from-[color-mix(in_oklab,var(--color-surface-float),white_3%)] to-surface-float px-3.5 shadow-[inset_0_1px_0_rgb(255_255_255/.08),0_6px_18px_-8px_rgb(2_4_10/.5)]">
				<button
					type="button"
					onClick={() => setNavOpen(true)}
					aria-label="Open documentation menu"
					className="btn-secondary pressable grid size-[30px] shrink-0 cursor-pointer place-items-center rounded-ctl outline-none focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2 lg:hidden"
				>
					<MenuIcon aria-hidden className="size-4" />
				</button>
				<Link
					to="/docs"
					className="flex shrink-0 items-center gap-2.5 text-ink-1 no-underline outline-none focus-visible:focus-ring"
				>
					<LogoMark size={24} />
					<span className="font-semibold text-[14px] leading-none">
						NebulaCtrl <span className="font-normal text-ink-3">Docs</span>
					</span>
				</Link>
				<div className="min-w-0 flex-1" />
				<button
					type="button"
					onClick={() => setSearchOpen(true)}
					aria-label="Search the docs"
					aria-keyshortcuts="Control+K Meta+K"
					className="field pressable hidden h-[34px] w-full max-w-[420px] min-w-0 flex-[0_1_420px] cursor-pointer items-center gap-2.5 rounded-ctl pr-2 pl-3 text-[13px] text-ink-4 leading-none sm:flex"
				>
					<SearchIcon aria-hidden className="size-4 shrink-0" />
					<span className="min-w-0 flex-1 truncate text-left">
						{SEARCH_PLACEHOLDER}
					</span>
					<Kbd aria-hidden className="mb-0 h-5 min-w-0 px-1.5 text-[11px]">
						{MOD_KEY}K
					</Kbd>
				</button>
				<button
					type="button"
					onClick={() => setSearchOpen(true)}
					aria-label="Search the docs"
					className="btn-secondary pressable grid size-[34px] shrink-0 cursor-pointer place-items-center rounded-ctl outline-none focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2 sm:hidden"
				>
					<SearchIcon aria-hidden className="size-3.5" />
				</button>
				<div className="hidden flex-1 sm:block" />
				{user ? (
					<Button
						variant="secondary"
						size="md"
						nativeButton={false}
						render={<Link to="/" />}
					>
						<ArrowLeftIcon />
						<span className="max-sm:sr-only">Back to console</span>
					</Button>
				) : (
					<Button
						variant="secondary"
						size="md"
						nativeButton={false}
						render={<Link to="/login" />}
					>
						<LogInIcon />
						<span className="max-sm:sr-only">Sign in</span>
					</Button>
				)}
			</header>

			<div className="flex min-h-0 flex-1">
				<aside className="hidden w-[272px] shrink-0 overflow-y-auto overscroll-contain border-line border-r lg:block">
					<DocsSidebar current={slug} />
				</aside>
				<div
					ref={setScroll}
					data-docs-scroll
					id="docs-main"
					className="min-w-0 flex-1 overflow-y-auto overscroll-contain"
				>
					<DocsScrollContext value={scroll}>
						<Outlet />
					</DocsScrollContext>
				</div>
			</div>

			<Sheet open={navOpen} onOpenChange={setNavOpen}>
				<SheetContent
					side="left"
					showOverlay
					className="w-[300px] max-w-[85vw] p-0"
				>
					<SheetTitle className="sr-only">Documentation</SheetTitle>
					<SheetDescription className="sr-only">
						Pages of the NebulaCtrl documentation.
					</SheetDescription>
					<div className="overflow-y-auto overscroll-contain">
						<DocsSidebar current={slug} onNavigate={() => setNavOpen(false)} />
					</div>
				</SheetContent>
			</Sheet>

			<CommandPalette
				items={[]}
				initialItems={suggested}
				queryItems={queryItems}
				placeholder={SEARCH_PLACEHOLDER}
				emptyMessage="No page mentions that."
				open={searchOpen}
				onOpenChange={setSearchOpen}
			/>
		</div>
	);
}
