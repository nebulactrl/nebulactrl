import {
	Alert,
	AlertAction,
	AlertDescription,
	AlertTitle,
} from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { openAdoption } from "#/features/server-adoption/store";
import type { ServerView } from "../model";

/**
 * The server's workloads from an earlier NebulaCtrl: while held, nothing on
 * the host is changed until they are reviewed; afterwards, the ones left
 * running unmanaged stay listed here, with a way back to the review.
 */
export function AdoptionCard({ server }: { server: ServerView }) {
	const adoption = server.adoption;
	if (!adoption) return null;
	const held = adoption.state === "held";
	if (!held && adoption.unmanaged === 0) return null;
	return (
		<Alert variant={held ? "warning" : "info"} role="note">
			<AlertTitle>
				{held
					? "Review the workloads on this host"
					: "Workloads run here unmanaged"}
			</AlertTitle>
			<AlertDescription className="sm:pr-44">
				{held
					? `This host runs ${adoption.found} ${adoption.found === 1 ? "service" : "services"} from an earlier NebulaCtrl. Nothing on it is changed until you review them.`
					: `${adoption.unmanaged} ${adoption.unmanaged === 1 ? "workload runs" : "workloads run"} here unmanaged. NebulaCtrl does not touch them.`}
			</AlertDescription>
			<AlertAction>
				<Button
					variant={held ? "primary" : "secondary"}
					size="sm"
					onClick={() => openAdoption(server.id)}
				>
					{held ? "Review workloads" : "Review"}
				</Button>
			</AlertAction>
		</Alert>
	);
}
