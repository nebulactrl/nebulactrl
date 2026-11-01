import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { errorCode, errorMessage } from "#/api/client-config";
import {
	getMeshOptions,
	setServerMeshMutation,
} from "#/api/gen/@tanstack/react-query.gen";
import { setServerMesh } from "#/api/gen/sdk.gen";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { FieldError } from "#/components/ui/field";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { enableAclEffect, meshErrorText, plural } from "./model";
import { MeshBlockers } from "./plan-block";
import { invalidateMesh } from "./queries";

/**
 * The plan of enabling mesh on a server, before anything happens: the devices
 * it adds, whether the host can run a link (`/dev/net/tun`), what the access
 * policy does about it, and anything that stops it. Confirming enables it.
 */
export function EnableMeshDialog({
	serverId,
	serverName,
	open,
	onOpenChange,
}: {
	serverId: string;
	serverName: string;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const plan = useQuery({
		queryKey: [{ _id: "planEnableMesh", path: { serverId } }],
		queryFn: async ({ signal }) => {
			const { data } = await setServerMesh({
				path: { serverId },
				query: { dryRun: true },
				body: { enabled: true },
				signal,
			});
			return data.plan;
		},
		enabled: open,
		staleTime: 0,
		gcTime: 0,
		retry: false,
		refetchInterval: false,
	});
	const mesh = useQuery({ ...getMeshOptions(), enabled: open });
	const enable = useMutation({
		...setServerMeshMutation(),
		onSuccess: async () => {
			await invalidateMesh(queryClient);
			toast.success(
				`${serverName} is joining the mesh`,
				"Its state updates on this page.",
			);
			onOpenChange(false);
		},
	});

	const data = plan.data;
	const blockers = data?.blockers ?? [];

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				<DialogHeader>
					<DialogTitle>Enable the mesh on {serverName}?</DialogTitle>
					<DialogDescription>
						{serverName} joins your tailnet so services and database replicas on
						your other servers can reach it, and it can reach theirs.
					</DialogDescription>
				</DialogHeader>
				<DialogBody className="flex flex-col gap-3">
					{plan.isPending ? (
						<div aria-busy="true" className="flex flex-col gap-2">
							<Skeleton className="h-6 w-2/3" />
							<Skeleton className="h-6 w-full" />
						</div>
					) : plan.isError ? (
						<Alert variant="destructive">
							<AlertDescription>
								{meshErrorText(errorCode(plan.error), errorMessage(plan.error))}
							</AlertDescription>
						</Alert>
					) : data ? (
						<>
							<ul className="surface-well flex flex-col gap-1.5 rounded-card p-3 text-ink-2 text-xs leading-[1.5]">
								<li>
									<span className="font-medium text-ink-1">
										Adds {plural(data.devicesAdded, "device")}
									</span>{" "}
									to your tailnet, tagged. A link added later for a service or a
									replica adds one more each.
								</li>
								{data.tunDevice ? (
									<li>
										<code className="font-mono">/dev/net/tun</code> is present:
										this server can also host links to services on other
										servers.
									</li>
								) : null}
								{mesh.data ? (
									<li>{enableAclEffect(mesh.data.aclMode)}</li>
								) : null}
							</ul>
							{(data.warnings ?? []).map((warning) => (
								<Alert key={warning} variant="warning">
									<AlertDescription>{warning}</AlertDescription>
								</Alert>
							))}
							<MeshBlockers blockers={blockers} />
						</>
					) : null}
					{enable.isError ? (
						<FieldError>
							{meshErrorText(
								errorCode(enable.error),
								errorMessage(enable.error),
							)}
						</FieldError>
					) : null}
				</DialogBody>
				<DialogFooter>
					<Button
						variant="ghost"
						disabled={enable.isPending}
						onClick={() => onOpenChange(false)}
					>
						Cancel
					</Button>
					<Button
						variant="primary"
						disabled={
							plan.isPending ||
							plan.isError ||
							blockers.length > 0 ||
							enable.isPending
						}
						onClick={() =>
							enable.mutate({
								path: { serverId },
								// Confirming here is the acceptance of the devices the plan lists.
								body: { enabled: true, acceptMeshChanges: true },
								headers: { "Idempotency-Key": crypto.randomUUID() },
							})
						}
					>
						{enable.isPending ? <Spinner /> : null}
						{data && data.devicesAdded > 0
							? `Enable mesh · adds ${plural(data.devicesAdded, "device")}`
							: "Enable mesh"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
