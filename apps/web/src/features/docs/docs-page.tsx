import { ApiTagList, ApiTagView } from "@nebulactrl/docs/api-reference";
import {
	type DocMeta,
	type DocPage,
	operationAnchor,
	REPO_BRANCH,
	type TocEntry,
} from "@nebulactrl/docs/types";
import {
	Link,
	useLocation,
	useParams,
	useRouter,
} from "@tanstack/react-router";
import { ArrowLeftIcon, ArrowRightIcon, PencilIcon } from "lucide-react";
import { useEffect, useMemo } from "react";
import { ToneIcon } from "#/components/common/tone-icon";
import { Skeleton } from "#/components/ui/skeleton";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { CopyMarkdownButton } from "./copy-markdown";
import { DocContent } from "./doc-content";
import { childrenOf, docMeta, docOrder, useDocPage } from "./docs-data";
import { DocsToc } from "./docs-toc";
import { LatestDocsNote } from "./latest-note";
import { RouterDocLink } from "./router-doc-link";
import { useDocsScroll } from "./scroll-context";
import "./docs.css";

// Release images are built with VITE_GITHUB_URL set to the repository. Without
// it there is no "Edit this page" link, rather than one that leads to a 404.
const REPO = import.meta.env.VITE_GITHUB_URL?.trim().replace(/\/+$/, "");

/** The page under /docs/$: looks the slug up, loads its chunk, draws body, outline, edit link and neighbours. */
export function DocsPageRoute() {
	const { _splat } = useParams({ from: "/docs/$" });
	const slug = (_splat ?? "").replace(/\/$/, "");
	const meta = docMeta(slug);
	if (!meta) return <DocNotFound slug={slug} />;
	return <DocView meta={meta} />;
}

function DocView({ meta }: { meta: DocMeta }) {
	const router = useRouter();
	const hash = useLocation({ select: (location) => location.hash });
	const scroll = useDocsScroll();
	const query = useDocPage(meta.slug);
	useDocumentTitle([meta.title, "Docs"]);

	const toc = useMemo<TocEntry[]>(() => {
		const page = query.data;
		if (!page) return [];
		if (page.kind === "api-tag") {
			return page.operations.map((op) => ({
				id: operationAnchor(op),
				text: `${op.method} ${op.path}`,
				level: 2,
			}));
		}
		return page.toc;
	}, [query.data]);

	const go = (href: string) => {
		if (href.startsWith("#")) {
			router.history.replace(`${window.location.pathname}${href}`);
			return;
		}
		router.history.push(href);
	};

	return (
		<div className="mx-auto grid w-full max-w-[1120px] grid-cols-1 gap-10 px-5 py-8 sm:px-8 xl:grid-cols-[minmax(0,1fr)_208px]">
			<article className="min-w-0">
				<div className="mb-3 flex items-center justify-between gap-3">
					<div className="type-micro">{meta.section}</div>
					<CopyMarkdownButton slug={meta.slug} />
				</div>
				{query.isPending ? (
					<PageSkeleton />
				) : query.isError || !query.data ? (
					<LoadError />
				) : (
					<Body page={query.data} meta={meta} hash={hash} onNavigate={go} />
				)}
				<PageFooter meta={meta} />
			</article>
			<aside className="hidden xl:block">
				<DocsToc entries={toc} scrollRoot={scroll} />
			</aside>
		</div>
	);
}

function Body({
	page,
	meta,
	hash,
	onNavigate,
}: {
	page: DocPage;
	meta: DocMeta;
	hash: string;
	onNavigate: (href: string) => void;
}) {
	if (page.kind === "api-tag") {
		return <ApiTagBody page={page} title={meta.title} hash={hash} />;
	}
	return (
		<>
			<DocContent
				html={page.html}
				hash={hash || undefined}
				onNavigate={onNavigate}
			/>
			{page.kind === "api" ? (
				<ApiTagList tags={childrenOf(page.slug)} Link={RouterDocLink} />
			) : null}
		</>
	);
}

function ApiTagBody({
	page,
	title,
	hash,
}: {
	page: Extract<DocPage, { kind: "api-tag" }>;
	title: string;
	hash: string;
}) {
	// Operation cards are React, not compiled HTML: scroll to a deep link once they exist.
	useEffect(() => {
		if (!hash) return;
		const frame = requestAnimationFrame(() =>
			document.getElementById(hash)?.scrollIntoView({ block: "start" }),
		);
		return () => cancelAnimationFrame(frame);
	}, [hash]);
	return <ApiTagView page={page} title={title} Link={RouterDocLink} />;
}

function PageSkeleton() {
	return (
		<div className="flex max-w-[72ch] flex-col gap-3" aria-busy="true">
			<span className="sr-only" role="status">
				Loading page
			</span>
			<Skeleton className="h-8 w-2/3" />
			<Skeleton className="h-4 w-full" />
			<Skeleton className="h-4 w-11/12" />
			<Skeleton className="h-4 w-5/6" />
			<Skeleton className="mt-4 h-24 w-full" />
		</div>
	);
}

function LoadError() {
	return (
		<div className="docs-callout" data-tone="danger" role="alert">
			<ToneIcon tone="danger" />
			<div className="docs-callout-body">
				<p className="docs-callout-title">This page could not load</p>
				<p>
					The file may have been replaced by a newer version of the control
					plane. Reload the page to try again.
				</p>
			</div>
		</div>
	);
}

function DocNotFound({ slug }: { slug: string }) {
	useDocumentTitle(["Page not found", "Docs"]);
	return (
		<div className="mx-auto max-w-[72ch] px-5 py-16 sm:px-8">
			<div className="type-micro mb-3">Docs</div>
			<h1 className="m-0 mb-3 font-display font-semibold text-[28px] text-ink-1 tracking-[-0.02em]">
				No page at /docs/{slug}
			</h1>
			<p className="text-[14px] text-ink-2">
				It may have moved or never existed. Search the docs with the field
				above, or start from the{" "}
				<Link to="/docs" className="text-accent-ink">
					overview
				</Link>
				.
			</p>
		</div>
	);
}

function PageFooter({ meta }: { meta: DocMeta }) {
	const index = docOrder.findIndex((candidate) => candidate.slug === meta.slug);
	const previous = index > 0 ? docOrder[index - 1] : undefined;
	const next = index >= 0 ? docOrder[index + 1] : undefined;
	return (
		<footer className="mt-12 max-w-[72ch] border-line border-t pt-4">
			<div className="mb-5 text-ink-3 text-xs">
				{meta.source && !meta.generated && REPO ? (
					<a
						href={`${REPO}/edit/${REPO_BRANCH}/${meta.source}`}
						target="_blank"
						rel="noreferrer noopener"
						className="inline-flex items-center gap-1.5 text-ink-3 no-underline hover:text-ink-1"
					>
						<PencilIcon aria-hidden className="size-3" />
						Edit this page on GitHub
					</a>
				) : (
					<span>
						{meta.kind === "api-tag"
							? "Generated from the OpenAPI spec (openapi/openapi.json)."
							: meta.source
								? `Generated from the source code (${meta.source}); do not edit by hand.`
								: ""}
					</span>
				)}
			</div>
			<LatestDocsNote className="mb-5 text-ink-3 text-xs" />
			<div className="grid gap-3 sm:grid-cols-2">
				{previous ? (
					<Neighbour meta={previous} direction="previous" />
				) : (
					<span />
				)}
				{next ? <Neighbour meta={next} direction="next" /> : null}
			</div>
		</footer>
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
		<Link
			to="/docs/$"
			params={{ _splat: meta.slug }}
			className={`surface-raised flex flex-col gap-0.5 rounded-card px-3.5 py-2.5 no-underline outline-none hover:bg-surface-float focus-visible:focus-ring ${forward ? "items-end text-right sm:col-start-2" : ""}`}
		>
			<span className="type-micro flex items-center gap-1.5">
				{forward ? null : <ArrowLeftIcon aria-hidden className="size-3" />}
				{forward ? "Next" : "Previous"}
				{forward ? <ArrowRightIcon aria-hidden className="size-3" /> : null}
			</span>
			<span className="font-medium text-[13px] text-ink-1">{meta.title}</span>
		</Link>
	);
}
