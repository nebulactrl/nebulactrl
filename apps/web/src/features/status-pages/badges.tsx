import { cn } from "cn";
import { Badge } from "#/components/ui/badge";
import type { LampTone } from "#/components/ui/lamp";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	IMPACT_LABEL,
	type IncidentImpact,
	type IncidentStatus,
	type PageState,
	PHASE_LABEL,
	STATE_KIND,
	STATE_LABEL,
	STATUS_LABEL,
} from "./model";

const IMPACT_TONE: Record<IncidentImpact, LampTone | undefined> = {
	none: undefined,
	minor: "staged",
	major: "degraded",
	critical: "failed",
};

/**
 * How bad an incident is, in words on a tone wash. An incident is a written
 * statement, not a live resource, so it carries no status icon.
 */
export function ImpactBadge({ impact }: { impact: IncidentImpact }) {
	const tone = IMPACT_TONE[impact];
	return tone ? (
		<Badge tone={tone}>{IMPACT_LABEL[impact]}</Badge>
	) : (
		<Badge variant="outline">{IMPACT_LABEL[impact]}</Badge>
	);
}

/** Where an incident is in its life: resolved is green words, the rest an outline. */
export function IncidentStatusBadge({ status }: { status: IncidentStatus }) {
	return status === "resolved" ? (
		<Badge variant="success">{STATUS_LABEL[status]}</Badge>
	) : (
		<Badge variant="outline">{STATUS_LABEL[status]}</Badge>
	);
}

export function PhaseBadge({ phase }: { phase: keyof typeof PHASE_LABEL }) {
	return phase === "active" ? (
		<Badge variant="accent">{PHASE_LABEL[phase]}</Badge>
	) : (
		<Badge variant="outline">{PHASE_LABEL[phase]}</Badge>
	);
}

/**
 * The live state of a whole page: the status icon and the words. This one is a
 * live resource status (derived from the services' checks), so the icon is
 * right here.
 */
export function StateLabel({
	state,
	className,
}: {
	state: PageState;
	className?: string;
}) {
	return (
		<span
			className={cn(
				"inline-flex items-center gap-1.5 text-ink-1 text-xs",
				className,
			)}
		>
			<StatusIcon kind={STATE_KIND[state]} label="" />
			{STATE_LABEL[state]}
		</span>
	);
}
