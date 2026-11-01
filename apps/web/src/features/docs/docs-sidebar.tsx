import type { NavItem } from "@nebulactrl/docs/types";
import { Link } from "@tanstack/react-router";
import { cn } from "cn";
import { useEffect, useRef, useState } from "react";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { docNav } from "./docs-data";

const ITEM =
	"flex min-h-7 items-center rounded-ctl px-2.5 py-1 text-[13px] leading-snug no-underline outline-none focus-visible:focus-ring";

/** The docs sidebar: sections in reading order; a page with children (the API reference) nests them while it or a child is open. */
export function DocsSidebar({
	current,
	onNavigate,
}: {
	current: string;
	onNavigate?: () => void;
}) {
	const ref = useRef<HTMLElement>(null);
	// Keep the open page in view when the sidebar is long and the page was reached by a link or search.
	// biome-ignore lint/correctness/useExhaustiveDependencies: `current` is the trigger
	useEffect(() => {
		ref.current
			?.querySelector('[aria-current="page"]')
			?.scrollIntoView({ block: "nearest" });
	}, [current]);
	return (
		<nav
			ref={ref}
			aria-label="Documentation"
			className="flex flex-col gap-3.5 px-3 py-4"
		>
			{docNav.map((section) => (
				<Section
					key={section.id}
					title={section.title}
					items={section.items}
					current={current}
					onNavigate={onNavigate}
				/>
			))}
		</nav>
	);
}

function contains(item: NavItem, slug: string): boolean {
	return (
		item.slug === slug || item.children.some((child) => contains(child, slug))
	);
}

function Section({
	title,
	items,
	current,
	onNavigate,
}: {
	title: string;
	items: NavItem[];
	current: string;
	onNavigate: (() => void) | undefined;
}) {
	const active = items.some((item) => contains(item, current));
	const [collapsed, setCollapsed] = useState(false);
	const open = active || !collapsed;
	return (
		<div>
			<button
				type="button"
				aria-expanded={open}
				onClick={() => setCollapsed(open)}
				className="type-micro flex w-full cursor-pointer items-center gap-1 rounded-ctl px-2.5 py-1 text-left outline-none hover:text-ink-2 focus-visible:focus-ring"
			>
				<DisclosureChevron open={open} className="size-3" />
				{title}
			</button>
			<DisclosurePanel open={open}>
				<ul className="m-0 mt-0.5 flex list-none flex-col gap-px p-0">
					{items.map((item) => (
						<Entry
							key={item.slug}
							item={item}
							current={current}
							onNavigate={onNavigate}
						/>
					))}
				</ul>
			</DisclosurePanel>
		</div>
	);
}

function Entry({
	item,
	current,
	onNavigate,
	nested = false,
}: {
	item: NavItem;
	current: string;
	onNavigate: (() => void) | undefined;
	nested?: boolean;
}) {
	const here = item.slug === current;
	const expanded = item.children.length > 0 && contains(item, current);
	return (
		<li>
			<Link
				to="/docs/$"
				params={{ _splat: item.slug }}
				onClick={onNavigate}
				aria-current={here ? "page" : undefined}
				className={cn(
					ITEM,
					nested && "text-xs",
					here
						? "bg-surface-raised font-medium text-ink-1"
						: "text-ink-3 hover:text-ink-1",
				)}
			>
				{item.title}
			</Link>
			{expanded ? (
				<ul className="m-0 mt-px mb-1 ml-3 flex list-none flex-col gap-px border-line border-l p-0 pl-1.5">
					{item.children.map((child) => (
						<Entry
							key={child.slug}
							item={child}
							current={current}
							onNavigate={onNavigate}
							nested
						/>
					))}
				</ul>
			) : null}
		</li>
	);
}
