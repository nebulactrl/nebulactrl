import { Button as ButtonPrimitive } from "@base-ui/react/button";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";

// Every button is a key: it sinks (scale .97) on pointer press, and its
// hover/pressed looks come from the `btn-*` utilities, which only apply to an
// enabled button. A disabled one fades and ignores the pointer entirely.
const buttonVariants = cva(
	"group/button inline-flex shrink-0 cursor-pointer select-none items-center justify-center has-[svg]:gap-1.5 whitespace-nowrap rounded-ctl font-medium text-[13px] leading-none focus-visible:focus-ring disabled:cursor-not-allowed disabled:opacity-45 aria-disabled:pointer-events-none aria-disabled:opacity-45 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
	{
		variants: {
			variant: {
				// The one call to action on a surface: the accent key.
				primary: "btn-primary pressable font-semibold",
				// Aliases of `primary`.
				default: "btn-primary pressable font-semibold",
				accent: "btn-primary pressable font-semibold",
				// A raised neutral key.
				secondary: "btn-secondary pressable",
				// Alias of `secondary`.
				outline: "btn-secondary pressable",
				ghost: "btn-ghost pressable",
				// Danger key: reads as dangerous before you read it.
				danger: "btn-danger pressable",
				// Alias of `danger`.
				destructive: "btn-danger pressable",
				// The confirm button of a destructive dialog.
				"danger-solid": "btn-danger-solid pressable font-semibold",
				// Alias of `danger-solid`.
				"destructive-solid": "btn-danger-solid pressable font-semibold",
				// The inline text action: accent ink that brightens on hover.
				link: "h-auto border-0 p-0 text-accent-ink transition-[color] duration-quick ease-out hover:text-ink-1",
			},
			size: {
				default: "h-8 px-3",
				// 24px filled chip button ("Update" in a changelog row).
				"2xs":
					"h-6 has-[svg]:gap-1 rounded-md px-2.5 text-2xs leading-none [&_svg:not([class*='size-'])]:size-3",
				xs: "h-[26px] has-[svg]:gap-1 rounded-md px-[9px] text-2xs leading-none [&_svg:not([class*='size-'])]:size-3",
				sm: "h-7 has-[svg]:gap-1 px-2.5 text-xs leading-none [&_svg:not([class*='size-'])]:size-3.5",
				// 30px, 12px label: the compact toolbar button.
				md: "h-[30px] px-3 text-xs leading-none [&_svg:not([class*='size-'])]:size-3.5",
				lg: "h-[34px] px-3",
				// The 40px auth and onboarding button.
				"xl-auth": "h-10 px-4 text-[14px] leading-none",
				xl: "h-[42px] px-4 text-[14px] leading-none",
				icon: "size-8",
				"icon-xs":
					"size-[26px] rounded-md [&_svg:not([class*='size-'])]:size-3",
				"icon-sm": "size-7 rounded-md [&_svg:not([class*='size-'])]:size-3.5",
				"icon-lg": "size-[34px]",
			},
		},
		compoundVariants: [
			// Filled actions carry 2px more padding than their neighbours so
			// they read as primary even at the same height.
			{ variant: "primary", size: "default", className: "px-3.5" },
			{ variant: "default", size: "default", className: "px-3.5" },
			{ variant: "accent", size: "default", className: "px-3.5" },
			{ variant: "primary", size: "lg", className: "px-3.5" },
			{ variant: "default", size: "lg", className: "px-3.5" },
			{ variant: "accent", size: "lg", className: "px-3.5" },
			{ variant: "danger-solid", size: "lg", className: "px-3.5" },
			{ variant: "destructive-solid", size: "lg", className: "px-3.5" },
			{ variant: "link", size: "default", className: "h-auto px-0 text-xs" },
		],
		defaultVariants: {
			variant: "primary",
			size: "default",
		},
	},
);

function Button({
	className,
	variant = "primary",
	size = "default",
	...props
}: ButtonPrimitive.Props & VariantProps<typeof buttonVariants>) {
	return (
		<ButtonPrimitive
			data-slot="button"
			className={cn(buttonVariants({ variant, size, className }))}
			{...props}
		/>
	);
}

export { Button, buttonVariants };
