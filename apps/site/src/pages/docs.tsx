import { ApiTagList } from "@nebulactrl/docs/api-reference";
import type {
	DocMeta,
	NavItem,
	NavSection,
	TocEntry,
} from "@nebulactrl/docs/types";
import { operationAnchor } from "@nebulactrl/docs/types";
import { ArrowLeftIcon } from "@phosphor-icons/react/dist/csr/ArrowLeft";
import { ArrowRightIcon } from "@phosphor-icons/react/dist/csr/ArrowRight";
import { BookOpenIcon } from "@phosphor-icons/react/dist/csr/BookOpen";
import { FileTextIcon } from "@phosphor-icons/react/dist/csr/FileText";
import { PencilSimpleIcon as PencilIcon } from "@phosphor-icons/react/dist/csr/PencilSimple";
import { RobotIcon as BotIcon } from "@phosphor-icons/react/dist/csr/Robot";
import { Document } from "../components/chrome.tsx";
import ApiTag from "../islands/api-tag.tsx";
import CopyButton from "../islands/copy-button.tsx";
import DocsSearch from "../islands/docs-search.tsx";
import DocsToc from "../islands/docs-toc.tsx";
import { Island } from "../islands/island.tsx";
import MenuToggle from "../islands/menu-toggle.tsx";
import type { SiteData } from "../server/site-data.ts";
import type { RenderContext } from "../site.ts";

const BLURBS: Record<string, string> = {
	"Getting started":
		"What NebulaCtrl is, the ideas it is built on, and your first deploy.",
	Installing:
		"Put the control plane on a machine, keep it updated, remove it cleanly.",
	Applications:
		"Git and image sources, builds, variables, domains, previews, scaling and tasks.",
	"Databases and backups": "Managed databases, scheduled backups and restores.",
	Templates: "One click catalogue of self hosted apps.",
	Servers:
		"Connect hosts, how the agent and proxy work, hardening and clean up.",
	Observability: "Logs, metrics, analytics and notifications.",
	"Teams and security":
		"Roles, sign in methods, SSO, secret managers and the threat model.",
	"Platform services": "The services NebulaCtrl can run for itself.",
	Reference: "Every CLI command and API endpoint.",
	Troubleshooting: "When something does not work, and what is not supported.",
	Internals: "Architecture decisions and why they were made.",
};

function flatten(items: NavItem[]): string[] {
	return items.flatMap((item) => [item.slug, ...flatten(item.children)]);
}

/** Pages in reading order: section by section, parents before their children. */
function docOrder(data: SiteData): DocMeta[] {
	return data.corpus.nav
		.flatMap((section) => flatten(section.items))
		.flatMap((slug) => {
			const entry = data.corpus.entries.get(slug);
			return entry ? [entry.meta] : [];
		});
}

function childrenOf(data: SiteData, slug: string): DocMeta[] {
	return [...data.corpus.entries.values()]
		.map((entry) => entry.meta)
		.filter((meta) => meta.parent === slug)
		.sort((a, b) => a.order - b.order);
}

function Sidebar({ nav, current }: { nav: NavSection[]; current: string }) {
	const item = (entry: NavItem, depth: number) => (
		<li key={entry.slug}>
			<a
				href={`/docs/${entry.slug}`}
				{...(entry.slug === current ? { "aria-current": "page" as const } : {})}
				className={`block rounded-lg py-1.5 pr-3 text-sm no-underline hover:text-ink-1 ${depth ? "pl-6" : "pl-3"} ${entry.slug === current ? "text-ink-1" : "text-ink-3"}`}
			>
				{entry.title}
			</a>
			{entry.children.length > 0 ? (
				<ul className="m-0 list-none p-0">
					{entry.children.map((child) => item(child, depth + 1))}
				</ul>
			) : null}
		</li>
	);
	return (
		<nav aria-label="Docs" className="flex flex-col gap-6 px-4 py-6">
			{nav.map((section) => (
				<div key={section.id}>
					<h2 className="type-micro mb-2 px-3">{section.title}</h2>
					<ul className="m-0 flex list-none flex-col p-0">
						{section.items.map((entry) => item(entry, 0))}
					</ul>
				</div>
			))}
		</nav>
	);
}

/** The docs frame: a bar with the search, the section sidebar (a drawer below 1024px) and the page. */
function DocsShell({
	data,
	current,
	toc,
	children,
}: {
	data: SiteData;
	current: string;
	toc: TocEntry[];
	children: React.ReactNode;
}) {
	return (
		<>
			<div className="page-bar sticky top-0 z-30">
				<div className="mx-auto flex h-14 max-w-[1440px] items-center gap-2 px-4 sm:px-8">
					<div className="js-only lg:hidden">
						<Island
							name="menu"
							props={{
								controls: "docs-sidebar",
								label: "docs contents",
								text: "Contents",
							}}
						>
							<MenuToggle
								controls="docs-sidebar"
								label="docs contents"
								text="Contents"
							/>
						</Island>
					</div>
					<div className="js-only flex min-w-0 flex-1 justify-center lg:justify-start">
						<Island name="docs-search" className="flex w-full min-w-0 max-w-md">
							<DocsSearch />
						</Island>
					</div>
					<a
						href="/docs"
						className="ml-auto hidden text-ink-3 text-sm no-underline hover:text-ink-1 sm:inline"
					>
						All docs
					</a>
				</div>
			</div>
			<div className="mx-auto grid max-w-[1440px] grid-cols-1 lg:grid-cols-[272px_minmax(0,1fr)] xl:grid-cols-[272px_minmax(0,1fr)_208px]">
				<aside
					id="docs-sidebar"
					data-open="false"
					className="drawer-panel docs-shell-sidebar border-line-faint border-b lg:sticky lg:top-14 lg:block! lg:h-[calc(100vh-56px)] lg:overflow-y-auto lg:border-r lg:border-b-0"
				>
					<Sidebar nav={data.corpus.nav} current={current} />
				</aside>
				<main
					id="main"
					className="page-surface min-w-0 px-4 py-8 sm:px-8 lg:py-12"
				>
					{children}
				</main>
				<aside className="hidden xl:block">
					{toc.length > 0 ? (
						<Island name="docs-toc" props={{ entries: toc }}>
							<DocsToc entries={toc} />
						</Island>
					) : null}
				</aside>
			</div>
		</>
	);
}

export function DocPageView({
	ctx,
	data,
	slug,
}: {
	ctx: RenderContext;
	data: SiteData;
	slug: string;
}) {
	const entry = data.corpus.entries.get(slug);
	if (!entry) throw new Error(`no docs page "${slug}"`);
	const { meta, page } = entry;
	const order = docOrder(data);
	const at = order.findIndex((m) => m.slug === slug);
	const previous = at > 0 ? order[at - 1] : undefined;
	const next = at >= 0 ? order[at + 1] : undefined;
	const toc: TocEntry[] =
		page.kind === "api-tag"
			? page.operations.map((op) => ({
					id: operationAnchor(op),
					text: `${op.method} ${op.path}`,
					level: 2 as const,
				}))
			: page.toc;
	const github = ctx.config.githubUrl;

	return (
		<Document
			ctx={ctx}
			current="docs"
			floating={false}
			dawn
			meta={{
				title: `${meta.title} | NebulaCtrl docs`,
				description: meta.description,
				path: `/docs/${slug}`,
				ogType: "article",
			}}
		>
			<DocsShell data={data} current={slug} toc={toc}>
				<article>
					<div className="mb-3 flex items-center justify-between gap-4">
						<p className="type-micro m-0">{meta.section}</p>
						<a
							href={`/docs/${slug}.md`}
							className="inline-flex items-center gap-2 text-ink-3 text-xs no-underline hover:text-ink-1"
						>
							<FileTextIcon aria-hidden className="size-3" />
							View as Markdown
						</a>
					</div>
					{page.kind === "api-tag" ? (
						<Island
							name="api-tag"
							props={{ page, title: meta.title }}
							mode="visible"
							className="block"
						>
							<ApiTag page={page} title={meta.title} />
						</Island>
					) : (
						<>
							<div
								className="docs-prose"
								// biome-ignore lint/security/noDangerouslySetInnerHtml: compiled at build time from repository markdown; the compiler escapes raw HTML
								dangerouslySetInnerHTML={{ __html: page.html }}
							/>
							{page.kind === "api" ? (
								<ApiTagList tags={childrenOf(data, page.slug)} />
							) : null}
						</>
					)}
					<footer className="mt-12 max-w-[72ch] border-line border-t pt-4">
						<p className="mb-5 text-ink-3 text-xs">
							{meta.source && !meta.generated ? (
								github ? (
									<a
										href={`${github}/edit/main/${meta.source}`}
										className="inline-flex items-center gap-2 text-ink-3 no-underline hover:text-ink-1"
									>
										<PencilIcon aria-hidden className="size-3" />
										Edit this page on GitHub
									</a>
								) : null
							) : meta.kind === "api-tag" ? (
								"Generated from the OpenAPI spec."
							) : meta.source ? (
								"Generated from the source code; do not edit by hand."
							) : null}
						</p>
						<div className="grid gap-3 sm:grid-cols-2">
							{previous ? (
								<Neighbour meta={previous} direction="previous" />
							) : (
								<span />
							)}
							{next ? <Neighbour meta={next} direction="next" /> : null}
						</div>
					</footer>
				</article>
			</DocsShell>
		</Document>
	);
}

function Neighbour({
	meta,
	direction,
}: {
	meta: DocMeta;
	direction: "previous" | "next";
}) {
	const forward = direction === "next";
	return (
		<a
			href={`/docs/${meta.slug}`}
			className={`surface-raised flex flex-col gap-1 rounded-xl px-4 py-3 no-underline hover:bg-surface-float ${forward ? "items-end text-right sm:col-start-2" : ""}`}
		>
			<span className="type-micro flex items-center gap-2">
				{forward ? null : <ArrowLeftIcon aria-hidden className="size-3" />}
				{forward ? "Next" : "Previous"}
				{forward ? <ArrowRightIcon aria-hidden className="size-3" /> : null}
			</span>
			<span className="font-semibold text-ink-1 text-sm">{meta.title}</span>
		</a>
	);
}

export function DocsHomeView({
	ctx,
	data,
}: {
	ctx: RenderContext;
	data: SiteData;
}) {
	const nav = data.corpus.nav;
	const start = (nav[0]?.items ?? []).slice(0, 3);
	const llms = `${ctx.config.siteUrl}/llms.txt`;
	return (
		<Document
			ctx={ctx}
			current="docs"
			floating={false}
			dawn
			meta={{
				title: "NebulaCtrl documentation",
				description:
					"Install NebulaCtrl, connect servers, deploy from Git or an image, and look up every CLI command and API endpoint.",
				path: "/docs",
			}}
		>
			<DocsShell data={data} current="" toc={[]}>
				<div className="max-w-[860px]">
					<h1 className="m-0 font-semibold text-4xl tracking-tight">
						NebulaCtrl documentation
					</h1>
					<p className="mt-4 max-w-[60ch] text-ink-2 text-lg">
						Install it, connect servers, deploy, and look up every CLI command
						and API endpoint. These are the latest docs. The same pages ship
						inside each release of the control plane, so an installed version
						always has the pages that match it.
					</p>
					<div className="mt-8 grid gap-3 sm:grid-cols-3">
						{start.map((item, index) => (
							<a
								key={item.slug}
								href={`/docs/${item.slug}`}
								className="surface-raised flex flex-col gap-1 rounded-xl px-4 py-4 no-underline hover:bg-surface-float"
							>
								<span className="type-micro">{`Step ${index + 1}`}</span>
								<span className="font-semibold text-base text-ink-1">
									{item.title}
								</span>
								<span className="text-ink-3 text-xs">
									{data.corpus.entries.get(item.slug)?.meta.description}
								</span>
							</a>
						))}
					</div>
					<section
						aria-labelledby="docs-agents"
						className="surface-raised mt-8 flex flex-col gap-3 rounded-xl p-4"
					>
						<h2
							id="docs-agents"
							className="m-0 flex items-center gap-2 font-semibold text-base text-ink-1"
						>
							<BotIcon aria-hidden className="size-4 text-ink-3" />
							For AI agents
						</h2>
						<p className="m-0 max-w-[68ch] text-ink-2 text-sm">
							Point a coding agent at the index below. Every page also reads as
							plain markdown: add{" "}
							<code className="font-mono text-ink-1">.md</code> to its address,
							or use <a href="/llms-full.txt">llms-full.txt</a> for everything
							in one file. The REST API is described by the{" "}
							<a href="/api/v1/openapi.json">OpenAPI document</a>.
						</p>
						<div className="flex items-center gap-2">
							<code className="surface-well min-w-0 flex-1 truncate rounded-lg px-3 py-2 font-mono text-ink-1 text-xs">
								{llms}
							</code>
							<div className="js-only">
								<Island
									name="copy"
									mode="visible"
									props={{ text: llms, label: "Copy the llms.txt address" }}
								>
									<CopyButton text={llms} label="Copy the llms.txt address" />
								</Island>
							</div>
						</div>
					</section>
					<div className="mt-12 grid gap-x-8 gap-y-8 sm:grid-cols-2">
						{nav.map((section) => (
							<section key={section.id}>
								<h2 className="m-0 mb-1 flex items-center gap-2 font-semibold text-base text-ink-1">
									<BookOpenIcon aria-hidden className="size-4 text-ink-4" />
									{section.title}
								</h2>
								<p className="m-0 mb-2 text-ink-3 text-xs">
									{BLURBS[section.title]}
								</p>
								<ul className="m-0 flex list-none flex-col gap-1 p-0">
									{section.items.slice(0, 6).map((item) => (
										<li key={item.slug}>
											<a
												href={`/docs/${item.slug}`}
												className="text-ink-2 text-sm no-underline hover:text-accent-ink"
											>
												{item.title}
											</a>
										</li>
									))}
									{section.items.length > 6 ? (
										<li className="text-ink-3 text-xs">
											and {section.items.length - 6} more in the sidebar
										</li>
									) : null}
								</ul>
							</section>
						))}
					</div>
				</div>
			</DocsShell>
		</Document>
	);
}
