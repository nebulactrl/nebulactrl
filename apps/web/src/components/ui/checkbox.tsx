import { Checkbox as CheckboxPrimitive } from "@base-ui/react/checkbox";
import { cn } from "cn";
import { Check } from "lucide-react";

/**
 * A 16px engraved well. Unchecked, its 3:1 control border is the whole
 * affordance; checked, it fills with the accent key and shows a white check
 * (the glyph, not the colour, is what says "on").
 */
function Checkbox({ className, ...props }: CheckboxPrimitive.Root.Props) {
	return (
		<CheckboxPrimitive.Root
			data-slot="checkbox"
			className={cn(
				"peer relative flex size-4 shrink-0 cursor-pointer items-center justify-center rounded-[5px] border border-line-control bg-surface-well text-white shadow-well outline-none transition-[background-color,border-color,box-shadow] duration-quick ease-out after:absolute after:-inset-x-3 after:-inset-y-2 focus-visible:focus-ring disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-bad group-has-disabled/field:opacity-50 data-checked:border-accent-fill-edge data-checked:bg-linear-to-b data-checked:from-accent-fill-top data-checked:to-accent-fill-bot data-checked:shadow-[inset_0_1px_0_rgb(255_255_255/0.28),0_1px_2px_rgb(2_4_10/0.5)] data-indeterminate:border-accent-fill-edge data-indeterminate:bg-linear-to-b data-indeterminate:from-accent-fill-top data-indeterminate:to-accent-fill-bot data-indeterminate:shadow-[inset_0_1px_0_rgb(255_255_255/0.28),0_1px_2px_rgb(2_4_10/0.5)]",
				className,
			)}
			{...props}
		>
			<CheckboxPrimitive.Indicator
				data-slot="checkbox-indicator"
				className="grid place-content-center text-current transition-none [&>svg]:size-3 [&>svg]:stroke-[3]"
			>
				<Check />
			</CheckboxPrimitive.Indicator>
		</CheckboxPrimitive.Root>
	);
}

export { Checkbox };
