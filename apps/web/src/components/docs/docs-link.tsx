import { docHref } from "@nebulactrl/docs/types";
import { cn } from "cn";
import { BookOpenIcon } from "lucide-react";
import { lazy, type ReactNode, Suspense, useState } from "react";
import { Button } from "#/components/ui/button";

// The help sheet is the whole docs reader; it loads the first time someone
// asks for it, so a page with a "Learn more" link pays for a button only.
const HelpSheet = lazy(() =>
	import("#/features/docs/help-sheet").then((module) => ({
		default: module.HelpSheet,
	})),
);

/** "operations/backups#destinations" → its slug and anchor. */
export function splitDocTarget(to: string): { slug: string; anchor: string } {
	const hash = to.indexOf("#");
	const slug = (hash < 0 ? to : to.slice(0, hash)).replace(/^\/?(docs\/)?/, "");
	return {
		slug: slug.replace(/\/$/, ""),
		anchor: hash < 0 ? "" : to.slice(hash + 1),
	};
}

/** A docs file path as written in the repo ("operations/monitoring.md", "installing/README.md") → a docs target. */
export function docTargetFromPath(path: string): string {
	return path.replace(/\/README\.md$/i, "").replace(/\.md$/i, "");
}

/**
 * A "Learn more" link to a docs page. It opens the page in a help sheet over
 * the current screen, so the reader keeps their place (and any unsaved
 * form); the sheet's "Open full page" goes to /docs in a new tab. A
 * modified click (new tab, new window) goes straight to the page.
 *
 * `to` is the docs slug with an optional anchor: "operations/backups#destinations".
 */
export function DocsLink({
	to,
	children = "Learn more",
	className,
	variant = "link",
}: {
	to: string;
	children?: ReactNode;
	className?: string;
	variant?: "link" | "button";
}) {
	const [open, setOpen] = useState(false);
	const [mounted, setMounted] = useState(false);
	const { slug, anchor } = splitDocTarget(to);
	const href = docHref(slug, anchor);

	const onClick = (event: React.MouseEvent<HTMLAnchorElement>) => {
		if (
			event.metaKey ||
			event.ctrlKey ||
			event.shiftKey ||
			event.altKey ||
			event.button !== 0
		) {
			return;
		}
		event.preventDefault();
		setMounted(true);
		setOpen(true);
	};

	const content = (
		<>
			<BookOpenIcon aria-hidden />
			{children}
			<span className="sr-only">
				{" "}
				(opens the documentation beside this page)
			</span>
		</>
	);

	return (
		<>
			{variant === "button" ? (
				<Button
					variant="secondary"
					nativeButton={false}
					className={className}
					render={<a href={href} onClick={onClick} />}
				>
					{content}
				</Button>
			) : (
				<a
					href={href}
					onClick={onClick}
					className={cn(
						"inline-flex items-center gap-1 text-accent-ink text-xs no-underline outline-none hover:text-ink-1 focus-visible:focus-ring [&_svg]:size-3",
						className,
					)}
				>
					{content}
				</a>
			)}
			{mounted ? (
				<Suspense fallback={null}>
					<HelpSheet
						slug={slug}
						anchor={anchor}
						open={open}
						onOpenChange={setOpen}
					/>
				</Suspense>
			) : null}
		</>
	);
}
