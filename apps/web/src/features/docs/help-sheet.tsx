import { docHref } from "@nebulactrl/docs/types";
import { ArrowLeftIcon, ExternalLinkIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "#/components/ui/button";
import {
	Sheet,
	SheetBody,
	SheetContent,
	SheetDescription,
	SheetHeader,
	SheetTitle,
} from "#/components/ui/sheet";
import { Skeleton } from "#/components/ui/skeleton";
import { DocContent } from "./doc-content";
import { docMeta, useDocPage } from "./docs-data";

interface Target {
	slug: string;
	anchor: string;
}

/** `/docs/operations/backups#x` → its target, or null for anything else. */
function parseDocsHref(href: string): Target | null {
	if (href !== "/docs" && !href.startsWith("/docs/")) return null;
	const hash = href.indexOf("#");
	const path = hash < 0 ? href : href.slice(0, hash);
	return {
		slug: path.replace(/^\/docs\/?/, "").replace(/\/$/, ""),
		anchor: hash < 0 ? "" : href.slice(hash + 1),
	};
}

/**
 * The docs reader as a sheet over the current screen: one page at a time,
 * with links between docs pages followed inside the sheet (a back button
 * returns) and "Open full page" for the real thing in a new tab.
 */
export function HelpSheet({
	slug,
	anchor,
	open,
	onOpenChange,
}: {
	slug: string;
	anchor: string;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const [trail, setTrail] = useState<Target[]>([{ slug, anchor }]);
	// Opening the sheet from another link starts a new trail.
	useEffect(() => {
		if (open) setTrail([{ slug, anchor }]);
	}, [open, slug, anchor]);

	const current = trail[trail.length - 1] ?? { slug, anchor };
	const meta = docMeta(current.slug);
	const query = useDocPage(current.slug);
	const fullHref = docHref(current.slug, current.anchor);

	const follow = (href: string) => {
		const target = parseDocsHref(href);
		if (!target) return; // an in-page anchor: DocContent already scrolled
		if (!docMeta(target.slug)) {
			window.open(href, "_blank", "noopener");
			return;
		}
		setTrail((previous) => [...previous, target]);
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent side="right" width={620} className="max-w-[100vw]">
				<SheetHeader className="pb-3">
					<div className="flex items-center gap-2 pr-8">
						{trail.length > 1 ? (
							<Button
								variant="ghost"
								size="icon-xs"
								aria-label="Back"
								onClick={() => setTrail((previous) => previous.slice(0, -1))}
							>
								<ArrowLeftIcon />
							</Button>
						) : null}
						<div className="min-w-0">
							<div className="type-micro">Documentation</div>
							<SheetTitle className="m-0 truncate font-semibold text-[15px] text-ink-1">
								{meta?.title ?? "Documentation"}
							</SheetTitle>
						</div>
						<Button
							variant="secondary"
							size="sm"
							className="ml-auto shrink-0"
							nativeButton={false}
							render={
								<a href={fullHref} target="_blank" rel="noreferrer noopener" />
							}
						>
							<ExternalLinkIcon />
							Open full page
						</Button>
					</div>
					<SheetDescription className="sr-only">
						{meta?.description ?? "A page of the NebulaCtrl documentation."}
					</SheetDescription>
				</SheetHeader>
				<SheetBody data-docs-scroll className="px-5 py-4">
					{!meta ? (
						<p className="text-[13px] text-ink-3">
							This page is not part of this version of the docs.{" "}
							<a
								href="/docs"
								target="_blank"
								rel="noreferrer noopener"
								className="text-accent-ink"
							>
								Browse the documentation
							</a>
							.
						</p>
					) : query.isPending ? (
						<div className="flex flex-col gap-3" aria-busy="true">
							<span className="sr-only" role="status">
								Loading page
							</span>
							<Skeleton className="h-7 w-2/3" />
							<Skeleton className="h-4 w-full" />
							<Skeleton className="h-4 w-5/6" />
							<Skeleton className="mt-3 h-20 w-full" />
						</div>
					) : query.isError || !query.data ? (
						<p className="text-[13px] text-ink-3">
							This page could not load. Try{" "}
							<a
								href={fullHref}
								target="_blank"
								rel="noreferrer noopener"
								className="text-accent-ink"
							>
								opening the full page
							</a>
							.
						</p>
					) : query.data.kind === "api-tag" ? (
						<p className="text-[13px] text-ink-3">
							The API reference is easier to read on its own page.{" "}
							<a
								href={fullHref}
								target="_blank"
								rel="noreferrer noopener"
								className="text-accent-ink"
							>
								Open full page
							</a>
							.
						</p>
					) : (
						<DocContent
							html={query.data.html}
							hash={current.anchor || undefined}
							onNavigate={follow}
						/>
					)}
				</SheetBody>
			</SheetContent>
		</Sheet>
	);
}
