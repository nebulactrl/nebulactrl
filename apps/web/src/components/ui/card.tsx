import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";
import type * as React from "react";

// The panel is a material, not a border: `raised` (default) is a card on the
// floor, `float` a toolbar/inspector-level panel, `pop` a menu-level one,
// `well` an engraved recess for guidance and code,
// `danger` a flat panel with a failed-red edge for irreversible actions.
// `size="lg"` is the settings grammar: 24px gutters and a section title
// instead of a panel title.
const cardVariants = cva(
	"group/card flex flex-col overflow-hidden rounded-card text-ink-1 text-sm",
	{
		variants: {
			variant: {
				default: "surface-raised",
				raised: "surface-raised",
				float: "surface-float",
				pop: "surface-pop",
				well: "surface-well",
				danger: "border border-destructive-border bg-surface-base",
			},
		},
		defaultVariants: {
			variant: "default",
		},
	},
);

function Card({
	className,
	variant = "default",
	size = "default",
	...props
}: React.ComponentProps<"div"> &
	VariantProps<typeof cardVariants> & { size?: "default" | "lg" }) {
	return (
		<div
			data-slot="card"
			data-size={size}
			data-variant={variant}
			className={cn(cardVariants({ variant }), className)}
			{...props}
		/>
	);
}

function CardHeader({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="card-header"
			className={cn(
				"flex shrink-0 items-baseline justify-between gap-3 border-line border-b px-5 py-3.5 group-data-[variant=danger]/card:border-line-strong group-data-[size=lg]/card:px-6",
				className,
			)}
			{...props}
		/>
	);
}

function CardTitle({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="card-title"
			className={cn(
				"font-semibold text-ink-1 text-sm leading-[1.4] group-data-[size=lg]/card:text-base group-data-[size=lg]/card:leading-[1.3]",
				className,
			)}
			{...props}
		/>
	);
}

function CardDescription({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="card-description"
			className={cn("text-ink-3 text-xs leading-normal", className)}
			{...props}
		/>
	);
}

function CardContent({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="card-content"
			className={cn(
				"px-5 py-4 group-data-[size=lg]/card:px-6 group-data-[size=lg]/card:py-5",
				className,
			)}
			{...props}
		/>
	);
}

function CardFooter({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="card-footer"
			className={cn(
				"flex shrink-0 items-center justify-end gap-2 border-line border-t bg-black/10 px-5 py-3.5 group-data-[size=lg]/card:px-6",
				className,
			)}
			{...props}
		/>
	);
}

export {
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
};
