import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import type { ProcessOption } from "#/features/processes/model";

/** The value of the "all processes" item; no process can be called this (names are lowercase DNS labels with no underscores). */
const ALL = "__all__";

/**
 * Narrows a service's metrics to one of its processes. "All processes" is the
 * service total (every process summed), the default.
 */
export function ProcessSelect({
	processes,
	value,
	onChange,
}: {
	processes: readonly ProcessOption[];
	/** The picked process' name; undefined is all processes. */
	value: string | undefined;
	onChange(next: string | undefined): void;
}) {
	const items = [
		{ value: ALL, label: "All processes" },
		...processes.map((process) => ({
			value: process.name,
			label: process.name,
		})),
	];
	return (
		<Select
			value={value ?? ALL}
			items={items}
			onValueChange={(next) => {
				if (next === null) return;
				onChange(next === ALL ? undefined : next);
			}}
		>
			<SelectTrigger aria-label="Process" className="w-[190px] shrink-0">
				<SelectValue />
			</SelectTrigger>
			<SelectContent alignItemWithTrigger={false}>
				{items.map((item) => (
					<SelectItem
						key={item.value}
						value={item.value}
						className={item.value === ALL ? undefined : "font-mono"}
					>
						{item.label}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}
