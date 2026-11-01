import { Toggle } from "#/components/ui/toggle";
import type { ProcessOption } from "./model";

/**
 * Filter chips for the log views of a service with processes: "All
 * processes" plus one chip per process, exactly one pressed. Same shape as
 * the component filter of a stack; each chip says the name in words.
 */
export function ProcessFilter({
	processes,
	value,
	onChange,
}: {
	processes: readonly ProcessOption[];
	/** The pressed process' name; undefined is all processes. */
	value: string | undefined;
	onChange(next: string | undefined): void;
}) {
	return (
		<ul
			aria-label="Show logs of"
			className="m-0 flex list-none flex-wrap items-center gap-1.5 p-0"
		>
			<li>
				<Toggle
					variant="outline"
					size="sm"
					pressed={value === undefined}
					onPressedChange={() => onChange(undefined)}
					className="font-mono text-2xs"
				>
					All processes
				</Toggle>
			</li>
			{processes.map((process) => (
				<li key={process.name}>
					<Toggle
						variant="outline"
						size="sm"
						pressed={value === process.name}
						onPressedChange={() => onChange(process.name)}
						className="font-mono text-2xs"
					>
						{process.name}
					</Toggle>
				</li>
			))}
		</ul>
	);
}
