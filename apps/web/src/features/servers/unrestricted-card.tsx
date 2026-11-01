import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { errorMessage, errorStatus } from "#/api/client-config";
import { getServerUnrestrictedQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { setServerUnrestricted } from "#/api/gen/sdk.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "#/components/ui/card";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { KvRow } from "#/components/ui/kv-row";
import { Skeleton } from "#/components/ui/skeleton";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { serverUnrestrictedQuery } from "#/features/stacks/queries";
import type { ServerView } from "./model";

/** What stays blocked on a server until an instance admin turns Unrestricted on. */
const ALLOW_LIST = [
	"Privileged mode and the host PID namespace",
	"Host devices, GPUs and a custom OCI runtime",
	"Unconfined seccomp and AppArmor profiles",
	"The NET_ADMIN and SYS_ADMIN capabilities",
	"Host network and host IPC (Compose stacks)",
	"The Docker socket and bind mounts of host paths (Compose stacks)",
] as const;

/** The warning the enabling dialog shows, word for word. */
const UNRESTRICTED_WARNING =
	"Stacks and services on this server can use privileged mode, host networking, devices, GPUs, a custom runtime, the host PID namespace, unconfined seccomp or AppArmor, NET_ADMIN, SYS_ADMIN, the Docker socket and host bind mounts. Anyone who can deploy here can take over this server. Only an instance admin can change this; it is recorded in the audit log.";

const plural = (count: number, one: string, many: string): string =>
	`${count} ${count === 1 ? one : many}`;

/**
 * The server's Unrestricted switch: the one switch that lets Compose stacks
 * and services with host-access Docker options run here. Turning it on opens
 * a warning and asks for the server's name. Only an instance admin may change
 * it; for anyone else the control plane answers 403 and the switch says so.
 * The Docker Compose plugin fact lives in the Overview's Host details.
 */
export function UnrestrictedCard({ server }: { server: ServerView }) {
	const info = useQuery(serverUnrestrictedQuery(server.id));
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const [allowOpen, setAllowOpen] = useState(false);
	const listId = useId();

	const toggle = useMutation({
		mutationFn: async (unrestricted: boolean) => {
			await setServerUnrestricted({
				path: { serverId: server.id },
				body: unrestricted
					? { unrestricted, confirm: server.name }
					: { unrestricted },
			});
		},
		onSuccess: async (_data, unrestricted) => {
			toast.success(
				unrestricted ? "Unrestricted turned on" : "Unrestricted turned off",
				unrestricted
					? `Stacks and services on ${server.name} can now use the settings the allow-list blocks.`
					: "Stacks and services that need it fail their next deploy, rollbacks included.",
			);
			await queryClient.invalidateQueries({
				queryKey: getServerUnrestrictedQueryKey({
					path: { serverId: server.id },
				}),
			});
		},
	});

	if (info.isPending) {
		return <Skeleton className="h-40 rounded-card" role="status" />;
	}
	if (!info.data) {
		return (
			<Alert variant="warning">
				<AlertDescription>
					The Unrestricted settings did not load: {errorMessage(info.error)}
				</AlertDescription>
			</Alert>
		);
	}
	const data = info.data;
	const forbidden = errorStatus(toggle.error) === 403;

	const change = async (unrestricted: boolean) => {
		if (unrestricted) {
			const ok = await confirm({
				title: `Turn on Unrestricted for ${server.name}?`,
				body: (
					<>
						{UNRESTRICTED_WARNING} Live on this server today:{" "}
						{plural(data.stacks, "Compose stack", "Compose stacks")} and{" "}
						{plural(data.dockerServices, "service", "services")} whose Docker
						options need it.
					</>
				),
				cta: "Turn on Unrestricted",
				danger: true,
				hold: false,
				typeToConfirm: server.name,
			});
			if (!ok) return;
		}
		toggle.mutate(unrestricted);
	};

	return (
		<Card>
			<CardHeader>
				<CardTitle>Unrestricted</CardTitle>
			</CardHeader>
			<CardContent className="py-1">
				<KvRow
					label="Unrestricted"
					description={`Off by default: Compose stacks and services follow the safe allow-list. On, they may use privileged mode, devices, GPUs, a runtime and the other host-access options. Live here: ${plural(data.stacks, "Compose stack", "Compose stacks")} and ${plural(data.dockerServices, "service", "services")} whose Docker options need it.`}
					htmlFor="srv-unrestricted"
					className="border-b-0"
				>
					<div className="flex flex-col items-start gap-2">
						<div className="flex items-center gap-3">
							<Switch
								id="srv-unrestricted"
								checked={data.unrestricted}
								disabled={toggle.isPending}
								onCheckedChange={(next) => void change(next)}
							/>
							<span className="text-ink-3 text-xs">
								{data.unrestricted ? "On" : "Off"}
							</span>
						</div>
						{forbidden ? (
							<Alert variant="warning">
								<AlertDescription>
									Only an instance admin can change this setting.
								</AlertDescription>
							</Alert>
						) : toggle.isError ? (
							<Alert variant="destructive">
								<AlertDescription>
									{errorMessage(toggle.error)}
								</AlertDescription>
							</Alert>
						) : null}
						<button
							type="button"
							aria-expanded={allowOpen}
							aria-controls={listId}
							onClick={() => setAllowOpen(!allowOpen)}
							className="flex w-fit cursor-pointer items-center gap-1.5 rounded-sm text-[12px] text-ink-2 outline-none hover:text-ink-1 focus-visible:focus-ring"
						>
							<DisclosureChevron open={allowOpen} className="size-3.5" />
							What the allow-list blocks
						</button>
						<DisclosurePanel open={allowOpen} id={listId}>
							<ul className="m-0 flex list-disc flex-col gap-1 pl-5 text-[12px] text-ink-3 leading-[1.5]">
								{ALLOW_LIST.map((item) => (
									<li key={item}>{item}</li>
								))}
							</ul>
						</DisclosurePanel>
					</div>
				</KvRow>
			</CardContent>
		</Card>
	);
}
