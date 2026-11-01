import { Avatar as AvatarPrimitive } from "@base-ui/react/avatar";
import { Button as ButtonPrimitive } from "@base-ui/react/button";
import { cn } from "cn";
import type * as React from "react";

/**
 * The small primitives the kit's own interface pieces (`DitherButton`,
 * `DitherCard`, `DitherAvatar`, `DitherEmptyState`, `DitherNotFound`) sit on.
 * They are Base UI plus the design vocabulary both apps define in their CSS
 * (`btn-*`, `pressable`, `surface-*`, `rounded-card`, `text-ink-*`), so the kit
 * is self-contained and draws the same material in `apps/web` and `apps/site`.
 *
 * They mirror the app's `components/ui/{button,card,avatar,empty}` class for
 * class. The vocabulary classes own the look; change a surface there, not
 * here.
 */

// ───────── Button ─────────

export type ButtonVariant =
	| "primary"
	| "default"
	| "accent"
	| "secondary"
	| "outline"
	| "ghost"
	| "danger"
	| "destructive"
	| "danger-solid"
	| "destructive-solid"
	| "link";

type ButtonSize =
	| "default"
	| "2xs"
	| "xs"
	| "sm"
	| "md"
	| "lg"
	| "xl-auth"
	| "xl"
	| "icon"
	| "icon-xs"
	| "icon-sm"
	| "icon-lg";

const BUTTON_BASE =
	"group/button inline-flex shrink-0 cursor-pointer select-none items-center justify-center has-[svg]:gap-1.5 whitespace-nowrap rounded-ctl font-medium text-[13px] leading-none focus-visible:focus-ring disabled:cursor-not-allowed disabled:opacity-45 aria-disabled:pointer-events-none aria-disabled:opacity-45 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4";

const BUTTON_VARIANT: Record<ButtonVariant, string> = {
	primary: "btn-primary pressable font-semibold",
	default: "btn-primary pressable font-semibold",
	accent: "btn-primary pressable font-semibold",
	secondary: "btn-secondary pressable",
	outline: "btn-secondary pressable",
	ghost: "btn-ghost pressable",
	danger: "btn-danger pressable",
	destructive: "btn-danger pressable",
	"danger-solid": "btn-danger-solid pressable font-semibold",
	"destructive-solid": "btn-danger-solid pressable font-semibold",
	link: "h-auto border-0 p-0 text-accent-ink transition-[color] duration-150 ease-out hover:text-ink-1",
};

const BUTTON_SIZE: Record<ButtonSize, string> = {
	default: "h-8 px-3",
	"2xs":
		"h-6 has-[svg]:gap-1 rounded-md px-2.5 text-2xs leading-none [&_svg:not([class*='size-'])]:size-3",
	xs: "h-[26px] has-[svg]:gap-1 rounded-md px-[9px] text-2xs leading-none [&_svg:not([class*='size-'])]:size-3",
	sm: "h-7 has-[svg]:gap-1 px-2.5 text-xs leading-none [&_svg:not([class*='size-'])]:size-3.5",
	md: "h-[30px] px-3 text-xs leading-none [&_svg:not([class*='size-'])]:size-3.5",
	lg: "h-[34px] px-3",
	"xl-auth": "h-10 px-4 text-[14px] leading-none",
	xl: "h-[42px] px-4 text-[14px] leading-none",
	icon: "size-8",
	"icon-xs": "size-[26px] rounded-md [&_svg:not([class*='size-'])]:size-3",
	"icon-sm": "size-7 rounded-md [&_svg:not([class*='size-'])]:size-3.5",
	"icon-lg": "size-[34px]",
};

/** Class names of a button. Accent keys carry 2px more padding at the default and `lg` sizes, the danger key at `lg`, so they read as primary. */
function buttonClassName(
	variant: ButtonVariant,
	size: ButtonSize,
	className?: string,
): string {
	const accent =
		variant === "primary" || variant === "default" || variant === "accent";
	const danger = variant === "danger-solid" || variant === "destructive-solid";
	const padded =
		(accent && (size === "default" || size === "lg")) ||
		(danger && size === "lg");
	return cn(
		BUTTON_BASE,
		BUTTON_VARIANT[variant],
		BUTTON_SIZE[size],
		padded && "px-3.5",
		variant === "link" && size === "default" && "h-auto px-0 text-xs",
		className,
	);
}

export type ButtonProps = ButtonPrimitive.Props & {
	variant?: ButtonVariant | undefined;
	size?: ButtonSize | undefined;
};

/** A key: sinks on press through `pressable`; a disabled one fades and never moves. */
export function Button({
	className,
	variant = "primary",
	size = "default",
	...props
}: ButtonProps) {
	return (
		<ButtonPrimitive
			data-slot="button"
			className={buttonClassName(variant, size, className as string)}
			{...props}
		/>
	);
}

// ───────── Card ─────────

export type CardVariant =
	| "default"
	| "raised"
	| "float"
	| "pop"
	| "well"
	| "sunken"
	| "danger";

const CARD_VARIANT: Record<CardVariant, string> = {
	default: "surface-raised",
	raised: "surface-raised",
	float: "surface-float",
	pop: "surface-pop",
	well: "surface-well",
	sunken: "surface-well",
	danger: "border border-destructive-border bg-surface-base",
};

/** A material, not a border: raised, float, pop, well or a danger panel. */
export function Card({
	className,
	variant = "default",
	size = "default",
	...props
}: React.ComponentProps<"div"> & {
	variant?: CardVariant | undefined;
	size?: "default" | "lg" | undefined;
}) {
	return (
		<div
			data-slot="card"
			data-size={size}
			data-variant={variant}
			className={cn(
				"group/card flex flex-col overflow-hidden rounded-card text-ink-1 text-sm",
				CARD_VARIANT[variant],
				className,
			)}
			{...props}
		/>
	);
}

// ───────── Avatar ─────────

export function Avatar({
	className,
	size = "default",
	...props
}: AvatarPrimitive.Root.Props & {
	size?: "default" | "sm" | "lg";
}) {
	return (
		<AvatarPrimitive.Root
			data-slot="avatar"
			data-size={size}
			className={cn(
				"group/avatar relative flex size-8 shrink-0 select-none rounded-full data-[size=lg]:size-10 data-[size=sm]:size-[22px]",
				className as string,
			)}
			{...props}
		/>
	);
}

export function AvatarImage({
	className,
	...props
}: AvatarPrimitive.Image.Props) {
	return (
		<AvatarPrimitive.Image
			data-slot="avatar-image"
			className={cn(
				"aspect-square size-full rounded-full object-cover",
				className as string,
			)}
			{...props}
		/>
	);
}

export function AvatarFallback({
	className,
	...props
}: AvatarPrimitive.Fallback.Props) {
	return (
		<AvatarPrimitive.Fallback
			data-slot="avatar-fallback"
			className={cn(
				"flex size-full items-center justify-center rounded-full border border-line-strong bg-surface-float font-mono font-semibold text-2xs text-ink-2 shadow-tile group-data-[size=sm]/avatar:text-[9px]",
				className as string,
			)}
			{...props}
		/>
	);
}

export function AvatarGroup({
	className,
	...props
}: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="avatar-group"
			className={cn(
				"group/avatar-group flex *:data-[slot=avatar]:-ml-1.5 *:data-[slot=avatar]:first:ml-0 *:data-[slot=avatar]:ring-2 *:data-[slot=avatar]:ring-surface-raised",
				className,
			)}
			{...props}
		/>
	);
}

export function AvatarGroupCount({
	className,
	...props
}: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="avatar-group-count"
			className={cn(
				"relative flex size-8 shrink-0 items-center justify-center rounded-full bg-surface-float text-2xs text-ink-3 ring-2 ring-surface-raised group-has-data-[size=lg]/avatar-group:size-10 group-has-data-[size=sm]/avatar-group:size-6 [&>svg]:size-4 group-has-data-[size=lg]/avatar-group:[&>svg]:size-5 group-has-data-[size=sm]/avatar-group:[&>svg]:size-3",
				className,
			)}
			{...props}
		/>
	);
}

// ───────── Empty ─────────

export function Empty({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="empty"
			className={cn(
				"flex w-full min-w-0 flex-1 flex-col items-center justify-center gap-4 px-6 py-12 text-center",
				className,
			)}
			{...props}
		/>
	);
}

export function EmptyHeader({
	className,
	...props
}: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="empty-header"
			className={cn("flex max-w-[68ch] flex-col items-center gap-1", className)}
			{...props}
		/>
	);
}

/** The object above the title; `variant="icon"` is the engraved icon tile. */
export function EmptyMedia({
	className,
	variant = "default",
	...props
}: React.ComponentProps<"div"> & { variant?: "default" | "icon" }) {
	return (
		<div
			data-slot="empty-icon"
			data-variant={variant}
			className={cn(
				"mb-2 flex shrink-0 items-center justify-center [&_svg]:pointer-events-none [&_svg]:shrink-0",
				variant === "icon"
					? "surface-well flex size-10 shrink-0 items-center justify-center rounded-card text-ink-3 [&_svg:not([class*='size-'])]:size-4"
					: "bg-transparent",
				className,
			)}
			{...props}
		/>
	);
}

export function EmptyDescription({
	className,
	...props
}: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="empty-description"
			className={cn("type-body text-ink-3", className)}
			{...props}
		/>
	);
}

export function EmptyContent({
	className,
	...props
}: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="empty-content"
			className={cn(
				"flex w-full min-w-0 max-w-sm flex-col items-center gap-2",
				className,
			)}
			{...props}
		/>
	);
}
