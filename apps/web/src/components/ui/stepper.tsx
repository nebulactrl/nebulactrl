import { cn } from "cn";
import { Badge } from "#/components/ui/badge";

export interface StepperProps {
	value: number;
	onValueChange: (value: number) => void;
	min?: number;
	max?: number;
	step?: number;
	/**
	 * The applied value when `value` is a staged edit. When it differs from
	 * `value`, a "staged · was N" hint shows beside the control so the
	 * reader sees both the pending and the live number.
	 */
	was?: number;
	/** Names the number for assistive tech: "Replicas". */
	"aria-label": string;
	/** Ids of the text that says why the control is disabled. */
	"aria-describedby"?: string;
	disabled?: boolean;
	className?: string;
	/**
	 * `default` is the 34×32 replicas control, `sm` the 26px one in a
	 * process row, `lg` the borderless-cell 32px one on a database panel.
	 */
	size?: "sm" | "default" | "lg";
}

/** − N + for a small integer such as replicas. */
export function Stepper({
	value,
	onValueChange,
	min = 0,
	max = Number.POSITIVE_INFINITY,
	step = 1,
	was,
	disabled = false,
	className,
	size = "default",
	...props
}: StepperProps) {
	const label = props["aria-label"];
	const set = (next: number) =>
		onValueChange(Math.min(max, Math.max(min, next)));
	const staged = was !== undefined && was !== value;
	const buttonClassName = cn(
		"flex cursor-pointer items-center justify-center px-1.5 py-px text-ink-1 transition-[background-color,color] duration-quick ease-out hover:bg-white/7 focus-visible:focus-ring focus-visible:z-10 active:bg-black/30 disabled:cursor-not-allowed disabled:hover:bg-transparent disabled:active:bg-transparent",
		size === "default" &&
			"h-8 w-[34px] font-medium font-mono text-[15px] disabled:opacity-40",
		size === "sm" &&
			"size-[26px] font-medium font-mono text-[14px] disabled:opacity-40",
		// The database panel's control greys the sign instead of fading it.
		size === "lg" &&
			"size-8 font-normal font-sans text-[16px] disabled:text-line-control",
		// Last: tailwind-merge drops an earlier leading-* when a text-* size follows it.
		"leading-none",
	);

	return (
		<span
			data-slot="stepper"
			className={cn("inline-flex items-center gap-3", className)}
		>
			<span
				role="group"
				aria-label={label}
				aria-describedby={props["aria-describedby"]}
				aria-disabled={disabled || undefined}
				className={cn(
					"inline-flex items-center overflow-hidden border border-line-control bg-surface-well shadow-well",
					size === "sm" ? "rounded-md" : "rounded-ctl",
				)}
			>
				<button
					type="button"
					aria-label={`Decrease ${label.toLowerCase()}`}
					disabled={disabled || value <= min}
					onClick={() => set(value - step)}
					className={buttonClassName}
				>
					−
				</button>
				<output
					aria-live="polite"
					className={cn(
						"text-center font-medium font-mono",
						size === "default" &&
							"min-w-10 border-line-control border-x text-sm leading-8",
						size === "sm" &&
							"min-w-[26px] border-line-control border-x text-xs leading-6",
						size === "lg" && "min-w-10 text-sm leading-none",
					)}
				>
					{value}
				</output>
				<button
					type="button"
					aria-label={`Increase ${label.toLowerCase()}`}
					disabled={disabled || value >= max}
					onClick={() => set(value + step)}
					className={buttonClassName}
				>
					+
				</button>
			</span>
			{staged ? (
				<Badge tone="staged" className="text-2xs">
					staged · was {was}
				</Badge>
			) : null}
		</span>
	);
}
