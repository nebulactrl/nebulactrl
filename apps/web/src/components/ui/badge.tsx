import { mergeProps } from "@base-ui/react/merge-props";
import { useRender } from "@base-ui/react/use-render";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";
import { Info } from "lucide-react";
import type { ComponentPropsWithRef } from "react";
import { type LampTone, statusKindOfTone } from "#/components/ui/lamp";
import { StatusIcon } from "#/components/ui/status-icon";

// `tone-wash` reads `data-tone`: it sets the label colour (>= 5.5:1) and the
// wash behind it. The frame is the same tone, faint.
const toneClassName =
	"tone-wash border-[color-mix(in_oklab,var(--tone)_30%,transparent)]";

const badgeVariants = cva(
	"group/badge inline-flex w-fit shrink-0 items-center justify-center has-[svg]:gap-1.5 overflow-hidden whitespace-nowrap rounded-md border border-transparent px-1.5 py-[3px] font-medium font-mono text-3xs leading-none transition-[color,background-color,border-color] duration-quick ease-out focus-visible:focus-ring [&>svg]:pointer-events-none [&>svg]:size-3",
	{
		variants: {
			variant: {
				// A kind, a tag, a count on a surface: a flat chip.
				default: "border-line-faint bg-white/5 text-ink-3",
				// "protected", "staged · was 3", an approvals count.
				accent: "border-accent-ink/25 bg-accent-wash text-accent-ink",
				// The environment beside a sheet title: the only chip with a frame.
				outline: "border-line-strong bg-transparent text-2xs text-ink-3",
				// "N staged" on a node, a tab's change count: the accent key.
				solid:
					"border-accent-fill-edge bg-linear-to-b from-accent-fill-top to-accent-fill-bot font-semibold text-white shadow-[inset_0_1px_0_rgb(255_255_255/0.25)]",
				success: toneClassName,
				warning: toneClassName,
				destructive: toneClassName,
				// Driven by the `tone` prop.
				tone: toneClassName,
			},
			shape: {
				tag: "",
				// A count on the inbox button.
				pill: "min-w-[18px] rounded-full px-[5px]",
				// A tab's or nav item's count: square-cornered, centred.
				count: "min-w-[18px] px-[5px]",
				// A status capsule ("installed", "available").
				capsule: "rounded-full px-[7px]",
				// The count in a page's own tab strip: a round chip, tight padding.
				"tab-count":
					"min-w-0 rounded-full px-1 py-0.5 text-center font-semibold",
			},
		},
		compoundVariants: [
			{
				variant: "default",
				shape: "tab-count",
				className: "bg-white/10",
			},
		],
		defaultVariants: {
			variant: "default",
			shape: "tag",
		},
	},
);

const VARIANT_TONE: Partial<
	Record<NonNullable<VariantProps<typeof badgeVariants>["variant"]>, LampTone>
> = {
	success: "ok",
	warning: "warn",
	destructive: "bad",
};

type BadgeProps = useRender.ComponentProps<"span"> &
	VariantProps<typeof badgeVariants> & {
		/** Colours the chip by status or semantic tone; wins over `variant`. */
		tone?: LampTone;
		/** Renders the tone's status icon before the label. Needs a `tone` (or a tonal variant). */
		lamp?: boolean;
	};

function Badge({
	className,
	variant = "default",
	shape = "tag",
	tone,
	lamp = false,
	render,
	children,
	...props
}: BadgeProps) {
	const resolvedVariant = tone ? "tone" : variant;
	const resolvedTone = tone ?? VARIANT_TONE[variant ?? "default"];
	return useRender({
		defaultTagName: "span",
		props: mergeProps<"span">(
			{
				className: cn(
					badgeVariants({ variant: resolvedVariant, shape }),
					lamp && "gap-1.5",
					className,
				),
				"data-tone": resolvedTone,
				children: lamp ? (
					<>
						{resolvedTone === "info" ? (
							<Info aria-hidden className="size-3 shrink-0 text-(--tone)" />
						) : (
							<StatusIcon
								kind={statusKindOfTone(resolvedTone ?? "off")}
								label=""
								className="size-3"
							/>
						)}
						{children}
					</>
				) : (
					children
				),
			} as ComponentPropsWithRef<"span">,
			props,
		),
		render,
		state: {
			slot: "badge",
			variant,
		},
	});
}

export { Badge, badgeVariants };
