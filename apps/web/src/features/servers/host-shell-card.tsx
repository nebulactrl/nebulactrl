import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { TerminalIcon } from "lucide-react";
import { errorMessage, errorStatus } from "#/api/client-config";
import { getServerHostShellQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { setServerHostShell } from "#/api/gen/sdk.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { DocsLink } from "#/components/docs/docs-link";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "#/components/ui/card";
import { KvRow } from "#/components/ui/kv-row";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { openHostShell, useHostShellInfo } from "#/features/terminal";
import type { ServerView } from "./model";

/**
 * The server's host shell (opt-in, off by default): the switch that allows
 * Owners and Admins to open a root shell on the machine, and the button that
 * opens one. Members who are not Owners or Admins get a 403 from the info
 * endpoint and see nothing at all.
 */
export function HostShellCard({ server }: { server: ServerView }) {
	const info = useHostShellInfo(server.id);
	const queryClient = useQueryClient();
	const confirm = useConfirm();

	const toggle = useMutation({
		mutationFn: async (enabled: boolean) => {
			await setServerHostShell({
				path: { serverId: server.id },
				body: { enabled },
			});
		},
		onSuccess: async (_data, enabled) => {
			toast.success(
				enabled ? "Host shell turned on" : "Host shell turned off",
				enabled
					? "Owners and Admins with two-factor authentication can open it from this page."
					: "New sessions are refused and open ones were ended.",
			);
			await queryClient.invalidateQueries({
				queryKey: getServerHostShellQueryKey({ path: { serverId: server.id } }),
			});
		},
	});

	if (errorStatus(info.error) === 403) return null;
	const data = info.data;

	const change = async (enabled: boolean) => {
		if (enabled) {
			const ok = await confirm({
				title: `Turn on the host shell for ${server.name}?`,
				body: (
					<>
						Owners and Admins who have two-factor authentication will be able to
						open a root shell on this machine from the browser. It sits outside
						every container, so it can reach all of this server's services,
						their volumes and the Docker daemon. Each session is audited; what
						is typed is not recorded.
					</>
				),
				cta: "Turn on",
				danger: true,
				hold: false,
			});
			if (!ok) return;
		}
		toggle.mutate(enabled);
	};

	return (
		<SkeletonReveal
			loading={info.isPending}
			skeleton={<Skeleton className="h-40 rounded-card" role="status" />}
		>
			{() =>
				data ? (
					<Card>
						<CardHeader>
							<CardTitle className="flex items-center gap-3">
								Host shell
								<DocsLink to="security/host-shell" />
							</CardTitle>
						</CardHeader>
						<CardContent className="py-1">
							{data.controlPlane ? (
								<Alert variant="warning" className="my-3">
									<AlertDescription>
										This server runs NebulaCtrl itself, so its host shell is
										root on the machine that holds every team's data and
										secrets. Only the instance admin can turn it on or open it.
									</AlertDescription>
								</Alert>
							) : null}
							<KvRow
								label="Allow a shell on this server"
								description={
									data.restricted
										? "Only the instance admin can change this on the server NebulaCtrl runs on."
										: "Off by default. A root shell on the machine itself, for Owners and Admins with two-factor authentication."
								}
								htmlFor="srv-host-shell"
							>
								<Switch
									id="srv-host-shell"
									checked={data.enabled}
									disabled={toggle.isPending || data.restricted}
									onCheckedChange={(next) => void change(next)}
								/>
								<span className="text-ink-3 text-xs">
									{data.enabled ? "On" : "Off"}
								</span>
							</KvRow>
							<KvRow
								label="Open a shell"
								description={
									data.available
										? "Sessions are audited (who, which server, how long); what you type is never recorded."
										: (data.reason ?? "Not available right now.")
								}
								className="border-b-0"
							>
								<div className="flex flex-col items-start gap-2">
									<Button
										variant="secondary"
										disabled={!data.available}
										onClick={() => openHostShell(server.id)}
									>
										<TerminalIcon aria-hidden />
										Open host shell
									</Button>
									{data.enabled && !data.twoFactorEnabled ? (
										<Link
											to="/account/$section"
											params={{ section: "security" }}
											className="text-accent-ink text-xs underline"
										>
											Set up two-factor authentication
										</Link>
									) : null}
									{toggle.isError ? (
										<Alert variant="destructive">
											<AlertDescription>
												{errorMessage(toggle.error)}
											</AlertDescription>
										</Alert>
									) : null}
								</div>
							</KvRow>
						</CardContent>
					</Card>
				) : (
					<Alert variant="warning">
						<AlertDescription>
							The host shell setting did not load: {errorMessage(info.error)}
						</AlertDescription>
					</Alert>
				)
			}
		</SkeletonReveal>
	);
}
