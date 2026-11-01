import { mergeProps } from "@base-ui/react/merge-props";
import { useRender } from "@base-ui/react/use-render";
import { cn } from "cn";
import { type ComponentPropsWithRef, useRef } from "react";
import { fadeMask, useEdgeFade } from "#/lib/hooks/use-edge-fade";
import { useTabGlide } from "./tab-glide";
import { tabsListVariants, tabsTriggerVariants } from "./tabs";

type NavTabsProps = Omit<ComponentPropsWithRef<"nav">, "aria-label" | "ref"> & {
	/** Names the bar for assistive tech: "Server sections". */
	"aria-label": string;
};

/**
 * A page's tab bar whose tabs are links to routes (the server page: Overview,
 * Traffic, Proxy…). It is a `nav` of links with `aria-current="page"` on the
 * one you are on, not an ARIA tablist: a tablist promises a panel per tab and
 * arrow-key roving, and a link bar has neither (every tab is its own page, so
 * Tab walks the links and a link opens in a new window). It looks the same as
 * `Tabs variant="underline"`: one glowing bar glides between the tabs, a strip
 * wider than its room scrolls sideways with the cut end fading, and the tab
 * you are on is kept in view.
 */
function NavTabs({ className, children, style, ...props }: NavTabsProps) {
	const listRef = useRef<HTMLElement>(null);
	const indicatorRef = useRef<HTMLSpanElement>(null);
	const fade = useEdgeFade(listRef);
	const mask = fadeMask(fade);
	useTabGlide(listRef, indicatorRef, {
		active: '[aria-current="page"]',
		items: "a",
		attributes: ["aria-current"],
		thumb: false,
		scrolls: true,
		enabled: true,
	});

	return (
		<nav
			ref={listRef}
			data-slot="nav-tabs"
			data-glide="u"
			className={cn(
				tabsListVariants({ variant: "underline" }),
				"relative",
				className,
			)}
			style={
				mask ? { maskImage: mask, WebkitMaskImage: mask, ...style } : style
			}
			{...props}
		>
			<span
				ref={indicatorRef}
				data-ind="1"
				aria-hidden="true"
				className="t-tabs-pill pointer-events-none absolute bottom-[-1px] left-0 z-10 h-0 w-0 rounded-[2px] bg-accent-ink opacity-0 shadow-glow-accent"
			/>
			{children}
		</nav>
	);
}

type NavTabProps = useRender.ComponentProps<"a"> & {
	/** This tab's page is the one open. */
	active: boolean;
};

/** One link of `NavTabs`: pass the router's `Link` as `render`. */
function NavTab({ active, className, render, ...props }: NavTabProps) {
	return useRender({
		defaultTagName: "a",
		render,
		props: mergeProps<"a">(
			{
				"aria-current": active ? "page" : undefined,
				className: cn(
					tabsTriggerVariants({ variant: "underline" }),
					"z-20 aria-[current=page]:border-transparent aria-[current=page]:text-ink-1",
					className,
				),
			} as ComponentPropsWithRef<"a">,
			props,
		),
		state: { slot: "nav-tab", active },
	});
}

export { NavTab, NavTabs };
