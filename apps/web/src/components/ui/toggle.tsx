import { Toggle as TogglePrimitive } from "@base-ui/react/toggle";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";

const toggleVariants = cva(
	"group/toggle inline-flex cursor-pointer items-center justify-center has-[svg]:gap-1.5 whitespace-nowrap rounded-ctl border border-transparent text-ink-3 text-sm outline-none hover:text-ink-1 focus-visible:focus-ring disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-bad aria-pressed:font-medium aria-pressed:text-ink-1 data-[state=on]:text-ink-1 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
	{
		variants: {
			variant: {
				// A key that stays pressed in: the well it sinks into is the state.
				default:
					"pressable bg-transparent aria-pressed:border-line-faint aria-pressed:bg-surface-well aria-pressed:shadow-pressed",
				outline:
					"pressable border-line-control bg-transparent text-ink-1 hover:border-ink-4 aria-pressed:border-accent-ink aria-pressed:bg-accent-wash",
				// One segment of a segmented control: mono; the selected surface is
				// the group's `thumb-raised` (a gliding indicator, or this item
				// itself when the group is `instant`).
				segmented:
					"rounded-[7px] bg-transparent font-medium font-mono text-xs transition-[color,background-color,box-shadow] duration-quick ease-out aria-pressed:bg-transparent aria-pressed:font-medium",
			},
			size: {
				default: "h-[30px] min-w-[30px] px-2.5",
				sm: "h-7 min-w-7 px-2 text-xs [&_svg:not([class*='size-'])]:size-3.5",
				lg: "h-8 min-w-8 px-3",
				segmented: "h-auto px-[9px] py-[5px] leading-none",
				// The 28px segment of the form controls (12px side padding).
				"segmented-lg": "h-7 px-[11px] leading-none",
			},
		},
		defaultVariants: {
			variant: "default",
			size: "default",
		},
	},
);

function Toggle({
	className,
	variant = "default",
	size = "default",
	...props
}: TogglePrimitive.Props & VariantProps<typeof toggleVariants>) {
	return (
		<TogglePrimitive
			data-slot="toggle"
			className={cn(toggleVariants({ variant, size, className }))}
			{...props}
		/>
	);
}

export { Toggle, toggleVariants };
