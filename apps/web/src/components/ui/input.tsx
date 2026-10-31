import { Input as InputPrimitive } from "@base-ui/react/input";
import { cn } from "cn";
import type * as React from "react";

/**
 * An engraved well (`field`) with the 3:1 control border. `default` is the
 * 32px toolbar field; `lg` the 36px form field that every dialog uses; `xl`
 * the 40px field on the auth and onboarding cards.
 */
function Input({
	className,
	type,
	scale = "default",
	...props
}: React.ComponentProps<"input"> & { scale?: "default" | "lg" | "xl" }) {
	return (
		<InputPrimitive
			type={type}
			data-slot="input"
			className={cn(
				"field h-8 w-full min-w-0 rounded-ctl px-2.5 font-normal text-[13px] outline-none file:inline-flex file:h-6 file:border-0 file:bg-transparent file:font-medium file:text-ink-1 file:text-xs read-only:text-ink-3 disabled:pointer-events-none disabled:cursor-not-allowed",
				scale === "lg" && "h-9",
				scale === "xl" && "h-10 px-3 text-[14px]",
				className,
			)}
			{...props}
		/>
	);
}

export { Input };
