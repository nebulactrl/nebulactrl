import { Segmented } from "#/components/ui/segmented";
import { CENTRAL_TAB_LABEL } from "../central/model";
import type { LogsMode } from "./url";

/**
 * Live (the tail, as ever), History (what the server stored) and, for a
 * service, Central logs (what the team's log store holds from every server).
 * Text labels, so the choice never rests on an icon; the address keeps it as
 * `?logs=`.
 */
export function LogsModeToggle({
	mode,
	onChange,
	central = false,
}: {
	mode: LogsMode;
	onChange(next: LogsMode): void;
	/** Offer Central logs: a service's tab has it, an environment's view does not. */
	central?: boolean;
}) {
	return (
		<Segmented<LogsMode>
			aria-label="Log source"
			font="sans"
			density="regular"
			value={mode}
			onValueChange={onChange}
			options={[
				{ value: "live", label: "Live" },
				{ value: "history", label: "History" },
				...(central
					? [{ value: "central" as const, label: CENTRAL_TAB_LABEL }]
					: []),
			]}
		/>
	);
}
