import { Link } from "@tanstack/react-router";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { MetricsPanel } from "#/features/metrics";
import { HostDetails } from "../host-details";
import type { ServerView } from "../model";
import { PlacedServices } from "../placed-services";
import { useServer } from "../server-context";
import { ServerSection } from "../server-section";
import { ConnectCard } from "./connect-card";
import { FactsStrip } from "./facts-strip";
import { TrafficSummary } from "./traffic-summary";

const LINK =
	"rounded-sm text-ink-1 underline underline-offset-2 outline-none focus-visible:focus-ring";

/** What needs a look, one callout each and only while it is true. */
function Warnings({ server }: { server: ServerView }) {
	const params = { serverId: server.id };
	return (
		<>
			{server.status === "offline" ? (
				<Alert variant="warning">
					<AlertTitle>The agent is not reporting</AlertTitle>
					<AlertDescription>
						Nothing has arrived for over 30 seconds. Data on this page is the
						last that came in; deployments to this server wait until it is back.
					</AlertDescription>
				</Alert>
			) : null}
			{server.diskAlert ? (
				<Alert variant="warning">
					<AlertTitle>Disk is over its alert threshold</AlertTitle>
					<AlertDescription>
						Usage is at or above {server.settings.diskAlertThresholdPercent}%.
						Free space or prune old images before deployments fail.{" "}
						<Link
							to="/servers/$serverId/settings/$section"
							params={{ ...params, section: "logs-and-disk" }}
							className={LINK}
						>
							Open Docker cleanup
						</Link>
					</AlertDescription>
				</Alert>
			) : null}
			{server.patches.known && server.patches.rebootRequired ? (
				<Alert variant="warning">
					<AlertTitle>Reboot required</AlertTitle>
					<AlertDescription>
						Installed updates only take effect after a reboot.{" "}
						<Link
							to="/servers/$serverId/updates"
							params={params}
							className={LINK}
						>
							Open Updates
						</Link>
					</AlertDescription>
				</Alert>
			) : null}
			{server.unrestricted ? (
				<Alert variant="warning">
					<AlertTitle>Unrestricted</AlertTitle>
					<AlertDescription>
						Stacks and services on this server can use privileged mode, host
						networking, devices, GPUs and the other host-access options.{" "}
						<Link
							to="/servers/$serverId/settings/$section"
							params={{ ...params, section: "danger" }}
							className={LINK}
						>
							Open the danger zone
						</Link>
					</AlertDescription>
				</Alert>
			) : null}
		</>
	);
}

/**
 * The server's Overview, about two screens: what the host is, its three
 * resource charts, traffic in four numbers, the services placed on it, and
 * any warnings. Everything deeper has a tab of its own.
 */
export function OverviewTab() {
	const server = useServer();
	const pending = server.status === "pending";
	return (
		<>
			{pending ? (
				<ServerSection id="connect" title="Connect this host">
					<ConnectCard server={server} />
				</ServerSection>
			) : null}
			<Warnings server={server} />
			<FactsStrip server={server} />
			{pending ? null : (
				<ServerSection id="charts" title="Charts">
					<MetricsPanel scope={{ serverId: server.id }} />
				</ServerSection>
			)}
			<ServerSection id="traffic" title="Traffic">
				<TrafficSummary server={server} />
			</ServerSection>
			<ServerSection id="services" title="Services on this host">
				<PlacedServices serverId={server.id} />
			</ServerSection>
			{pending ? null : (
				<ServerSection id="host" title="Host details">
					<HostDetails server={server} />
				</ServerSection>
			)}
		</>
	);
}
