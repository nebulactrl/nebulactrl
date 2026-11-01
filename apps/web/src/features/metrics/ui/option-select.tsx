import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";

interface Option<T extends string> {
	value: T;
	label: string;
}

export interface OptionSelectProps<T extends string> {
	value: T;
	onChange: (value: T) => void;
	options: readonly Option<T>[];
	"aria-label": string;
	className?: string;
}

/** A compact single-choice dropdown over the shared Select. */
export function OptionSelect<T extends string>({
	value,
	onChange,
	options,
	className,
	...props
}: OptionSelectProps<T>) {
	return (
		<Select
			value={value}
			items={[...options]}
			onValueChange={(next) => {
				if (typeof next === "string") onChange(next as T);
			}}
		>
			<SelectTrigger
				size="sm"
				aria-label={props["aria-label"]}
				className={className}
			>
				<SelectValue />
			</SelectTrigger>
			<SelectContent alignItemWithTrigger={false} align="start">
				{options.map((option) => (
					<SelectItem key={option.value} value={option.value}>
						{option.label}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}
