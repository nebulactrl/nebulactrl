import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { listServersQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { removeServer } from "#/api/gen/sdk.gen";
import { CodeBlock } from "#/components/common/code-block";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Button } from "#/components/ui/button";
import { toast } from "#/components/ui/toast";
import type { ServerView } from "./model";
import { useServicesOnServer } from "./use-services-on-server";

type Mode = "clean" | "forget";

/**
 * Local backups are never deleted. The agent's default backup directory lives
 * in its state directory and is moved to /var/lib/nebulactrl-backups when the
 * agent removes itself; a configured directory elsewhere stays where it is.
 */
function backupPath(_server: ServerView): string {
	return "/var/lib/nebulactrl-backups (or the backup directory you configured, when it is outside /var/lib/nebulactrl-agent)";
}

/** What the person runs on the host by hand after a server was forgotten. */
export function manualCleanupCommand(origin: string): string {
	return [
		`docker ps -aq --filter label=nebulactrl.managed | xargs -r docker rm -f`,
		`docker rm -f nebulactrl-traefik nebulactrl-pages nebulactrl-cloudflared nebulactrl-tailscale`,
		`curl -fsSL ${origin}/install-agent.sh | sudo sh -s -- --uninstall --purge`,
	].join("\n");
}

/** The services still placed on the server, named: the removal refuses while there are any. */
function PlacedNote({ serverId }: { serverId: string }) {
	const { placed } = useServicesOnServer(serverId);
	if (placed.length === 0) return null;
	const shown = placed.slice(0, 5);
	const rest = placed.length - shown.length;
	return (
		<p>
			{placed.length === 1
				? "1 service is still placed here: "
				: `${placed.length} services are still placed here: `}
			<b className="text-ink-1">
				{shown
					.map(({ service, project }) => `${service.name} (${project.name})`)
					.join(", ")}
				{rest > 0 ? ` and ${rest} more` : ""}
			</b>
			. Move or delete {placed.length === 1 ? "it" : "them"} first; the server
			is not removed while it hosts services.
		</p>
	);
}

/** The commands that empty the host by hand, wrapped, with a key to copy them. */
function CleanupCommands() {
	const commands = manualCleanupCommand(window.location.origin);
	return (
		<CodeBlock copyValue={commands} className="break-words">
			{commands}
		</CodeBlock>
	);
}

/**
 * What removing a server does, as the dialog and the card menu share it:
 * `ask("clean")` has the agent empty the host and remove itself, `ask("forget")`
 * deletes the server and leaves the host as it is (with the commands to clean
 * it by hand). Both are called "Remove" throughout, and both ask for the
 * server's name and a hold. A server that never connected has nothing on a
 * host to clean, so it gets neither the cleanup text nor the commands.
 */
export function useRemoveServer(server: ServerView) {
	const confirm = useConfirm();
	const queryClient = useQueryClient();
	const router = useRouter();
	const removing = server.removal?.state === "removing";
	const neverConnected = server.status === "pending";
	// An offline server (or an agent too old to clean) can only be forgotten.
	const canClean = server.status === "online" && server.canDecommission;

	const remove = useMutation({
		mutationFn: (mode: Mode) =>
			removeServer({
				path: { serverId: server.id },
				query: { confirm: server.name, mode },
			}),
		onSuccess: async (res) => {
			await queryClient.invalidateQueries({ queryKey: listServersQueryKey() });
			if (res.data?.removed) {
				toast.success(`Removed ${server.name}`);
				await router.navigate({ to: "/servers" });
			} else {
				toast.success(
					`Removing ${server.name}: the agent is emptying the host`,
				);
			}
		},
	});

	const body = (mode: Mode) => {
		if (neverConnected) {
			return (
				<div className="flex flex-col gap-2">
					<p>
						{server.name} never connected, so there is nothing on a host to
						clean up. Its install commands stop working.
					</p>
					<PlacedNote serverId={server.id} />
				</div>
			);
		}
		if (mode === "clean") {
			return (
				<div className="flex flex-col gap-2">
					<p>
						The agent stops and removes every container, network and volume
						NebulaCtrl manages on this host, the proxy included, and then
						removes itself: its identity, service unit and files. Docker and
						everything that is not NebulaCtrl's stay. The data of the volumes is
						deleted.
					</p>
					<p>Backups stored on this server stay at {backupPath(server)}.</p>
					<PlacedNote serverId={server.id} />
					<p>This cannot be undone.</p>
				</div>
			);
		}
		return (
			<div className="flex flex-col gap-2">
				<p>
					The server and its agent identity are deleted, but the{" "}
					<b>host keeps its containers, volumes and the agent's files</b>. Run
					this on the host to clean it up yourself:
				</p>
				<CleanupCommands />
				<p>Backups stored on this server stay at {backupPath(server)}.</p>
				<PlacedNote serverId={server.id} />
			</div>
		);
	};

	const ask = (mode: Mode) =>
		void confirm({
			title:
				mode === "forget" && canClean
					? `Remove ${server.name} without cleaning?`
					: `Remove ${server.name}?`,
			body: body(mode),
			cta: "Remove server",
			danger: true,
			typeToConfirm: server.name,
			onConfirm: async () => {
				await remove.mutateAsync(mode);
			},
		});

	return {
		/** The control-plane host cannot be removed from here. */
		hidden: server.controlPlane,
		removing,
		canClean,
		ask,
	};
}

/**
 * "Remove server…": the dialog explains the two ways (clean: the agent empties
 * the host and removes itself; without cleaning: the host is left as it is,
 * with the command to clean it by hand), asks for the server's name and a
 * hold, and names the services that block the removal.
 */
export function RemoveServerButton({
	server,
	size,
}: {
	server: ServerView;
	size?: "sm" | "md" | "lg";
}) {
	const { hidden, removing, canClean, ask } = useRemoveServer(server);
	if (hidden) return null;
	return (
		<div className="flex flex-wrap items-center gap-2">
			<Button
				variant="danger"
				size={size}
				disabled={removing}
				onClick={() => ask(canClean ? "clean" : "forget")}
			>
				{removing ? "Removing…" : "Remove server…"}
			</Button>
			{canClean && !removing ? (
				<Button variant="ghost" size={size} onClick={() => ask("forget")}>
					Remove without cleaning…
				</Button>
			) : null}
		</div>
	);
}
