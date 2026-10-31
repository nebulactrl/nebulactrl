import { FactLine } from "#/components/settings/fact-line";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import {
	type PlacementView,
	placementSummary,
	WARNING_TITLE,
	warningKind,
} from "./model";

/**
 * What a deployment plan says about placement, as a fact line under the Build
 * control: one sentence ("Builds on build-01 (sticky), pushes to
 * ghcr.io/acme, deploys to web-02"), the platform the image is built for, and
 * an alert for each warning: a fallback to the app's server, a
 * cross-architecture build under emulation, a cold build cache. A fallback or
 * an emulated build warns; a cold cache is only information.
 */
export function PlacementReadout({
	placement,
	targetName,
}: {
	placement: PlacementView;
	/** The server the service deploys to. */
	targetName: string;
}) {
	return (
		<div className="flex flex-col gap-2" data-testid="plan-placement">
			<FactLine>
				<span className="font-medium text-ink-1">
					{placementSummary(placement, { name: targetName })}
				</span>
				{placement.platform !== "" ? (
					<span className="text-ink-3">
						{" "}
						for <span className="font-mono">{placement.platform}</span>
					</span>
				) : null}
				.
			</FactLine>
			{placement.warnings.map((warning) => {
				const kind = warningKind(warning);
				return (
					<Alert
						key={`${warning.code}:${warning.message}`}
						variant={kind === "cold_cache" ? "info" : "warning"}
					>
						<AlertTitle>{WARNING_TITLE[kind]}</AlertTitle>
						<AlertDescription>{warning.message}</AlertDescription>
					</Alert>
				);
			})}
		</div>
	);
}
