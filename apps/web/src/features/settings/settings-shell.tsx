import { useRouter } from "@tanstack/react-router";
import { cn } from "cn";
import { SearchIcon, XIcon } from "lucide-react";
import {
	type KeyboardEvent,
	type MouseEvent,
	type ReactNode,
	useId,
	useMemo,
	useState,
} from "react";
import { PageFrame } from "#/components/shell/page-frame";
import { Button } from "#/components/ui/button";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectGroup,
	SelectItem,
	SelectLabel,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import {
	filterPages,
	findPage,
	groupPages,
	type SettingsArea,
	type SettingsPage,
	settingsElsewhere,
	settingsHref,
} from "./nav-model";
import { SettingsSection } from "./section";

// The row of the section list: 32px, the active page sunk into a well with
// the ink lit. The same rows as the inspector's settings rail.
const NAV_ITEM =
	"flex h-8 shrink-0 cursor-pointer items-center rounded-ctl border border-transparent px-2.5 font-medium text-[13px] text-ink-3 leading-none outline-none transition-colors duration-quick ease-out hover:text-ink-1 focus-visible:focus-ring aria-[current=page]:border-line aria-[current=page]:bg-surface-well aria-[current=page]:text-ink-1 aria-[current=page]:shadow-well";

/** The `data-page` of each area: the shared element a sidebar label morphs into. */
const PAGE_NAME: Record<SettingsArea, string> = {
	account: "account",
	team: "team-settings",
	admin: "admin",
	project: "project-settings",
	server: "server-settings",
};

/** True for a plain left click: modified clicks keep their browser meaning. */
function isPlainClick(event: MouseEvent<HTMLAnchorElement>): boolean {
	return (
		event.button === 0 &&
		!event.metaKey &&
		!event.ctrlKey &&
		!event.shiftKey &&
		!event.altKey
	);
}

/** Sets the browser tab title for a framed settings area. */
function ShellTitle({ parts }: { parts: ReadonlyArray<string | undefined> }) {
	useDocumentTitle(parts);
	return null;
}

/**
 * The one layout of every settings area (account, team, admin, a project and a
 * server): the h1 and what the area applies to, a section list on the left
 * with group headers and a filter, and the active page on the right. `pages`
 * are the pages this person may use; the active page is drawn even when it is
 * not one of them, so a direct address can say why it is empty. A server's
 * pages sit inside the server page, under its own h1 and tabs: `embedded` drops
 * the frame and the document title and keeps the list and the page.
 */
export function SettingsShell({
	area,
	title,
	scopeLine,
	pages,
	active,
	projectId,
	serverId,
	embedded = false,
	children,
}: {
	area: SettingsArea;
	title: string;
	/** What the area applies to: "Team · Acme · applies to everyone in it". */
	scopeLine: string;
	pages: readonly SettingsPage[];
	/** The section id of the open page. */
	active: string;
	/** The project, for the project area's addresses. */
	projectId?: string;
	/** The server, for the server area's addresses. */
	serverId?: string;
	/** Draw the list and the page without the page frame. */
	embedded?: boolean;
	children: ReactNode;
}) {
	const router = useRouter();
	const ids = useId();
	const current = findPage(area, active);
	const titleParts = embedded ? null : [current?.title, title];

	const [query, setQuery] = useState("");
	const matches = useMemo(() => filterPages(pages, query), [pages, query]);
	const groups = useMemo(() => groupPages(matches), [matches]);
	const filtering = query.trim() !== "";
	const elsewhere = filtering ? settingsElsewhere(area, query) : undefined;
	const hrefOf = (page: SettingsPage) =>
		settingsHref(area, page.section, {
			...(projectId ? { projectId } : {}),
			...(serverId ? { serverId } : {}),
		});

	const open = (target: { href: string } | SettingsPage) => {
		setQuery("");
		router.history.push("href" in target ? target.href : hrefOf(target));
	};

	const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
		if (event.key === "Enter") {
			const first = matches[0];
			if (!first) return;
			event.preventDefault();
			open(first);
		} else if (event.key === "Escape" && filtering) {
			event.preventDefault();
			event.stopPropagation();
			setQuery("");
		}
	};

	const labelId = `${ids}-sections`;
	const statusId = `${ids}-status`;

	const layout = (
		<div className="grid min-w-0 gap-6 lg:grid-cols-[200px_minmax(0,1fr)] lg:gap-10">
			<div className="flex min-w-0 flex-col gap-3 lg:sticky lg:top-0 lg:h-fit">
				<h2 id={labelId} className="sr-only">
					{title} sections
				</h2>
				<div className="relative">
					<SearchIcon
						aria-hidden
						className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-ink-3"
					/>
					<Input
						type="text"
						aria-label={`Filter ${title.toLowerCase()}`}
						aria-describedby={statusId}
						autoComplete="off"
						spellCheck={false}
						placeholder="Filter"
						className={cn("pl-8", filtering && "pr-8")}
						value={query}
						onChange={(event) => setQuery(event.target.value)}
						onKeyDown={onKeyDown}
					/>
					{filtering ? (
						<Button
							variant="ghost"
							size="icon-xs"
							aria-label="Clear filter"
							className="absolute top-1/2 right-1 -translate-y-1/2"
							onClick={() => setQuery("")}
						>
							<XIcon aria-hidden />
						</Button>
					) : null}
				</div>
				<p
					id={statusId}
					role="status"
					className={
						filtering && matches.length === 0
							? "type-secondary px-2.5"
							: "sr-only"
					}
				>
					{filtering
						? matches.length === 0
							? `No settings match “${query.trim()}”.`
							: `${matches.length} ${matches.length === 1 ? "page matches" : "pages match"}. Press Enter to open the first.`
						: ""}
				</p>
				{filtering && matches.length === 0 && elsewhere ? (
					<p className="type-secondary px-2.5">
						Looking for {elsewhere.what}?{" "}
						<a
							href={elsewhere.href}
							onClick={(event) => {
								if (!isPlainClick(event)) return;
								event.preventDefault();
								open({ href: elsewhere.href });
							}}
							className="text-ink-1 underline decoration-line-strong underline-offset-2 hover:decoration-ink-3 focus-visible:focus-ring"
						>
							{elsewhere.where}
						</a>
					</p>
				) : null}
				{/* Below lg the section list is one picker: a strip of twenty links
				    hides the current page off-screen and has no group names. */}
				<Select
					value={active}
					items={pages.map((page) => ({
						value: page.section,
						label: page.title,
					}))}
					onValueChange={(section) => {
						const page = pages.find(
							(candidate) => candidate.section === section,
						);
						if (page) open(page);
					}}
				>
					<SelectTrigger
						aria-label={`${title} section`}
						className="w-full data-[size=default]:h-10 lg:hidden"
					>
						<SelectValue />
					</SelectTrigger>
					<SelectContent alignItemWithTrigger={false} align="start">
						{groups.map(({ group, pages: inGroup }) => (
							<SelectGroup key={group ?? "ungrouped"}>
								{group ? <SelectLabel>{group}</SelectLabel> : null}
								{inGroup.map((page) => (
									<SelectItem key={page.section} value={page.section}>
										{page.title}
									</SelectItem>
								))}
							</SelectGroup>
						))}
					</SelectContent>
				</Select>
				<nav
					aria-labelledby={labelId}
					className="hidden lg:flex lg:flex-col lg:gap-0.5"
				>
					{groups.map(({ group, pages: inGroup }, index) => (
						<div
							key={group ?? "ungrouped"}
							className={cn("flex flex-col gap-0.5", index > 0 && "mt-3")}
						>
							{group ? (
								<h3 className="type-micro px-2.5 pb-1">{group}</h3>
							) : null}
							{inGroup.map((page) => {
								const href = hrefOf(page);
								return (
									<a
										key={page.section}
										href={href}
										aria-current={page.section === active ? "page" : undefined}
										onClick={(event) => {
											if (!isPlainClick(event)) return;
											event.preventDefault();
											router.history.push(href);
										}}
										className={NAV_ITEM}
									>
										{page.title}
									</a>
								);
							})}
						</div>
					))}
				</nav>
			</div>
			<div className="min-w-0">
				{current ? (
					<SettingsSection
						key={`${area}:${current.section}`}
						id={`${area}-${current.section}`}
						title={current.title}
						description={current.description}
						docs={current.docs}
					>
						{children}
					</SettingsSection>
				) : (
					children
				)}
			</div>
		</div>
	);

	if (embedded) return layout;
	return (
		<PageFrame page={PAGE_NAME[area]} title={title} description={scopeLine}>
			{titleParts ? <ShellTitle parts={titleParts} /> : null}
			{layout}
		</PageFrame>
	);
}
