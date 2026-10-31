import { CopyButton } from "#/components/common/copy-button";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { agentUpgradeCommand } from "../lib/errors";

/**
 * The server's agent predates proxy v2, so domain options, external routes,
 * TCP/UDP ports, certificates and the tailnet cannot be staged. Says how to
 * upgrade: re-run the installer from this control plane.
 */
export function AgentTooOldBanner({ serverName }: { serverName?: string }) {
	const command = agentUpgradeCommand(window.location.origin);
	return (
		<Alert variant="warning" data-testid="agent-too-old">
			<AlertTitle>
				The agent{serverName ? ` on ${serverName}` : ""} is too old for this
			</AlertTitle>
			<AlertDescription>
				<p>
					Upgrade it on the server, then try again. The installer replaces the
					binary and restarts the agent; running containers and the proxy stay
					up.
				</p>
				<div className="surface-well mt-2 flex items-start gap-2 rounded-ctl py-2 pr-2 pl-3">
					<code className="min-w-0 flex-1 select-all break-all font-mono text-2xs text-ink-1 leading-[1.7]">
						{command}
					</code>
					<CopyButton variant="chip" value={command} className="shrink-0" />
				</div>
			</AlertDescription>
		</Alert>
	);
}
