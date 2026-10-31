import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { errorMessage, errorStatus } from "#/api/client-config";
import { getSystemQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { setMcpEnabled } from "#/api/gen/sdk.gen";
import { ErrorState } from "#/components/common/error-state";
import { Badge } from "#/components/ui/badge";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { mcpSystemQuery } from "./mcp-state";
import { SettingsPlate } from "./section";

/**
 * Admin → MCP: whether AI clients may connect to this installation. Off by
 * default; while it is off `/mcp` answers 404 and REST and the CLI work either
 * way. While NEBULACTRL_MCP_ENABLED is set on the control plane it decides, and
 * the page says so instead of offering a switch.
 */
export function McpSwitchCard() {
	const queryClient = useQueryClient();
	const system = useQuery(mcpSystemQuery());
	const toggle = useMutation({
		mutationFn: async (enabled: boolean) => {
			await setMcpEnabled({ body: { enabled } });
		},
		onSuccess: (_data, enabled) => {
			toast.success(
				enabled ? "MCP turned on" : "MCP turned off",
				enabled
					? "/mcp answers now. People can create a token for their AI client."
					: "/mcp answers 404 now.",
			);
		},
		onError: (error) => {
			toast.error("Could not switch MCP", errorMessage(error));
		},
		onSettled: async (_data, error) => {
			// A 409 means the environment decides: show the locked state.
			if (!error || errorStatus(error) === 409) {
				await queryClient.invalidateQueries({ queryKey: getSystemQueryKey() });
			}
		},
	});

	return (
		<SkeletonReveal
			loading={system.isPending}
			skeleton={<Skeleton className="h-32 rounded-card" />}
		>
			{() => {
				const data = system.data;
				if (!data) {
					return (
						<ErrorState
							frame="raised"
							title="The MCP state did not load"
							body="The control plane did not report whether MCP is on. Nothing was changed; try again."
							retry={() => void system.refetch()}
						/>
					);
				}
				return (
					<SettingsPlate
						id="mcp-switch"
						title="Allow AI clients to connect"
						tipLabel="About MCP"
						tip="Off by default. Anyone in a team can then create a token for their AI client under Account settings → MCP clients; every action keeps the same checks and audit trail."
						actions={
							<Badge
								tone={data.mcpEnabled ? "ok" : "off"}
								lamp
								shape="capsule"
								className="shrink-0"
							>
								{data.mcpEnabled ? "On" : "Off"}
							</Badge>
						}
					>
						{data.mcpLocked ? (
							<p className="type-secondary text-ink-2">
								MCP is {data.mcpEnabled ? "on" : "off"}.{" "}
								<code className="type-mono">NEBULACTRL_MCP_ENABLED</code> on the
								control plane decides this.
							</p>
						) : (
							<div className="flex items-center justify-between gap-4">
								<label
									htmlFor="mcp-enabled"
									className="type-secondary text-ink-2"
								>
									When it is off, <code className="type-mono">/mcp</code>{" "}
									answers 404. The REST API and the CLI work either way.
								</label>
								<Switch
									id="mcp-enabled"
									checked={data.mcpEnabled}
									disabled={toggle.isPending}
									onCheckedChange={(next) => toggle.mutate(next)}
								/>
							</div>
						)}
					</SettingsPlate>
				);
			}}
		</SkeletonReveal>
	);
}
