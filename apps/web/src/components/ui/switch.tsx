import { Switch as SwitchPrimitive } from "@base-ui/react/switch";
import { cn } from "cn";

/**
 * A groove (`track-well`, 3:1 border) with a knob that stands proud of it.
 * Checked, the groove lights up with the accent and the knob slides 16px:
 * position, not colour alone, says which state it is in.
 */
function Switch({
	className,
	size = "default",
	...props
}: SwitchPrimitive.Root.Props & {
	size?: "sm" | "default";
}) {
	return (
		<SwitchPrimitive.Root
			data-slot="switch"
			data-size={size}
			className={cn(
				"track-well peer group/switch relative inline-flex shrink-0 cursor-pointer items-center rounded-full p-0.5 outline-none transition-[background-color,border-color,box-shadow] duration-180 ease-smooth-out after:absolute after:-inset-x-3 after:-inset-y-2 focus-visible:focus-ring aria-invalid:border-bad data-[size=default]:h-[22px] data-[size=sm]:h-[18px] data-[size=default]:w-[38px] data-[size=sm]:w-[30px] data-checked:border-accent-fill-edge data-checked:track-lit data-disabled:cursor-not-allowed data-disabled:opacity-50",
				className,
			)}
			{...props}
		>
			<SwitchPrimitive.Thumb
				data-slot="switch-thumb"
				className="knob pointer-events-none block transition-[translate] duration-180 ease-smooth-out group-data-[size=default]/switch:size-4 group-data-[size=sm]/switch:size-3 data-checked:translate-x-4 group-data-[size=sm]/switch:data-checked:translate-x-3 data-unchecked:translate-x-0"
			/>
		</SwitchPrimitive.Root>
	);
}

export { Switch };
