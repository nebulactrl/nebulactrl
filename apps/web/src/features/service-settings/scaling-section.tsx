import type { Service } from "#/api/models";
import { hasProcesses } from "#/features/processes/model";
import { ScaleRows } from "#/features/processes/scale-rows";
import { ScalingForm, scalingSummary } from "#/features/scaling";
import { Section, SectionBody } from "./fields";

/**
 * Replica count and CPU autoscaling. Unlike every other section it is not
 * part of the settings draft: scaling is operational, so its own Apply acts
 * on the running service immediately and nothing is staged for a deploy. The
 * header says so ("Applies now"), so it is never mistaken for the limits under
 * it, which wait for the save bar and the next deploy. A service with several
 * processes scales each one on its own.
 */
export function ScalingSection({ service }: { service: Service }) {
	const several = hasProcesses(service);
	return (
		<Section
			title="Scaling"
			field="scale"
			description={
				several
					? "How many copies of each process run, without a deploy or a rebuild."
					: "How many copies of the current release run behind the proxy, without a deploy or a rebuild."
			}
			aside={
				<>
					Applies now
					{!several && service.scaling.eligible ? (
						<>
							{" · "}
							<span className="font-mono">
								{scalingSummary(service.scaling)}
							</span>
						</>
					) : null}
				</>
			}
		>
			<SectionBody>
				{several ? (
					<ScaleRows service={service} />
				) : (
					<ScalingForm service={service} primary={false} />
				)}
			</SectionBody>
		</Section>
	);
}
