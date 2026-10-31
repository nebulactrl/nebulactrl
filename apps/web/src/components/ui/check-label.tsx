import { cn } from "cn";
import { type ReactNode, useId } from "react";
import { Checkbox } from "#/components/ui/checkbox";

export interface CheckLabelProps {
	checked: boolean;
	onCheckedChange(checked: boolean): void;
	disabled?: boolean;
	children: ReactNode;
	/** Lays out the checkbox beside its label; defaults to a centred row. */
	className?: string;
	/** Classes for the clickable label text. */
	labelClassName?: string;
	/** Classes for the checkbox itself (e.g. `mt-0.5` to align with a first line). */
	checkboxClassName?: string;
}

/**
 * A checkbox with its label, associated by id so a click on the text toggles
 * it and assistive tech reads them together.
 */
export function CheckLabel({
	checked,
	onCheckedChange,
	disabled,
	children,
	className,
	labelClassName,
	checkboxClassName,
}: CheckLabelProps) {
	const id = useId();
	return (
		<div className={cn("flex items-center gap-2", className)}>
			<Checkbox
				id={id}
				checked={checked}
				disabled={disabled ?? false}
				onCheckedChange={(next) => onCheckedChange(next === true)}
				className={checkboxClassName}
			/>
			<label
				htmlFor={id}
				className={cn(
					"min-w-0 flex-1 cursor-pointer select-none",
					disabled && "cursor-not-allowed opacity-50",
					labelClassName,
				)}
			>
				{children}
			</label>
		</div>
	);
}
