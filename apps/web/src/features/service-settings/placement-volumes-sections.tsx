import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PlusIcon, Trash2Icon } from "lucide-react";
import { type FormEvent, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createVolume, deleteVolume } from "#/api/gen/sdk.gen";
import { serversQuery, volumesQuery } from "#/api/hooks";
import { invalidateServices } from "#/api/invalidate";
import type { Server, Service } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { unschedulableReason } from "#/features/fleet/model";
import { FieldMark } from "#/features/project-config";
import { RetiredVolumes } from "#/features/volume-backups/retired-volumes";
import { unrestrictedFieldLabel, unrestrictedFields } from "./docker-options";
import { useDockerPolicy } from "./docker-policy";
import {
	type DraftProps,
	Section,
	SectionBody,
	type SelectOption,
	SelectRow,
} from "./fields";

export function headroom(server: Server): string {
	if (!server.latest)
		return server.status === "online" ? "no sample yet" : server.status;
	const { cpuPercent, memUsed, memTotal, diskUsed, diskTotal } = server.latest;
	const pct = (used: number, total: number) =>
		total > 0 ? Math.round((used / total) * 100) : 0;
	return `CPU ${Math.round(cpuPercent)}% · mem ${pct(memUsed, memTotal)}% · disk ${pct(diskUsed, diskTotal)}%`;
}

/**
 * Server options for a select: name, status and what is left on the host.
 * `needsUnrestricted` marks servers that are not unrestricted as unfit for a
 * service whose Docker options need that tier; the server the service is
 * placed on now (`current`) stays choosable so the form can show where it is.
 */
export function serverOptions(
	servers: readonly Server[],
	needsUnrestricted = false,
	current = "",
): SelectOption[] {
	return servers
		.filter(
			(server) => server.status !== "revoked" && server.status !== "pending",
		)
		.map((server) => {
			// A draining or drained server, or one that does not accept workloads,
			// is shown but cannot be chosen.
			const closed =
				server.drainState === "draining" ||
				server.drainState === "drained" ||
				!server.acceptWorkloads
					? unschedulableReason(server)
					: null;
			const restricted =
				needsUnrestricted && !server.unrestricted && server.id !== current;
			return {
				value: server.id,
				text: restricted
					? `${server.name} (needs an unrestricted server)`
					: closed
						? `${server.name} (${closed})`
						: `${server.name} (${server.status})`,
				disabled: closed !== null || restricted,
				label: (
					<span className="flex min-w-0 flex-1 items-center gap-3">
						<span className="font-medium">{server.name}</span>
						<span
							className={`text-[11px] ${server.status === "online" && !closed && !restricted ? "text-ink-3" : "text-warning-ink"}`}
						>
							{restricted
								? "needs an unrestricted server"
								: closed
									? `${closed}: not accepting services`
									: server.status === "online"
										? headroom(server)
										: server.status}
						</span>
					</span>
				),
			};
		});
}

/**
 * Which server runs the service. Changing it is saved like any setting but
 * only takes effect through a deployment, which starts a new container on the
 * target; nothing is migrated.
 */
export function PlacementSection({
	service,
	draft,
	errors,
	set,
	serverError,
}: DraftProps & { service: Service; serverError?: string | undefined }) {
	const { policy } = useDockerPolicy();
	const needing = unrestrictedFields(draft.docker, policy);
	const servers = useQuery(serversQuery());
	const list = servers.data ?? [];
	const target = list.find((server) => server.id === draft.serverId);
	const moving =
		service.serverId !== null &&
		draft.serverId !== "" &&
		draft.serverId !== service.serverId;
	return (
		<Section
			title="Server"
			description="Which server runs this service. A change takes effect on the next deploy."
		>
			<SelectRow
				id="set-server"
				label="Server"
				value={draft.serverId}
				options={serverOptions(
					list,
					needing.length > 0,
					service.serverId ?? "",
				)}
				error={errors.serverId}
				placeholder={servers.isPending ? "Loading servers…" : "Choose a server"}
				onChange={(value) => set("serverId", value)}
			/>
			{moving && target ? (
				<SectionBody>
					<Alert variant="warning">
						<AlertTitle>Move to server {target.name}</AlertTitle>
						<AlertDescription>
							Creates a new deployment on {target.name}; volumes are not
							migrated. Nothing moves until you deploy, and the current
							container keeps serving from{" "}
							{service.server?.name ?? "its server"} until the new one is
							healthy.
						</AlertDescription>
					</Alert>
				</SectionBody>
			) : null}
			{needing.length > 0 ? (
				<SectionBody>
					<Alert variant="warning">
						<AlertDescription>
							The Docker options of this service (
							{needing.map(unrestrictedFieldLabel).join(", ")}) need a server an
							instance admin made Unrestricted; other servers are not offered.
						</AlertDescription>
					</Alert>
				</SectionBody>
			) : null}
			{serverError ? (
				<SectionBody>
					<Alert variant="destructive">
						<AlertDescription>{serverError}</AlertDescription>
					</Alert>
				</SectionBody>
			) : null}
		</Section>
	);
}

export function VolumesSection({ service }: { service: Service }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const volumes = useQuery(volumesQuery(service.id));
	const [name, setName] = useState("");
	const [mountPath, setMountPath] = useState("");

	const add = useMutation({
		mutationFn: async () => {
			await createVolume({
				path: { serviceId: service.id },
				body: { name: name.trim(), mountPath: mountPath.trim() },
			});
		},
		onSuccess: async () => {
			setName("");
			setMountPath("");
			await invalidateServices(queryClient);
			toast.success("Volume added", "Staged until the next deploy.");
		},
	});
	const remove = useMutation({
		mutationFn: async (volumeId: string) => {
			await deleteVolume({ path: { volumeId } });
		},
		onSuccess: () => invalidateServices(queryClient),
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (name.trim() === "" || mountPath.trim() === "" || add.isPending) return;
		add.mutate();
	};

	const items = volumes.data ?? [];
	return (
		<Section
			title="Volumes"
			description="Data that survives deploys. A service with volumes stops the old container first, so there is a short gap."
		>
			<SectionBody>
				{items.length === 0 ? (
					<p className="type-secondary mb-3">
						No volumes. Data inside the container is lost on every deploy.
					</p>
				) : (
					<ul className="mb-3" aria-label="Volumes">
						{items.map((volume) => (
							<li
								key={volume.id}
								className="flex items-center gap-3 border-line border-b py-2 first:pt-0 last:border-0"
							>
								<div className="min-w-0 flex-1">
									<span className="flex min-w-0 items-center gap-1.5">
										<span className="font-mono text-[13px] text-ink-1">
											{volume.mountPath}
										</span>
										<FieldMark field={`volumes.${volume.name}`} />
									</span>
									<span
										className="block truncate font-mono text-[11px] text-ink-4"
										title={volume.dockerName}
									>
										{volume.name} · {volume.dockerName}
									</span>
									{volume.restoreLabel ? (
										<span className="block text-[11px] text-status-staged-on">
											{volume.restoreLabel}
										</span>
									) : null}
								</div>
								<Button
									variant="ghost"
									size="icon-sm"
									aria-label={`Remove volume ${volume.name}`}
									onClick={() =>
										void confirm({
											title: `Remove volume ${volume.name}?`,
											body: "The mount is removed on the next deploy. The Docker volume and its data stay on the server until you delete the service with “delete volumes”.",
											cta: "Remove volume",
											glyph: "trash",
											danger: true,
											hold: false,
											onConfirm: () => remove.mutateAsync(volume.id),
										})
									}
								>
									<Trash2Icon aria-hidden />
								</Button>
							</li>
						))}
					</ul>
				)}
				<form
					onSubmit={submit}
					className="grid grid-cols-1 items-center gap-2 @md/section:grid-cols-[1fr_1.4fr_auto]"
				>
					<Input
						aria-label="Volume name"
						autoComplete="off"
						className="font-mono"
						placeholder="data"
						value={name}
						onChange={(event) => setName(event.target.value)}
					/>
					<Input
						aria-label="Mount path"
						autoComplete="off"
						className="font-mono"
						placeholder="/var/lib/data"
						value={mountPath}
						onChange={(event) => setMountPath(event.target.value)}
					/>
					<Button
						type="submit"
						size="md"
						variant="secondary"
						disabled={
							add.isPending || name.trim() === "" || mountPath.trim() === ""
						}
					>
						{add.isPending ? <Spinner /> : <PlusIcon aria-hidden />}
						Add
					</Button>
				</form>
				{add.isError ? (
					<FieldError className="mt-2">{errorMessage(add.error)}</FieldError>
				) : null}
				<RetiredVolumes serviceId={service.id} />
			</SectionBody>
		</Section>
	);
}
