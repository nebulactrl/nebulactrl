import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	addDatabaseReplicaMutation,
	getMeshOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import type { Service } from "#/api/models";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { useServers } from "#/features/canvas/data";
import { ServerPicker } from "#/features/create/create-dialog";
import { isSchedulable } from "#/features/fleet/model";
import {
	canConfirmAdd,
	defaultReplicaServer,
	hasIssue,
	isSameServerPick,
	issueMessages,
	placementLine,
	SAME_SERVER_NOTE,
	suggestReplicaName,
} from "./model";
import { invalidateReplication } from "./queries";

export interface AddReplicaDialogProps {
	/** The primary the replica follows. */
	service: Pick<Service, "id" | "name" | "slug" | "serverId">;
	/** The slugs of its replicas, so the suggested name is free. */
	taken: readonly string[];
	open: boolean;
	onOpenChange(open: boolean): void;
}

/**
 * Add a standby replica: pick the server (another one, or the primary's own), read
 * the plan the control plane computes (what it adds to the mesh, what blocks
 * it) and confirm. A replica is made by a background operation, not a staged
 * change: it copies the primary's data, then starts and streams.
 */
export function AddReplicaDialog({
	service,
	taken,
	open,
	onOpenChange,
}: AddReplicaDialogProps) {
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				<AddReplicaBody
					service={service}
					taken={taken}
					onClose={() => onOpenChange(false)}
				/>
			</DialogContent>
		</Dialog>
	);
}

function AddReplicaBody({
	service,
	taken,
	onClose,
}: {
	service: AddReplicaDialogProps["service"];
	taken: readonly string[];
	onClose(): void;
}) {
	const queryClient = useQueryClient();
	const servers = useServers();
	const nameId = useId();
	const usable = useMemo(
		() =>
			(servers.data ?? []).filter(
				(server) => server.status !== "revoked" && server.status !== "pending",
			),
		[servers.data],
	);
	const offline = useMemo(
		() =>
			new Map(
				usable
					.filter((server) => server.status !== "online")
					.map((server) => [server.id, "offline"] as const),
			),
		[usable],
	);

	const suggested = suggestReplicaName(service.slug, taken);
	const [name, setName] = useState("");
	const [picked, setPicked] = useState("");
	const [acceptMesh, setAcceptMesh] = useState(false);
	// One key per opening: a retry after a lost response replays, not duplicates.
	const [idempotencyKey] = useState(() => crypto.randomUUID());

	// Another online server that is in the mesh first (that is what a standby
	// is for), the primary's own otherwise.
	const online = usable.filter(
		(server) => server.status === "online" && isSchedulable(server),
	);
	const mesh = useQuery({ ...getMeshOptions(), retry: false });
	const meshed = useMemo(
		() =>
			new Set(
				(mesh.data?.nodes ?? [])
					.filter((node) => node.state === "online")
					.map((node) => node.serverId),
			),
		[mesh.data],
	);
	// Until the mesh answer is in, no default: the plan would run for the
	// primary's own server and then again for the better pick.
	const serverId =
		picked !== ""
			? picked
			: mesh.isPending
				? ""
				: defaultReplicaServer(online, service.serverId, meshed);

	const plan = useMutation(addDatabaseReplicaMutation());
	const create = useMutation(addDatabaseReplicaMutation());

	// A new server means a new plan.
	// biome-ignore lint/correctness/useExhaustiveDependencies: `plan.mutate` is stable; the plan reruns per server only.
	useEffect(() => {
		if (serverId === "") return;
		setAcceptMesh(false);
		plan.mutate({
			path: { serviceId: service.id },
			body: { serverId, dryRun: true },
		});
	}, [serverId, service.id]);

	const current =
		plan.data?.plan.serverId === serverId ? plan.data.plan : undefined;
	const blockers = current?.blockers ?? [];
	const warnings = current?.warnings ?? [];
	const needsMesh = hasIssue(blockers, "mesh_server_required");
	const meshDevices = current?.devicesAdded ?? 0;
	const canCreate =
		!create.isPending && canConfirmAdd(current, acceptMesh) && serverId !== "";

	const submit = () =>
		create.mutate(
			{
				path: { serviceId: service.id },
				body: {
					serverId,
					...(name.trim() === "" ? {} : { name: name.trim() }),
					acceptMeshChanges: acceptMesh,
				},
				headers: { "Idempotency-Key": idempotencyKey },
			},
			{
				onSuccess: async () => {
					await invalidateReplication(queryClient);
					toast.add({
						type: "success",
						title: `Creating ${name.trim() || suggested}`,
						description:
							"A standby replica copies the primary's data before it starts. Follow it in the Standby replica section.",
						timeout: 9000,
					});
					onClose();
				},
			},
		);

	return (
		<form
			className="contents"
			onSubmit={(event) => {
				event.preventDefault();
				if (canCreate) submit();
			}}
		>
			<DialogHeader>
				<DialogTitle>Add a standby replica · {service.name}</DialogTitle>
				<DialogDescription>
					A standby replica is a copy that follows {service.name} and can answer
					read-only queries. Writes always go to the primary. It is created now,
					in the background; promoting it is always your decision.
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				<Field>
					<FieldLabel htmlFor={nameId}>Name</FieldLabel>
					<Input
						id={nameId}
						value={name}
						placeholder={suggested}
						autoComplete="off"
						spellCheck={false}
						onChange={(event) => setName(event.target.value)}
					/>
					<FieldDescription>
						Optional. It becomes a service of its own in this environment.
					</FieldDescription>
				</Field>

				<Field>
					<FieldLabel>Server</FieldLabel>
					{usable.length === 0 ? (
						<Alert variant="warning">
							<AlertDescription>
								No server is available to run a replica.
							</AlertDescription>
						</Alert>
					) : (
						<ServerPicker
							servers={usable}
							value={serverId}
							onChange={(id) => setPicked(id)}
							disabledReasons={offline}
						/>
					)}
				</Field>

				{serverId !== "" && current === undefined && !plan.isError ? (
					<div
						className="flex flex-col gap-2"
						role="status"
						aria-label="Planning the replica"
					>
						<Skeleton className="h-5 w-2/3" />
						<Skeleton className="h-10 w-full" />
					</div>
				) : null}
				{plan.isError ? (
					<Alert variant="destructive">
						<AlertTitle>The replica could not be planned</AlertTitle>
						<AlertDescription>{errorMessage(plan.error)}</AlertDescription>
					</Alert>
				) : null}

				{isSameServerPick(current, serverId, service.serverId) ? (
					<Alert variant="info">
						<AlertDescription>{SAME_SERVER_NOTE}</AlertDescription>
					</Alert>
				) : null}
				{current ? (
					<>
						<p className="text-[13px] text-ink-2">{placementLine(current)}</p>
						{(current.links ?? []).length > 0 ? (
							<ul
								aria-label="Mesh links"
								className="flex list-disc flex-col gap-1 pl-5 font-mono text-2xs text-ink-2"
							>
								{(current.links ?? []).map((link) => (
									<li key={`${link.from}-${link.to}-${link.purpose}`}>
										{link.from} → {link.to} ({link.purpose})
									</li>
								))}
							</ul>
						) : null}
						{warnings.length > 0 ? (
							<Alert variant="warning">
								<AlertTitle>Before you add it</AlertTitle>
								<AlertDescription>
									<ul className="flex list-disc flex-col gap-1 pl-4">
										{issueMessages(warnings).map((message) => (
											<li key={message}>{message}</li>
										))}
									</ul>
								</AlertDescription>
							</Alert>
						) : null}
						{blockers.length > 0 ? (
							<Alert variant="destructive">
								<AlertTitle>It cannot be added right now</AlertTitle>
								<AlertDescription className="flex flex-col gap-2">
									<ul className="flex list-disc flex-col gap-1 pl-4">
										{issueMessages(blockers).map((message) => (
											<li key={message}>{message}</li>
										))}
									</ul>
									{needsMesh ? (
										<span>
											<Link
												to="/servers/$serverId/settings/$section"
												params={{ serverId, section: "mesh" }}
												className="text-accent-ink underline"
												onClick={onClose}
											>
												Enable the mesh on {current.serverName}
											</Link>
										</span>
									) : null}
								</AlertDescription>
							</Alert>
						) : null}
						{meshDevices > 0 && blockers.length === 0 ? (
							<CheckLabel
								checked={acceptMesh}
								onCheckedChange={setAcceptMesh}
								className="items-start"
								checkboxClassName="mt-0.5"
							>
								<span className="text-[13px] text-ink-1">
									Connect over the mesh: adds {meshDevices}{" "}
									{meshDevices === 1 ? "device" : "devices"}
								</span>
								<span className="block text-2xs text-ink-3">
									Each link is a tagged device on your tailnet; some plans
									charge for them.
								</span>
							</CheckLabel>
						) : null}
					</>
				) : null}

				{create.isError ? (
					<Alert variant="destructive">
						<AlertTitle>The replica was refused</AlertTitle>
						<AlertDescription>{errorMessage(create.error)}</AlertDescription>
					</Alert>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="ghost" onClick={onClose}>
					Cancel
				</Button>
				<Button type="submit" disabled={!canCreate}>
					{create.isPending ? <Spinner /> : null}
					Add standby replica
				</Button>
			</DialogFooter>
		</form>
	);
}
