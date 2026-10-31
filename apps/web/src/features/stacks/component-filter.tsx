import { Toggle } from "#/components/ui/toggle";
import type { StackComponent } from "./model";

/**
 * Filter chips for a stack's log views: "All" plus one chip per component,
 * exactly one pressed. The names are the compose service names; each chip
 * says "name" in words, so nothing rests on colour.
 */
export function ComponentFilter({
	components,
	value,
	onChange,
}: {
	components: readonly StackComponent[];
	/** The pressed component's name; undefined is All. */
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
					All components
				</Toggle>
			</li>
			{components.map((component) => (
				<li key={component.name}>
					<Toggle
						variant="outline"
						size="sm"
						pressed={value === component.name}
						onPressedChange={() => onChange(component.name)}
						className="font-mono text-2xs"
					>
						{component.name}
					</Toggle>
				</li>
			))}
		</ul>
	);
}
