import { Badge } from "#/components/ui/badge";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { DRAIN_STATE_LABEL, type DrainState } from "./model";

const KIND: Record<
	Exclude<DrainState, "none">,
	{ tone: "deploying" | "stopped" | "failed"; kind: StatusKind }
> = {
	draining: { tone: "deploying", kind: "deploying" },
	drained: { tone: "stopped", kind: "stopped" },
	failed: { tone: "failed", kind: "failed" },
};

/**
 * A server's drain state as a status icon plus its word ("Draining",
 * "Drained", "Drain failed"); renders nothing for a server that is not being
 * drained.
 */
export function DrainChip({
	state,
	message,
}: {
	state: DrainState;
	message?: string;
}) {
	if (state === "none") return null;
	const { tone, kind } = KIND[state];
	return (
		<Badge
			tone={tone}
			shape="capsule"
			className="gap-1.5 py-1 font-sans text-xs"
			{...(message ? { title: message } : {})}
		>
			<StatusIcon kind={kind} label="" />
			{DRAIN_STATE_LABEL[state]}
		</Badge>
	);
}
