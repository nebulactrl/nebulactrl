import { cn } from "cn";
import type * as React from "react";

// Table anatomy: 38px sentence-case header (ink-3, 12/500) over a `line`
// rule, 40px rows (48 when a secondary id folds under the primary) divided by
// `line-faint`, a flat white tint on hover with no animation, `accent-wash`
// when selected. No shadow, no gradient on rows or cells. The frame (border,
// radius) comes from the Card the table sits in.
//
// Below `md` a table with `stack` set switches from a scrolling grid to a
// column of cards: the header row hides, each `TableRow` becomes a card,
// and each `TableCell` becomes a "label: value" line using its own
// `label` prop for the left side — the text of the column's `TableHead`.
// Pass `pin="start"` on a row's primary identifier cell and `pin="end"` on
// its status cell to keep those two, unlabelled, on the card's
// always-visible top line (a sideways-scrolling table would hide the status
// column entirely). Every other cell still renders — nothing is hidden,
// only reflowed — so `stack` never loses data the way horizontal scroll
// does. Adopt per table: `<Table stack>`, then `label`/`pin` on that
// table's `TableCell`s; `TableHead` needs no change, it already hides
// itself in stacked mode.
function Table({
	className,
	stack,
	...props
}: React.ComponentProps<"table"> & {
	/** Switches to the card layout described above below the `md` breakpoint. */
	stack?: boolean;
}) {
	return (
		<div
			data-slot="table-container"
			className="relative w-full overflow-x-auto"
		>
			<table
				data-slot="table"
				data-stack={stack ? "true" : undefined}
				className={cn(
					"w-full caption-bottom border-collapse text-sm",
					className,
				)}
				{...props}
			/>
		</div>
	);
}

function TableHeader({ className, ...props }: React.ComponentProps<"thead">) {
	return (
		<thead
			data-slot="table-header"
			className={cn(
				"[&_tr]:border-line [&_tr]:border-b [&_tr]:hover:bg-transparent",
				className,
			)}
			{...props}
		/>
	);
}

function TableBody({ className, ...props }: React.ComponentProps<"tbody">) {
	return (
		<tbody
			data-slot="table-body"
			className={cn("[&_tr:last-child]:border-0", className)}
			{...props}
		/>
	);
}

function TableRow({ className, ...props }: React.ComponentProps<"tr">) {
	return (
		<tr
			data-slot="table-row"
			className={cn(
				"group/row border-line-faint border-b hover:bg-white/4 has-aria-expanded:bg-white/4 aria-selected:bg-accent-wash data-[state=selected]:bg-accent-wash",
				className,
			)}
			{...props}
		/>
	);
}

function TableHead({ className, ...props }: React.ComponentProps<"th">) {
	return (
		<th
			data-slot="table-head"
			className={cn(
				"h-[38px] whitespace-nowrap px-3.5 text-left align-middle font-medium text-ink-3 text-xs [&:has([role=checkbox])]:pr-0",
				className,
			)}
			{...props}
		/>
	);
}

function TableCell({
	className,
	label,
	pin,
	...props
}: React.ComponentProps<"td"> & {
	/** Column header text, shown before the value in stacked mode (see `Table`'s `stack` prop). */
	label?: string;
	/** Keeps this cell, unlabelled, on the stacked card's always-visible top line. */
	pin?: "start" | "end";
}) {
	return (
		<td
			data-slot="table-cell"
			data-label={label}
			data-pin={pin}
			className={cn(
				"h-10 px-3.5 py-2 align-middle [&:has([role=checkbox])]:pr-0",
				className,
			)}
			{...props}
		/>
	);
}

// Right-most 44px column: a 28px icon button that rests as a bare muted glyph
// and gains its border on row hover or focus, so the column never reads as a
// wall of buttons but the action is always discoverable.
function TableActionsCell({ className, ...props }: React.ComponentProps<"td">) {
	return (
		<td
			data-slot="table-actions-cell"
			className={cn(
				"w-11 px-2 py-2 text-center align-middle [&_button]:border-transparent [&_button]:bg-transparent [&_button]:text-ink-3 group-hover/row:[&_button]:border-line-control [&_button:focus-visible]:border-line-control [&_button[aria-expanded=true]]:border-line-control [&_button[aria-expanded=true]]:bg-white/8",
				className,
			)}
			{...props}
		/>
	);
}

export {
	Table,
	TableActionsCell,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
};
