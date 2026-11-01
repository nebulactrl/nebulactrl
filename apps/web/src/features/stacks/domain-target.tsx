import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { defaultDomainPort, type StackComponent } from "./model";

/**
 * Where a stack's domain points: the component (compose service) and, unless
 * it declares exactly one port, the container port. Any port from 1 to 65535
 * is accepted, since a compose file need not declare what it listens on.
 */
export function DomainTargetFields({
	components,
	component,
	port,
	onComponent,
	onPort,
	disabled = false,
}: {
	components: readonly StackComponent[];
	/** The chosen component name; "" for none yet. */
	component: string;
	port: string;
	onComponent(name: string): void;
	onPort(value: string): void;
	disabled?: boolean;
}) {
	const chosen = components.find((candidate) => candidate.name === component);
	const only = defaultDomainPort(chosen);
	const declared = chosen?.ports ?? [];
	return (
		<div className="flex flex-wrap items-center gap-2">
			<Select
				value={component === "" ? null : component}
				items={components.map((c) => ({ value: c.name, label: c.name }))}
				disabled={disabled || components.length === 0}
				onValueChange={(next) => {
					if (next === null) return;
					onComponent(next);
					onPort("");
				}}
			>
				<SelectTrigger
					aria-label="Component the domain serves"
					className="w-[180px] shrink-0 font-mono"
				>
					<SelectValue placeholder="Component" />
				</SelectTrigger>
				<SelectContent alignItemWithTrigger={false}>
					{components.map((c) => (
						<SelectItem key={c.name} value={c.name} className="font-mono">
							{c.name}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
			<Input
				aria-label="Container port"
				inputMode="numeric"
				autoComplete="off"
				disabled={disabled || component === ""}
				className="w-[130px] shrink-0 font-mono"
				placeholder={only === undefined ? "port" : `${only} (default)`}
				list={declared.length > 1 ? "stack-ports" : undefined}
				value={port}
				onChange={(event) => onPort(event.target.value)}
			/>
			{declared.length > 1 ? (
				<datalist id="stack-ports">
					{declared.map((value) => (
						<option key={value} value={value} />
					))}
				</datalist>
			) : null}
		</div>
	);
}
