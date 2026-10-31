import { Slider as SliderPrimitive } from "@base-ui/react/slider";
import { cn } from "cn";

/**
 * A groove (`track-well`, 3:1 border) with a lit accent range and a knob that
 * stands proud of it. One thumb per value: pass an array for a range.
 * `aria-label` names the slider and every thumb ("Memory", "Memory 2").
 */
function Slider<Value extends number | readonly number[]>({
	className,
	value,
	defaultValue,
	...props
}: SliderPrimitive.Root.Props<Value>) {
	const initial = value ?? defaultValue;
	const thumbCount =
		typeof initial === "number" || initial === undefined
			? 1
			: (initial as readonly number[]).length;
	const label = props["aria-label"];
	return (
		<SliderPrimitive.Root
			data-slot="slider"
			value={value as Value}
			defaultValue={defaultValue as Value}
			className={cn(
				"group/slider relative w-full data-disabled:cursor-not-allowed data-disabled:opacity-50",
				className,
			)}
			{...props}
		>
			<SliderPrimitive.Control
				data-slot="slider-control"
				className="flex h-[18px] w-full touch-none select-none items-center data-disabled:cursor-not-allowed"
			>
				<SliderPrimitive.Track
					data-slot="slider-track"
					className="track-well relative h-2 w-full rounded-full"
				>
					<SliderPrimitive.Indicator
						data-slot="slider-range"
						className="track-lit h-full rounded-full"
					/>
					{Array.from({ length: thumbCount }, (_, index) => (
						<SliderPrimitive.Thumb
							key={index}
							index={index}
							data-slot="slider-thumb"
							getAriaLabel={
								label
									? () => (thumbCount > 1 ? `${label} ${index + 1}` : label)
									: null
							}
							className="knob block size-[18px] shrink-0 cursor-grab outline-none transition-[box-shadow,filter] duration-quick ease-out has-[:focus-visible]:focus-ring data-dragging:cursor-grabbing data-dragging:brightness-90 data-disabled:cursor-not-allowed"
						/>
					))}
				</SliderPrimitive.Track>
			</SliderPrimitive.Control>
		</SliderPrimitive.Root>
	);
}

export { Slider };
