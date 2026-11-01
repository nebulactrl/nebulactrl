import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CloudIcon, HardDriveIcon, PlusIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { listBackupDestinationsOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	deleteBackupDestination,
	testBackupDestination,
} from "#/api/gen/sdk.gen";
import type { DestinationView } from "#/api/gen/types.gen";
import { serversQuery } from "#/api/hooks";
import { useConfirm } from "#/components/common/confirm-dialog";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Badge } from "#/components/ui/badge";
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
import { Field, FieldLabel } from "#/components/ui/field";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { toast } from "#/components/ui/toast";
import { BuiltinStorageCard } from "#/features/instance-services/builtin-storage-card";
import { DestinationDialog } from "./destination-dialog";
import { destinationWhere } from "./model";
import { invalidateBackups } from "./queries";

function TestDialog({
	destination,
	onClose,
}: {
	destination: DestinationView | null;
	onClose: () => void;
}) {
	const queryClient = useQueryClient();
	const servers = useQuery({
		...serversQuery(),
		enabled: destination !== null,
	});
	const online = (servers.data ?? []).filter((s) => s.status === "online");
	const [chosen, setChosen] = useState("");
	const serverId = chosen || online[0]?.id || "";

	const run = useMutation({
		mutationFn: async () => {
			if (!destination) return null;
			const { data } = await testBackupDestination({
				path: { destinationId: destination.id },
				body: { serverId },
			});
			return data;
		},
		onSuccess: async (data) => {
			await invalidateBackups(queryClient);
			if (data?.lastTestOk === true) {
				toast.success(
					"Destination works",
					data.lastTestMessage ||
						"The agent wrote, read and deleted a probe object.",
				);
			} else if (data?.lastTestOk === false) {
				toast.error("Destination test failed", data.lastTestMessage);
			} else {
				toast.info(
					"Still waiting for the agent",
					"The result appears on the destination when it answers.",
				);
			}
			onClose();
		},
		onError: (e) =>
			toast.error("Could not test the destination", errorMessage(e)),
	});

	return (
		<Dialog
			open={destination !== null}
			onOpenChange={(o) => (o ? null : onClose())}
		>
			<DialogContent size="md">
				<DialogHeader>
					<DialogTitle>Test {destination?.name}</DialogTitle>
					<DialogDescription>
						The agent on the chosen server writes, reads back and deletes a
						small probe object. The agent is the one that talks to the
						destination, so choose a server that will take backups.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					<Field>
						<FieldLabel htmlFor="test-server">Server</FieldLabel>
						<Select
							value={serverId}
							items={online.map((s) => ({ value: s.id, label: s.name }))}
							onValueChange={(v) => setChosen(String(v ?? ""))}
						>
							<SelectTrigger id="test-server" className="w-full">
								<SelectValue
									placeholder={
										servers.isPending
											? "Loading servers…"
											: "No server is online"
									}
								/>
							</SelectTrigger>
							<SelectContent alignItemWithTrigger={false}>
								{online.map((s) => (
									<SelectItem key={s.id} value={s.id}>
										{s.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</Field>
				</DialogBody>
				<DialogFooter>
					<Button variant="ghost" onClick={onClose}>
						Cancel
					</Button>
					<Button
						variant="primary"
						disabled={serverId === "" || run.isPending}
						onClick={() => run.mutate()}
					>
						{run.isPending ? <Spinner /> : null}
						{run.isPending ? "Testing…" : "Run test"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

function TestState({ d }: { d: DestinationView }) {
	if (d.lastTestOk === undefined || d.lastTestOk === null) {
		return (
			<span className="flex items-center gap-2 text-ink-3 text-xs">
				<StatusIcon kind="pending" label="" /> Not tested
			</span>
		);
	}
	return (
		<span className="flex flex-col gap-0.5">
			<span className="flex items-center gap-2 text-ink-2 text-xs">
				<StatusIcon kind={d.lastTestOk ? "healthy" : "failed"} label="" />
				{d.lastTestOk ? "Works" : "Failed"}
				{d.lastTestAt ? (
					<span className="text-ink-4">
						· <Relative iso={d.lastTestAt} />
					</span>
				) : null}
			</span>
			{d.lastTestOk ? null : (
				<span
					className="max-w-[320px] truncate text-destructive-text text-xs"
					title={d.lastTestMessage}
				>
					{d.lastTestMessage}
				</span>
			)}
		</span>
	);
}

/** Team settings → Backup storage: where database backups are written. */
export function DestinationsSection() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const destinations = useQuery(listBackupDestinationsOptions());
	const [editing, setEditing] = useState<DestinationView | null>(null);
	const [creating, setCreating] = useState(false);
	const [testing, setTesting] = useState<DestinationView | null>(null);

	const remove = useMutation({
		mutationFn: async (d: DestinationView) => {
			await deleteBackupDestination({ path: { destinationId: d.id } });
			return d;
		},
		onSuccess: async (d) => {
			toast.success(
				`Removed ${d.name}`,
				"Objects already stored there are not touched.",
			);
			await invalidateBackups(queryClient);
		},
		onError: (e) =>
			toast.error("Could not remove the destination", errorMessage(e)),
	});

	if (destinations.isPending) return <Skeleton className="h-40 rounded-card" />;
	if (destinations.isError && !destinations.data) {
		return (
			<ErrorState
				frame="raised"
				title="Destinations did not load"
				body="The control plane did not answer, so nothing was changed."
				retry={() => void destinations.refetch()}
			/>
		);
	}
	const all = destinations.data?.items ?? [];
	// The built-in storage is the instance's: its own read-only card, not a row to edit.
	const builtin = all.find((d) => d.kind === "builtin");
	const items = all.filter((d) => d.kind !== "builtin");
	const add = (
		<Button variant="primary" onClick={() => setCreating(true)}>
			<PlusIcon />
			Add destination
		</Button>
	);

	return (
		<div className="flex flex-col gap-4">
			<BuiltinStorageCard
				actions={
					builtin ? (
						<>
							<Button
								variant="ghost"
								size="sm"
								onClick={() => setTesting(builtin)}
							>
								Test
							</Button>
							<span className="ml-auto text-ink-3 text-xs">
								{builtin.inUseBySchedules} schedule
								{builtin.inUseBySchedules === 1 ? "" : "s"} ·{" "}
								{builtin.storedBackups} stored backup
								{builtin.storedBackups === 1 ? "" : "s"}
							</span>
						</>
					) : undefined
				}
			/>
			{items.length === 0 && builtin ? (
				<div className="flex flex-wrap items-center justify-between gap-3">
					<p className="m-0 text-ink-3 text-xs">
						Want a copy somewhere else? Add an S3-compatible bucket (AWS, R2,
						B2, Wasabi, Garage …) or a directory on a server.
					</p>
					{add}
				</div>
			) : items.length === 0 ? (
				<EmptyState
					illustration="folder"
					title="Add a place for backups"
					body="Add an S3-compatible bucket (AWS, R2, B2, Wasabi, Garage …) or a directory on a server. Backups stream straight from the database to it; schedule them from a database's Backups tab."
					action={add}
				/>
			) : (
				<>
					<div className="flex justify-end">{add}</div>
					<div className="overflow-hidden border-line border-y">
						<Table stack>
							<TableHeader>
								<TableRow>
									<TableHead>Name</TableHead>
									<TableHead>Where</TableHead>
									<TableHead>Test</TableHead>
									<TableHead>In use</TableHead>
									<TableHead className="w-px" />
								</TableRow>
							</TableHeader>
							<TableBody>
								{items.map((d) => (
									<TableRow key={d.id}>
										<TableCell pin="start">
											<span className="flex items-center gap-2">
												{d.kind === "s3" ? (
													<CloudIcon
														aria-hidden
														className="size-4 text-ink-3"
													/>
												) : (
													<HardDriveIcon
														aria-hidden
														className="size-4 text-ink-3"
													/>
												)}
												<span className="font-medium text-ink-1">{d.name}</span>
												{d.offSite ? null : (
													<Badge tone="warn" shape="capsule">
														not off-site
													</Badge>
												)}
											</span>
										</TableCell>
										<TableCell
											label="Where"
											className="font-mono text-ink-3 text-xs"
										>
											{destinationWhere(d)}
										</TableCell>
										<TableCell label="Test">
											<TestState d={d} />
										</TableCell>
										<TableCell label="In use" className="text-ink-3 text-xs">
											{d.inUseBySchedules} schedule
											{d.inUseBySchedules === 1 ? "" : "s"} · {d.storedBackups}{" "}
											stored backup
											{d.storedBackups === 1 ? "" : "s"}
										</TableCell>
										<TableCell pin="end">
											<span className="flex justify-end gap-1">
												<Button
													variant="ghost"
													size="sm"
													onClick={() => setTesting(d)}
												>
													Test
												</Button>
												<Button
													variant="ghost"
													size="sm"
													onClick={() => setEditing(d)}
												>
													Edit
												</Button>
												<Button
													variant="ghost"
													size="sm"
													className="text-destructive-text"
													onClick={() =>
														void confirm({
															title: `Remove ${d.name}?`,
															body:
																d.inUseBySchedules + d.storedBackups > 0
																	? "A destination that schedules or stored backups use can not be removed. Delete those first."
																	: "Nothing is deleted from the bucket or directory.",
															cta: "Remove destination",
															danger: true,
															hold: false,
															onConfirm: async () => {
																await remove.mutateAsync(d);
															},
														})
													}
												>
													Remove
												</Button>
											</span>
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					</div>
				</>
			)}
			<DestinationDialog
				open={creating || editing !== null}
				onOpenChange={(o) => {
					if (!o) {
						setCreating(false);
						setEditing(null);
					}
				}}
				destination={editing}
			/>
			<TestDialog destination={testing} onClose={() => setTesting(null)} />
		</div>
	);
}
