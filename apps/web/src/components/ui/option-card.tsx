import { Radio as RadioPrimitive } from "@base-ui/react/radio";
import { RadioGroup as RadioGroupPrimitive } from "@base-ui/react/radio-group";
import { cn } from "cn";
import { Check } from "lucide-react";
import type { ReactNode } from "react";

export interface OptionCardGroupProps<T extends string> {
	value: T;
	onValueChange: (value: T) => void;
	/** Accessible name; or pair with a visible label via `aria-labelledby`. */
	"aria-label"?: string;
	"aria-labelledby"?: string;
	/** Ids of the text that says why the group is disabled. */
	"aria-describedby"?: string;
	disabled?: boolean;
	className?: string;
	children: ReactNode;
}

/** A row of `OptionCard`s where exactly one is chosen. Wraps by default. */
export function OptionCardGroup<T extends string>({
	value,
	onValueChange,
	disabled = false,
	className,
	children,
	...aria
}: OptionCardGroupProps<T>) {
	return (
		<RadioGroupPrimitive
			data-slot="option-card-group"
			value={value}
			onValueChange={(next) => onValueChange(next as T)}
			disabled={disabled}
			className={cn("flex flex-wrap gap-2", className)}
			{...aria}
		>
			{children}
		</RadioGroupPrimitive>
	);
}

export interface OptionCardProps {
	value: string;
	/** The option's name: "M", "Web + Postgres". */
	title: ReactNode;
	/** One line of what it means: "1 vCPU · 1 GiB". */
	description?: ReactNode;
	/** Mono title and description, for sizes and identifiers. */
	mono?: boolean;
	/** A dashed outline for the open-ended choice ("Custom") until picked. */
	dashed?: boolean;
	disabled?: boolean;
	className?: string;
}

/**
 * A choice that needs a line of explanation: an instance size, a project
 * template. Selected sinks into the surface: an accent border on an accent wash, with a check.
 */
export function OptionCard({
	value,
	title,
	description,
	mono = false,
	dashed = false,
	disabled = false,
	className,
}: OptionCardProps) {
	return (
		<RadioPrimitive.Root
			data-slot="option-card"
			value={value}
			disabled={disabled}
			className={cn(
				"surface-raised pressable relative flex min-w-[120px] cursor-pointer flex-col items-start gap-1.5 rounded-card px-3 py-2.5 text-left text-ink-1 outline-none hover:border-line-control focus-visible:focus-ring data-checked:border-accent-ink data-checked:bg-none data-checked:bg-accent-wash data-checked:shadow-pressed data-disabled:cursor-not-allowed data-disabled:opacity-50",
				dashed &&
					"border-line-control! border-dashed! data-checked:border-accent-ink! data-checked:border-solid!",
				className,
			)}
		>
			<RadioPrimitive.Indicator
				data-slot="option-card-indicator"
				className="absolute top-2.5 right-2.5 text-accent-ink"
			>
				<Check aria-hidden="true" className="size-3.5" />
			</RadioPrimitive.Indicator>
			<span
				className={cn("font-medium text-sm leading-none", mono && "font-mono")}
			>
				{title}
			</span>
			{description ? (
				<span
					className={cn(
						"text-ink-3",
						mono ? "font-mono text-2xs leading-none" : "text-xs leading-[1.3]",
					)}
				>
					{description}
				</span>
			) : null}
		</RadioPrimitive.Root>
	);
}
