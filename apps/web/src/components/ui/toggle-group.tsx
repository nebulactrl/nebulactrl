import { Toggle as TogglePrimitive } from "@base-ui/react/toggle";
import { ToggleGroup as ToggleGroupPrimitive } from "@base-ui/react/toggle-group";
import type { VariantProps } from "class-variance-authority";
import { cn } from "cn";
import * as React from "react";

import { toggleVariants } from "#/components/ui/toggle";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import { prefersReducedMotion } from "#/lib/transitions";

type SegmentedIndicator = "glide" | "instant";

const ToggleGroupContext = React.createContext<
	VariantProps<typeof toggleVariants> & {
		spacing?: number;
		orientation?: "horizontal" | "vertical";
	}
>({
	size: "default",
	variant: "default",
	spacing: 2,
	orientation: "horizontal",
});

function ToggleGroup({
	className,
	variant,
	size,
	spacing = 2,
	orientation = "horizontal",
	indicator = "glide",
	children,
	style,
	ref: forwardedRef,
	...props
}: ToggleGroupPrimitive.Props &
	VariantProps<typeof toggleVariants> & {
		spacing?: number;
		orientation?: "horizontal" | "vertical";
		/** Settings segments use a direct, non-gliding active fill. */
		indicator?: SegmentedIndicator;
	}) {
	const groupRef = React.useRef<HTMLDivElement>(null);
	const indicatorRef = React.useRef<HTMLSpanElement>(null);
	const setGroupRef = React.useCallback(
		(node: HTMLDivElement | null) => {
			groupRef.current = node;
			if (typeof forwardedRef === "function") forwardedRef(node);
			else if (forwardedRef)
				(
					forwardedRef as React.MutableRefObject<HTMLDivElement | null>
				).current = node;
		},
		[forwardedRef],
	);

	React.useLayoutEffect(() => {
		const group = groupRef.current;
		const indicatorNode = indicatorRef.current;
		if (
			!group ||
			!indicatorNode ||
			variant !== "segmented" ||
			indicator !== "glide"
		)
			return;

		// A `[data-glide]` indicator's first placement is never animated: only
		// later moves between segments glide. `placed` tracks whether this
		// indicator has synced once already.
		let placed = false;
		let frame = 0;
		const sync = () => {
			cancelAnimationFrame(frame);
			frame = requestAnimationFrame(() => {
				const active = group.querySelector<HTMLElement>(
					':scope > [aria-pressed="true"]',
				);
				if (!active) {
					indicatorNode.style.opacity = "0";
					placed = false;
					return;
				}
				// A key press moves the pressed segment without gliding: the
				// reader is navigating by keyboard and the motion only makes
				// them wait.
				// `.t-tabs-pill` owns the glide (transitions.css); `none` lands the
				// thumb without it, an empty value hands the clock back to the class.
				indicatorNode.style.transition =
					placed && !prefersReducedMotion() && !lastInputWasKeyboard()
						? ""
						: "none";
				indicatorNode.style.width = `${active.offsetWidth}px`;
				indicatorNode.style.height = `${active.offsetHeight}px`;
				indicatorNode.style.transform = `translate(${active.offsetLeft}px, ${active.offsetTop}px)`;
				indicatorNode.style.opacity = "1";
				placed = true;
			});
		};

		const resize = new ResizeObserver(sync);
		const mutation = new MutationObserver(sync);
		resize.observe(group);
		for (const item of group.querySelectorAll<HTMLElement>('[role="button"]'))
			resize.observe(item);
		mutation.observe(group, {
			attributes: true,
			childList: true,
			subtree: true,
			attributeFilter: ["aria-pressed", "data-state"],
		});
		sync();
		return () => {
			cancelAnimationFrame(frame);
			resize.disconnect();
			mutation.disconnect();
		};
	}, [indicator, variant]);

	return (
		<ToggleGroupPrimitive
			ref={setGroupRef}
			data-slot="toggle-group"
			data-variant={variant}
			data-size={size}
			data-spacing={spacing}
			data-orientation={orientation}
			data-indicator={variant === "segmented" ? indicator : undefined}
			data-glide={
				variant === "segmented" && indicator === "glide" ? "p" : undefined
			}
			style={
				typeof style === "function"
					? (state) =>
							({ "--gap": spacing, ...style(state) }) as React.CSSProperties
					: ({ "--gap": spacing, ...style } as React.CSSProperties)
			}
			className={cn(
				"group/toggle-group relative isolate flex w-fit flex-row items-center gap-[--spacing(var(--gap))] rounded-ctl data-vertical:flex-col data-vertical:items-stretch data-[variant=segmented]:track-well data-[variant=segmented]:rounded-[10px] data-[variant=segmented]:p-0.5",
				className,
			)}
			{...props}
		>
			{variant === "segmented" && indicator === "glide" ? (
				<span
					ref={indicatorRef}
					data-ind="1"
					aria-hidden="true"
					className="t-tabs-pill pointer-events-none thumb-raised absolute left-0 top-0 z-0 h-0 w-0 rounded-[7px] opacity-0"
				/>
			) : null}
			<ToggleGroupContext.Provider
				value={{ variant, size, spacing, orientation }}
			>
				{children}
			</ToggleGroupContext.Provider>
		</ToggleGroupPrimitive>
	);
}

function ToggleGroupItem({
	className,
	children,
	variant = "default",
	size = "default",
	...props
}: TogglePrimitive.Props & VariantProps<typeof toggleVariants>) {
	const context = React.useContext(ToggleGroupContext);

	return (
		<TogglePrimitive
			data-slot="toggle-group-item"
			data-variant={context.variant || variant}
			data-size={context.size || size}
			data-spacing={context.spacing}
			className={cn(
				"shrink-0 group-data-[spacing=0]/toggle-group:rounded-none group-data-[spacing=0]/toggle-group:px-2 focus:z-20 focus-visible:z-20 group-data-[spacing=0]/toggle-group:has-data-[icon=inline-end]:pr-1.5 group-data-[spacing=0]/toggle-group:has-data-[icon=inline-start]:pl-1.5 group-data-horizontal/toggle-group:data-[spacing=0]:first:rounded-l-ctl group-data-vertical/toggle-group:data-[spacing=0]:first:rounded-t-ctl group-data-horizontal/toggle-group:data-[spacing=0]:last:rounded-r-ctl group-data-vertical/toggle-group:data-[spacing=0]:last:rounded-b-ctl group-data-horizontal/toggle-group:data-[spacing=0]:data-[variant=outline]:border-l-0 group-data-vertical/toggle-group:data-[spacing=0]:data-[variant=outline]:border-t-0 group-data-[variant=segmented]/toggle-group:relative group-data-[indicator=instant]/toggle-group:aria-pressed:thumb-raised group-data-[variant=segmented]/toggle-group:z-10",
				toggleVariants({
					variant: context.variant || variant,
					size: context.size || size,
				}),
				className,
			)}
			{...props}
		>
			{children}
		</TogglePrimitive>
	);
}

export { ToggleGroup, ToggleGroupItem };
