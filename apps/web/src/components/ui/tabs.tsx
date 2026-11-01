import { Tabs as TabsPrimitive } from "@base-ui/react/tabs";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";
import {
	createContext,
	type MutableRefObject,
	useCallback,
	useContext,
	useRef,
} from "react";
import { fadeMask, useEdgeFade } from "#/lib/hooks/use-edge-fade";
import { useTabGlide } from "./tab-glide";

function Tabs({
	className,
	orientation = "horizontal",
	...props
}: TabsPrimitive.Root.Props) {
	return (
		<TabsPrimitive.Root
			data-slot="tabs"
			data-orientation={orientation}
			className={cn("group/tabs flex data-horizontal:flex-col", className)}
			{...props}
		/>
	);
}

/*
 * Four tab looks. The lit edge (or the raised thumb) is the state and the
 * text colour is the second cue, so selection never rests on colour alone.
 * - default: the compact in-page row on a hairline, active tab at 600, a
 *   static 2px accent-ink bar.
 * - underline: the console's in-page tabs, 40px tall, 22px apart on a
 *   hairline, medium weight both ways so switching never reflows the row;
 *   one glowing accent-ink bar glides between tabs.
 * - sheet: the service panel's tabs, 36px tall, 28px apart, no hairline.
 * - pressed: a well track with a raised thumb gliding behind the selected tab
 *   (the segmented control's look, for a tab strip).
 * Route-level tab bars (links, not Base UI tabs) are `NavTabs`
 * (nav-tabs.tsx): the same underline look on a `nav` of links with
 * `aria-current="page"` and no tab roles.
 */
const tabsListVariants = cva(
	"flex items-end overflow-x-auto border-b no-scrollbar",
	{
		variants: {
			variant: {
				default: "gap-0.5 border-line",
				underline: "gap-[22px] border-line",
				// The service panel's row: 28px apart, no rule of its own (the
				// header above it carries the divider).
				sheet: "gap-7 border-b-0 overflow-hidden",
				pressed:
					"w-fit max-w-full items-center gap-0.5 overflow-x-auto rounded-[10px] border border-line-control bg-surface-well p-0.5 shadow-well no-scrollbar",
			},
		},
		defaultVariants: { variant: "default" },
	},
);

const tabsTriggerVariants = cva(
	"-mb-px inline-flex shrink-0 cursor-pointer items-center whitespace-nowrap border-transparent border-b-2 text-ink-3 text-sm transition-[color,border-color] duration-quick ease-out hover:text-ink-1 focus-visible:focus-ring disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-3.5",
	{
		variants: {
			variant: {
				default: "gap-1.5 px-3 py-2 font-normal leading-[1.3]",
				underline: "h-10 gap-2 px-px font-medium leading-none",
				sheet: "-mb-0 h-9 gap-2 px-px font-medium leading-none",
				pressed:
					"mb-0 h-7 gap-1.5 rounded-[7px] border-0 px-3 font-medium text-xs leading-none",
			},
			active: {
				true: "text-ink-1",
				false: "",
			},
		},
		compoundVariants: [
			{
				variant: "default",
				active: true,
				className: "border-accent-ink font-semibold",
			},
			{ variant: "underline", active: true, className: "border-accent-ink" },
			{ variant: "sheet", active: true, className: "border-accent-ink" },
			{ variant: "pressed", active: true, className: "thumb-raised" },
		],
		defaultVariants: { variant: "default", active: false },
	},
);

type TabsVariant = NonNullable<
	VariantProps<typeof tabsListVariants>["variant"]
>;

const TabsVariantContext = createContext<TabsVariant>("default");

function TabsList({
	className,
	variant = "default",
	children,
	ref: forwardedRef,
	...props
}: TabsPrimitive.List.Props & { variant?: TabsVariant }) {
	const isPressed = variant === "pressed";
	const isGlide = variant === "underline" || variant === "sheet" || isPressed;
	const listRef = useRef<HTMLDivElement>(null);
	const indicatorRef = useRef<HTMLSpanElement>(null);
	const setListRef = useCallback(
		(node: HTMLDivElement | null) => {
			listRef.current = node;
			if (typeof forwardedRef === "function") return forwardedRef(node);
			if (forwardedRef)
				(forwardedRef as MutableRefObject<HTMLDivElement | null>).current =
					node;
		},
		[forwardedRef],
	);

	// A strip wider than its room scrolls sideways: the ends that continue
	// fade out, and the selected tab is kept in view.
	const scrolls = isGlide && variant !== "sheet";
	const fade = useEdgeFade(listRef);
	const mask = scrolls ? fadeMask(fade) : undefined;

	useTabGlide(listRef, indicatorRef, {
		active: '[role="tab"][data-active]',
		items: '[role="tab"]',
		attributes: ["data-active", "aria-selected"],
		thumb: isPressed,
		scrolls,
		enabled: isGlide,
	});

	return (
		<TabsVariantContext.Provider value={variant}>
			<TabsPrimitive.List
				ref={setListRef}
				data-slot="tabs-list"
				data-variant={variant}
				data-glide={isGlide ? "u" : undefined}
				className={cn(
					tabsListVariants({ variant }),
					isGlide && "relative",
					className,
				)}
				style={mask ? { maskImage: mask, WebkitMaskImage: mask } : undefined}
				{...props}
			>
				{isGlide ? (
					<span
						ref={indicatorRef}
						data-ind="1"
						aria-hidden="true"
						className={cn(
							"t-tabs-pill pointer-events-none absolute left-0 w-0 opacity-0",
							isPressed
								? "thumb-raised top-0 z-0 h-0 rounded-[7px]"
								: "z-10 h-0 rounded-[2px] bg-accent-ink shadow-glow-accent",
							variant === "sheet" && "bottom-0",
							variant === "underline" && "bottom-[-1px]",
						)}
					/>
				) : null}
				{children}
			</TabsPrimitive.List>
		</TabsVariantContext.Provider>
	);
}

function TabsTrigger({ className, ...props }: TabsPrimitive.Tab.Props) {
	const variant = useContext(TabsVariantContext);
	return (
		<TabsPrimitive.Tab
			data-slot="tabs-trigger"
			className={cn(
				tabsTriggerVariants({ variant }),
				variant === "underline" || variant === "sheet" || variant === "pressed"
					? "z-20 data-active:border-transparent data-active:text-ink-1"
					: "data-active:border-accent-ink data-active:text-ink-1",
				variant === "default" && "data-active:font-semibold",
				className,
			)}
			{...props}
		/>
	);
}

function TabsContent({ className, ...props }: TabsPrimitive.Panel.Props) {
	return (
		<TabsPrimitive.Panel
			data-slot="tabs-content"
			className={cn("flex-1 outline-none", className)}
			{...props}
		/>
	);
}

export {
	Tabs,
	TabsContent,
	TabsList,
	TabsTrigger,
	tabsListVariants,
	tabsTriggerVariants,
};
