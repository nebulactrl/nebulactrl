import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import type { DomainProcessOption } from "./model";

/**
 * The process a new domain serves when the reader has not picked one: the
 * default process when it has a port, else the only process with one. Empty
 * when the choice is the reader's (several ports, none of them the default's)
 * or there is nothing to choose.
 */
export function effectiveProcess(
	options: readonly DomainProcessOption[],
	picked: string,
): string {
	if (options.some((option) => option.name === picked)) return picked;
	const standard = options.find((option) => option.isDefault);
	if (standard) return standard.name;
	return options.length === 1 ? (options[0]?.name ?? "") : "";
}

/**
 * Where a service's domain points when it runs several processes: the process
 * that serves it. A process has one port, so the port is the process' own and
 * read-only here; a process without a port is not offered.
 */
export function DomainProcessTarget({
	options,
	process,
	onProcess,
	disabled = false,
}: {
	options: readonly DomainProcessOption[];
	/** The chosen process name; "" for none yet. */
	process: string;
	onProcess(name: string): void;
	disabled?: boolean;
}) {
	const chosen = options.find((option) => option.name === process);
	return (
		<div className="flex flex-wrap items-center gap-2">
			<Select
				value={process === "" ? null : process}
				items={options.map((option) => ({
					value: option.name,
					label: option.name,
				}))}
				disabled={disabled || options.length === 0}
				onValueChange={(next) => {
					if (next !== null) onProcess(next);
				}}
			>
				<SelectTrigger
					aria-label="Process the domain serves"
					className="w-[180px] shrink-0 font-mono"
				>
					<SelectValue placeholder="Process" />
				</SelectTrigger>
				<SelectContent alignItemWithTrigger={false}>
					{options.map((option) => (
						<SelectItem
							key={option.name}
							value={option.name}
							className="font-mono"
						>
							{option.name}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
			<Input
				aria-label="Container port"
				readOnly
				autoComplete="off"
				disabled={disabled || chosen === undefined}
				className="w-[130px] shrink-0 font-mono"
				placeholder="port"
				title="A domain uses the port of the process it serves"
				value={chosen ? String(chosen.port) : ""}
			/>
			{options.length === 0 ? (
				<span className="type-secondary">
					Give a process a port before adding a domain.
				</span>
			) : null}
		</div>
	);
}
