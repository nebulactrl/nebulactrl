import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";
import { Children, type ComponentProps, type ReactNode } from "react";

// Every key is a `keycap`: a lit top edge, a shaded bottom and a lip under it.
// The variants only change its size; the lip needs 2px below, so `key` (inside a
// button label, where the lip would push the baseline) drops it.
const kbdVariants = cva(
	"keycap pointer-events-none w-fit select-none gap-1 [&_svg:not([class*='size-'])]:size-3",
	{
		variants: {
			variant: {
				// A shortcut hint beside a label: "esc", "⌘K".
				hint: "",
				// A key cap on the shortcuts list.
				cap: "h-6! min-w-6! px-[7px]! text-xs!",
				// The smaller cap on a palette row.
				"cap-sm": "h-5! min-w-5! px-1.5!",
				// A key hint inside a button label: small, flush ("⌘↵").
				key: "mb-0! h-[18px]! min-w-[18px]! px-[5px]! font-normal! text-2xs!",
			},
		},
		defaultVariants: { variant: "hint" },
	},
);

// ⌘ ⇧ ⌥ ⌃ and the arrows are missing from IBM Plex Mono. Each one is wrapped in
// `keycap-mod`, which draws it with the system symbol font at 1.1em, so it fills
// the cap beside letters whatever `font-*` or `text-*` class the key carries.
const MODIFIER_GLYPHS = /([⌘⇧⌥⌃⌫⌦⎋⇥↵↑↓←→])/;

function withModifierGlyphs(children: ReactNode): ReactNode {
	return Children.map(children, (child) => {
		if (typeof child !== "string") return child;
		return child.split(MODIFIER_GLYPHS).map((part, index) =>
			MODIFIER_GLYPHS.test(part) ? (
				<span key={index} className="keycap-mod">
					{part}
				</span>
			) : (
				part
			),
		);
	});
}

function Kbd({
	className,
	variant,
	children,
	...props
}: ComponentProps<"kbd"> & VariantProps<typeof kbdVariants>) {
	return (
		<kbd
			data-slot="kbd"
			className={cn(kbdVariants({ variant }), className)}
			{...props}
		>
			{withModifierGlyphs(children)}
		</kbd>
	);
}

export { Kbd };
