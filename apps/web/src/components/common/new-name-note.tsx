import { InfoIcon } from "lucide-react";
import { NEW_NAME_NOTE } from "#/lib/dns-new-name";

/**
 * The quiet heads-up under a passing DNS check while the name is new: a
 * resolver on the user's own network may still remember it as missing. One
 * line of small text with an info icon, not a callout.
 */
export function NewNameNote() {
	return (
		<p className="m-0 flex items-start gap-1.5 text-ink-3 text-xs leading-[1.5]">
			<InfoIcon aria-hidden className="mt-0.5 size-3.5 shrink-0" />
			<span>{NEW_NAME_NOTE}</span>
		</p>
	);
}
