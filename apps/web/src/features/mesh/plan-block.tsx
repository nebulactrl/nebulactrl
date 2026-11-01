import { Link } from "@tanstack/react-router";
import { NetworkIcon } from "lucide-react";
import { Alert, AlertDescription } from "#/components/ui/alert";
import {
	blockerAction,
	linkPlanLabel,
	type MeshBlocker,
	type MeshPlan,
	planHeadline,
	purposeLabel,
	stateChip,
} from "./model";
import { StateLabel } from "./state-label";

/**
 * Each thing that stops a mesh action, as a destructive callout with the way
 * forward as a link when the code says one ("Enable mesh on db-01").
 */
export function MeshBlockers({
	blockers,
}: {
	blockers: readonly MeshBlocker[];
}) {
	return (
		<>
			{blockers.map((blocker) => {
				const action = blockerAction(blocker);
				return (
					<Alert
						key={`${blocker.code}:${blocker.serverId ?? ""}`}
						variant="destructive"
					>
						<AlertDescription>
							{blocker.message}
							{action ? (
								<>
									{" "}
									{action.serverId ? (
										<Link
											to="/servers/$serverId/settings/$section"
											params={{ serverId: action.serverId, section: "mesh" }}
											className="text-accent-ink underline"
										>
											{action.text}
										</Link>
									) : (
										<Link
											to="/settings/$section"
											params={{ section: "server-mesh" }}
											className="text-accent-ink underline"
										>
											{action.text}
										</Link>
									)}
								</>
							) : null}
						</AlertDescription>
					</Alert>
				);
			})}
		</>
	);
}

/**
 * What an action does to the mesh, in the words of the plan: the headline
 * ("Connect over the mesh: adds 1 device"), each link it needs, and each thing
 * that stops it with the way forward. Shown in the deployment plan and in the
 * link review. Renders nothing when no service crosses a server.
 */
export function MeshPlanBlock({
	mesh,
	className,
}: {
	mesh: MeshPlan | null | undefined;
	className?: string | undefined;
}) {
	const headline = planHeadline(mesh);
	if (!mesh || !headline) return null;
	return (
		<section
			aria-label="Mesh"
			data-testid="mesh-plan"
			className={className ?? "flex flex-col gap-2"}
		>
			<div className="surface-well flex flex-col gap-2 rounded-card p-3">
				<p className="flex items-center gap-2 font-medium text-ink-1 text-xs">
					<NetworkIcon aria-hidden className="size-3.5 shrink-0 text-ink-3" />
					{headline}
				</p>
				{mesh.links.length > 0 ? (
					<ul className="flex flex-col gap-1">
						{mesh.links.map((link) => (
							<li
								key={`${link.fromServerId}>${link.toServerId}:${link.purpose}`}
								className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-2xs text-ink-3"
							>
								<span className="font-mono text-ink-2">
									{linkPlanLabel(link)}
								</span>
								<span>{purposeLabel(link.purpose).toLowerCase()}</span>
								{link.providers.length > 0 ? (
									<span className="font-mono">{link.providers.join(", ")}</span>
								) : null}
								{link.state === "new" ? (
									<span className="text-ink-2">new link, adds a device</span>
								) : (
									<StateLabel
										chip={stateChip(link.state)}
										className="text-2xs"
									/>
								)}
							</li>
						))}
					</ul>
				) : null}
			</div>
			<MeshBlockers blockers={mesh.blockers} />
		</section>
	);
}
