"use client";

import { Menu as MenuPrimitive } from "@base-ui/react/menu";
import { cn } from "cn";
import * as React from "react";
import { useInstantPopupRef } from "#/components/ui/instant-popup";

function DropdownMenu({ ...props }: MenuPrimitive.Root.Props) {
	return <MenuPrimitive.Root data-slot="dropdown-menu" {...props} />;
}

function DropdownMenuTrigger({ ...props }: MenuPrimitive.Trigger.Props) {
	return <MenuPrimitive.Trigger data-slot="dropdown-menu-trigger" {...props} />;
}

function DropdownMenuContent({
	align = "start",
	alignOffset = 0,
	side = "bottom",
	sideOffset = 4,
	className,
	ref: forwardedRef,
	...props
}: MenuPrimitive.Popup.Props &
	Pick<
		MenuPrimitive.Positioner.Props,
		"align" | "alignOffset" | "side" | "sideOffset"
	>) {
	const mergedRef = useInstantPopupRef<HTMLDivElement>(
		forwardedRef as React.Ref<HTMLDivElement> | undefined,
	);
	return (
		<MenuPrimitive.Portal>
			<MenuPrimitive.Positioner
				className="isolate z-50 outline-none"
				align={align}
				alignOffset={alignOffset}
				side={side}
				sideOffset={sideOffset}
			>
				<MenuPrimitive.Popup
					ref={mergedRef}
					data-slot="dropdown-menu-content"
					className={cn(
						"t-dropdown surface-pop z-50 max-h-(--available-height) min-w-[200px] overflow-y-auto overflow-x-hidden overscroll-contain rounded-menu p-1 text-ink-1 outline-none",
						className,
					)}
					{...props}
				/>
			</MenuPrimitive.Positioner>
		</MenuPrimitive.Portal>
	);
}

function DropdownMenuGroup({ ...props }: MenuPrimitive.Group.Props) {
	return <MenuPrimitive.Group data-slot="dropdown-menu-group" {...props} />;
}

function DropdownMenuLabel({
	className,
	inset,
	...props
}: MenuPrimitive.GroupLabel.Props & {
	inset?: boolean;
}) {
	return (
		<MenuPrimitive.GroupLabel
			data-slot="dropdown-menu-label"
			data-inset={inset}
			className={cn("type-label px-2.5 pt-2.5 pb-2 data-inset:pl-8", className)}
			{...props}
		/>
	);
}

function DropdownMenuItem({
	className,
	inset,
	variant = "default",
	...props
}: MenuPrimitive.Item.Props & {
	inset?: boolean;
	variant?: "default" | "destructive";
}) {
	return (
		<MenuPrimitive.Item
			data-slot="dropdown-menu-item"
			data-inset={inset}
			data-variant={variant}
			// `text-left`: an item rendered as a `<button>` would otherwise
			// inherit the button's centred text in any stretched child line.
			className={cn(
				"group/dropdown-menu-item relative flex min-h-[30px] w-full cursor-pointer select-none items-center gap-2 rounded-md px-2.5 text-left text-[13px] text-ink-2 leading-none outline-hidden data-highlighted:bg-white/7 data-highlighted:text-ink-1 data-inset:pl-8 data-[variant=destructive]:text-destructive-text data-[variant=destructive]:data-highlighted:bg-destructive-hover data-[variant=destructive]:data-highlighted:text-destructive-text data-disabled:pointer-events-none data-disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-3.5 [&_svg]:text-ink-3 data-highlighted:[&_svg]:text-ink-1 data-[variant=destructive]:*:[svg]:text-destructive-text",
				className,
			)}
			// Base UI warns when an item rendered as a `<button>` is not told so.
			nativeButton={
				React.isValidElement(props.render) && props.render.type === "button"
			}
			{...props}
		/>
	);
}

function DropdownMenuSeparator({
	className,
	...props
}: MenuPrimitive.Separator.Props) {
	return (
		<MenuPrimitive.Separator
			data-slot="dropdown-menu-separator"
			className={cn("-mx-1 my-1 h-px bg-line", className)}
			{...props}
		/>
	);
}

export {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
};
