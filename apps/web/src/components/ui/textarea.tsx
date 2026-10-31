import { cn } from "cn";
import type * as React from "react";

function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
	return (
		<textarea
			data-slot="textarea"
			className={cn(
				"field field-sizing-content flex min-h-16 w-full resize-none rounded-ctl px-2.5 py-2 font-normal text-[13px] leading-[1.55] outline-none disabled:cursor-not-allowed",
				className,
			)}
			{...props}
		/>
	);
}

export { Textarea };
