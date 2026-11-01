import { useMutation, useQueryClient } from "@tanstack/react-query";
import { errorMessage } from "#/api/client-config";
import { revokeServer } from "#/api/gen/sdk.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "#/components/ui/card";
import { KvRow } from "#/components/ui/kv-row";
import { toast } from "#/components/ui/toast";
import { DrainChip } from "#/features/fleet/drain-chip";
import { canStartDrain } from "#/features/fleet/model";
import type { ServerView } from "./model";
import { serverViewQuery, serverViewsQuery } from "./queries";
import { RemoveServerButton } from "./remove-server";
import { useOpenDrain } from "./server-context";
import { UnrestrictedCard } from "./unrestricted-card";

/** Drain: move every service elsewhere and close the server to new ones. */
function DrainZone({ server }: { server: ServerView }) {
	const openDrain = useOpenDrain();
	const state = server.drainState;
	const description =
		state === "draining"
			? "This server is being drained. Its services move one at a time; follow them in the drain sheet."
			: state === "drained"
				? "This server is drained: nothing runs here and it accepts no new services."
				: state === "failed"
					? `The last drain stopped early${server.drainMessage ? `: ${server.drainMessage}` : "."} Retry it, or undrain the server.`
					: "Evacuate this host before maintenance or retirement. Services are redeployed on other servers one by one, and this server stops accepting new ones. Volumes are not copied.";
	const label =
		state === "draining"
			? "View drain progress"
			: state === "drained" || state === "failed"
				? "Open drain"
				: "Drain server…";
	return (
		<Card>
			<CardHeader>
				<CardTitle>Drain this server</CardTitle>
				<DrainChip state={state} message={server.drainMessage} />
			</CardHeader>
			<CardContent className="flex flex-col items-start gap-4">
				<p className="type-body max-w-[68ch] text-ink-3">{description}</p>
				<Button
					variant="secondary"
					disabled={!canStartDrain(server) && state !== "draining"}
					onClick={openDrain}
				>
					{label}
				</Button>
			</CardContent>
		</Card>
	);
}

/** Revoke: the agent's identity is rejected for good; confirmed by holding. */
function RevokeZone({ server }: { server: ServerView }) {
	const confirm = useConfirm();
	const queryClient = useQueryClient();
	const revoke = useMutation({
		mutationFn: async () => {
			await revokeServer({ path: { serverId: server.id } });
		},
		onSuccess: async () => {
			toast.success(
				`Revoked ${server.name}`,
				"The agent was disconnected. Containers on the host keep running.",
			);
			await Promise.all([
				queryClient.invalidateQueries({
					queryKey: serverViewQuery(server.id).queryKey,
				}),
				queryClient.invalidateQueries({
					queryKey: serverViewsQuery().queryKey,
				}),
			]);
		},
	});
	const revoked = server.status === "revoked";
	return (
		<Card variant="danger">
			<CardHeader>
				<CardTitle className="text-destructive-text">
					Revoke the agent
				</CardTitle>
			</CardHeader>
			<CardContent className="py-0">
				<KvRow
					label="Revoke this server"
					description={
						revoked
							? "This server's agent identity is revoked. Enrol a new server to use this host again."
							: "Disconnects the agent and rejects its identity. Deployments that were never delivered fail with “No changes were made”. Containers already running on the host are left untouched."
					}
					className="border-0"
				>
					<div className="flex flex-col items-start gap-2">
						<Button
							variant="danger"
							disabled={revoked || revoke.isPending}
							onClick={() =>
								void confirm({
									title: `Revoke ${server.name}?`,
									body: (
										<>
											The agent is disconnected and its identity can never
											connect again. Services placed here stop receiving
											deployments until you move them. Running containers are
											not removed.
										</>
									),
									cta: "Revoke agent",
									danger: true,
									onConfirm: async () => {
										await revoke.mutateAsync();
									},
								})
							}
						>
							Revoke agent
						</Button>
						{revoke.isError ? (
							<Alert variant="destructive">
								<AlertDescription>
									{errorMessage(revoke.error)}
								</AlertDescription>
							</Alert>
						) : null}
					</div>
				</KvRow>
			</CardContent>
		</Card>
	);
}

/** Remove: delete the server from NebulaCtrl, after the agent has emptied the host or without touching it. */
function RemoveZone({ server }: { server: ServerView }) {
	if (server.controlPlane) return null;
	return (
		<Card variant="danger">
			<CardHeader>
				<CardTitle className="text-destructive-text">
					Remove this server
				</CardTitle>
			</CardHeader>
			<CardContent className="flex flex-col items-start gap-4">
				<p className="type-body max-w-[68ch] text-ink-3">
					Deletes the server from NebulaCtrl. A clean removal first has the
					agent remove every container, network and volume NebulaCtrl manages on
					the host; removing without cleaning leaves the host as it is. Services
					placed here block it: move or delete them first.
				</p>
				<RemoveServerButton server={server} />
			</CardContent>
		</Card>
	);
}

/** The server's Danger zone page: what lifts its limits, empties it, cuts it off or deletes it. */
export function DangerZone({ server }: { server: ServerView }) {
	return (
		<>
			<UnrestrictedCard key={`unrestricted-${server.id}`} server={server} />
			<DrainZone server={server} />
			<RevokeZone server={server} />
			<RemoveZone server={server} />
		</>
	);
}
