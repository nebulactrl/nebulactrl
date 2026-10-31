import type { Deployment, Service } from "#/api/models";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { isInFlight } from "./labels";

/**
 * "Waiting for <server> (offline since …)": an in-flight deployment whose
 * server is not connected. The command is saved and goes out when the agent
 * is back; nothing has changed on the host yet.
 */
export function WaitingForServer({ deployment }: { deployment: Deployment }) {
	const waiting = deployment.waitingFor;
	if (!waiting) return null;
	return (
		<Alert variant="warning">
			<AlertTitle>
				Waiting for {waiting.serverName}
				{waiting.offlineSince ? (
					<>
						{" "}
						(offline since <Relative iso={waiting.offlineSince} />)
					</>
				) : null}
			</AlertTitle>
			<AlertDescription>
				The deployment is saved and is sent when the agent reconnects. If it is
				not delivered within 15 minutes it fails and no changes are made. A
				deployment the agent already accepted keeps waiting: it never times out
				on its own.
			</AlertDescription>
		</Alert>
	);
}

/**
 * What the latest deployment means for the reader right now: waiting for an
 * offline server, a failed deploy with the previous release still serving,
 * or (the serious one) a rollback that failed and needs hands on the host.
 */
export function RecoveryBanner({
	deployment,
	service,
}: {
	deployment: Deployment;
	service: Service;
}) {
	if (isInFlight(deployment.status))
		return <WaitingForServer deployment={deployment} />;

	if (deployment.status === "rollback_failed") {
		const server = service.server?.name ?? "the server";
		return (
			<Alert variant="destructive">
				<AlertTitle>
					Rollback failed: {service.name} may not be serving
				</AlertTitle>
				<AlertDescription>
					<p>
						The new release did not pass verification, and putting the previous
						one back also failed.
						{deployment.statusMessage ? ` ${deployment.statusMessage}` : ""}
					</p>
					<p className="font-medium text-ink-1">Recover by hand:</p>
					<ol className="list-decimal space-y-1 pl-4">
						<li>
							Check {server} is online and its proxy reports ok (Servers →{" "}
							{server}).
						</li>
						<li>
							On the host, list the containers:{" "}
							<code className="font-mono text-[11px] text-ink-1">
								docker ps -a --filter label=nebulactrl.service={service.id}
							</code>
						</li>
						<li>
							Remove any broken one with{" "}
							<code className="font-mono text-[11px] text-ink-1">
								docker rm -f &lt;name&gt;
							</code>
							, then press Redeploy. The previous image stays on the host until
							it is pruned.
						</li>
					</ol>
				</AlertDescription>
			</Alert>
		);
	}

	if (deployment.status === "failed" && service.currentDeploymentId !== null) {
		return (
			<Alert variant="warning">
				<AlertTitle>
					Deployment failed: the previous release is still serving
				</AlertTitle>
				<AlertDescription>
					{deployment.statusMessage || "Open the deployment below for the log."}{" "}
					Nothing was taken down.
				</AlertDescription>
			</Alert>
		);
	}

	if (deployment.status === "rolled_back") {
		return (
			<Alert variant="warning">
				<AlertTitle>
					The new release failed verification and was rolled back
				</AlertTitle>
				<AlertDescription>
					{deployment.statusMessage || "The previous release was restored."}
				</AlertDescription>
			</Alert>
		);
	}
	return null;
}
