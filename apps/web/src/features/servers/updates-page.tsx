import { AgentUpgradeCard } from "./agent-upgrade/agent-upgrade";
import { UpdatesTab } from "./patching/updates-tab";
import { useServer } from "./server-context";

/** The Updates tab: the host's OS updates, then the agent's own version and upgrade. */
export function UpdatesPage() {
	const server = useServer();
	return (
		<>
			<UpdatesTab key={`updates-${server.id}`} server={server} />
			<AgentUpgradeCard serverId={server.id} />
		</>
	);
}
