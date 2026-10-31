import { useState } from "react";
import type { Service } from "#/api/models";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { ReplicaStrip, ScaleDialog } from "#/features/scaling";
import { autoscalingLine, replicaCounts, scaleEventLine } from "./model";

/**
 * Scaling as ruled rows: replicas ready of wanted (a big numeral with its
 * Scale button), whether autoscaling is on, the last scale event and anything
 * blocking a scale-out. A service that cannot scale says why instead of
 * offering the button.
 */
export function ScalingSummary({ service }: { service: Service }) {
	const [open, setOpen] = useState(false);
	const { scaling } = service;
	const { ready, desired } = replicaCounts(service);
	const event = scaling.lastScaleEvent;

	return (
		<div className="rule-rows">
			<div className="flex items-center justify-between gap-3">
				<div className="flex min-w-0 flex-1 flex-col gap-1.5">
					<span className="type-micro">Replicas ready</span>
					<span className="type-metric text-ink-1">
						{ready} / {scaling.replicas}
					</span>
					{desired !== scaling.replicas ? (
						<span className="text-ink-3 text-xs">{desired} running now</span>
					) : null}
					<ReplicaStrip service={service} />
				</div>
				{scaling.eligible ? (
					<Button variant="secondary" size="sm" onClick={() => setOpen(true)}>
						Scale
					</Button>
				) : null}
			</div>
			<div className="grid grid-cols-[88px_minmax(0,1fr)] items-baseline gap-3 text-xs leading-[1.4]">
				<span className="text-ink-3">Autoscaling</span>
				<span className="min-w-0 text-ink-1">
					{scaling.eligible ? autoscalingLine(scaling) : "Not available"}
				</span>
			</div>
			{event ? (
				<div className="grid grid-cols-[88px_minmax(0,1fr)] items-baseline gap-3 text-xs leading-[1.4]">
					<span className="text-ink-3">Last scaled</span>
					<span className="min-w-0 text-ink-1">
						{scaleEventLine(event)} · <Relative iso={event.at} />
					</span>
				</div>
			) : null}
			{scaling.blockedReason ? (
				<Alert variant="warning">
					<AlertDescription>{scaling.blockedReason}</AlertDescription>
				</Alert>
			) : null}
			{!scaling.eligible ? (
				<p className="text-ink-3 text-xs leading-[1.45]">
					Runs as one replica.{" "}
					{scaling.ineligibleReason ??
						"Replicas need a stateless service: no volumes and an HTTP port."}
				</p>
			) : null}
			<ScaleDialog serviceId={service.id} open={open} onOpenChange={setOpen} />
		</div>
	);
}
