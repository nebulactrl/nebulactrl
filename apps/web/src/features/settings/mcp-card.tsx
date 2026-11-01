import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { PlusIcon } from "lucide-react";
import { meQuery } from "#/api/hooks";
import { CodeBlock } from "#/components/common/code-block";
import { CopyButton } from "#/components/common/copy-button";
import { ErrorState } from "#/components/common/error-state";
import { Button } from "#/components/ui/button";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { mcpSystemQuery } from "./mcp-state";
import { SettingsNotice, SettingsPlate } from "./section";
import { mcpClientConfig } from "./token-presets";

/**
 * Account settings → MCP clients: the endpoint, a client config snippet in the
 * common `mcpServers` shape with a bearer header, and a shortcut to mint a
 * token scoped for it. While the instance admin has MCP off there is nothing to
 * set up, and the page says who can change that. The switch is in Admin → MCP.
 */
export function McpClientsCard({
	onCreateToken,
}: {
	onCreateToken: () => void;
}) {
	const system = useQuery(mcpSystemQuery());
	const me = useQuery(meQuery());

	return (
		<SkeletonReveal
			loading={system.isPending}
			skeleton={<Skeleton className="h-56 rounded-card" />}
		>
			{() => {
				if (!system.data) {
					return (
						<ErrorState
							frame="raised"
							title="The MCP state did not load"
							body="The control plane did not report whether MCP is on. Nothing was changed; try again."
							retry={() => void system.refetch()}
						/>
					);
				}
				if (!system.data.mcpEnabled) {
					return me.data?.instanceAdmin ? (
						<SettingsNotice>
							MCP is off. Turn it on in{" "}
							<Link
								to="/admin/$section"
								params={{ section: "mcp" }}
								className="text-accent-ink underline"
							>
								Admin → MCP
							</Link>
							.
						</SettingsNotice>
					) : (
						<SettingsNotice>
							Your instance admin has switched MCP off.
						</SettingsNotice>
					);
				}
				// With no public URL configured the server cannot name itself; the
				// address the browser reached it on is the honest fallback.
				const url = system.data.mcpUrl || `${window.location.origin}/mcp`;
				const config = mcpClientConfig(url);
				return (
					<>
						<SettingsPlate
							id="mcp-endpoint"
							title="Endpoint"
							actions={
								<Button variant="primary" size="sm" onClick={onCreateToken}>
									<PlusIcon />
									Create an MCP token
								</Button>
							}
						>
							<div className="surface-well flex items-center gap-2 rounded-ctl py-1.5 pr-1.5 pl-3">
								<code className="min-w-0 flex-1 truncate font-mono text-ink-1 text-xs">
									{url}
								</code>
								<CopyButton variant="chip" value={url} />
							</div>
							{system.data.mcpUrl ? null : (
								<p className="text-ink-3 text-xs">
									<code className="type-mono">NEBULACTRL_PUBLIC_URL</code> is
									not set, so this is the address you are on.
								</p>
							)}
						</SettingsPlate>
						<SettingsPlate
							id="mcp-config"
							title="Client config"
							tipLabel="About the client config"
							tip="Edits stage until applied, and an AI client needs your approval to change production. Each token is limited to 60 requests per minute."
						>
							<p className="type-secondary text-ink-3">
								Paste it into any client that reads{" "}
								<code className="type-mono text-ink-2">mcpServers</code>, with a
								token you create here.
							</p>
							<CodeBlock copyValue={config}>
								<pre className="whitespace-pre-wrap">{config}</pre>
							</CodeBlock>
						</SettingsPlate>
					</>
				);
			}}
		</SkeletonReveal>
	);
}
