import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	getDumpRestoreOptions,
	listBackupDestinationsOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { cancelDumpRestore, createDumpRestore } from "#/api/gen/sdk.gen";
import { serversQuery } from "#/api/hooks";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
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
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { StatusPill } from "#/components/ui/lamp";
import { Progress } from "#/components/ui/progress";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { formatBytes } from "#/lib/format";
import { DestinationSelect } from "./dialogs";
import { checkDumpFile, describeDump, uploadPercent } from "./dump";
import { type DumpUpload, uploadDump } from "./dump-upload";
import { restorePresentation } from "./model";
import { invalidateBackups } from "./queries";

type Mode = "new" | "in_place";
type Transfer = "idle" | "sending" | "sent" | "failed";

const FINISHED = ["succeeded", "failed"];

/**
 * Restore from a dump file: pick the file, create the restore (a new database
 * or a safety backup first), then send the file once the target is ready. The
 * control plane relays it to the agent as it arrives; it is never stored.
 */
export function DumpRestoreDialog({
	open,
	onClose,
	serviceId,
	serviceSlug,
	engine,
	serverId,
	scheduleDestinationId,
}: {
	open: boolean;
	onClose: () => void;
	serviceId: string;
	serviceSlug: string;
	engine: string;
	serverId: string | null;
	/** The destination of the database's backup schedule, preselected for the safety backup. */
	scheduleDestinationId: string | undefined;
}) {
	const queryClient = useQueryClient();
	const servers = useQuery({ ...serversQuery(), enabled: open });
	const destinations = useQuery({
		...listBackupDestinationsOptions(),
		enabled: open,
	});
	const [file, setFile] = useState<File | null>(null);
	const [fileProblem, setFileProblem] = useState<string | undefined>();
	const [fileKind, setFileKind] = useState<string | undefined>();
	const [mode, setMode] = useState<Mode>("new");
	const [name, setName] = useState("");
	const [server, setServer] = useState("");
	const [confirm, setConfirm] = useState("");
	const [destination, setDestination] = useState("");
	const [error, setError] = useState<string | null>(null);
	const [restoreId, setRestoreId] = useState<string | null>(null);
	const [transfer, setTransfer] = useState<Transfer>("idle");
	const [sent, setSent] = useState(0);
	const [transferError, setTransferError] = useState<string | null>(null);
	const upload = useRef<DumpUpload | null>(null);
	const startedFor = useRef<string | null>(null);

	useEffect(() => {
		if (!open) return;
		setFile(null);
		setFileProblem(undefined);
		setFileKind(undefined);
		setMode("new");
		setName("");
		setServer("");
		setConfirm("");
		setDestination("");
		setError(null);
		setRestoreId(null);
		setTransfer("idle");
		setSent(0);
		setTransferError(null);
		startedFor.current = null;
	}, [open]);

	const restore = useQuery({
		...getDumpRestoreOptions({ path: { restoreId: restoreId ?? "" } }),
		enabled: restoreId !== null,
		refetchInterval: (q) =>
			q.state.data && FINISHED.includes(q.state.data.status) ? false : 1500,
	});
	const view = restore.data;
	const status = view?.status ?? "provisioning";
	const finished = FINISHED.includes(status);

	const online = (servers.data ?? []).filter((s) => s.status === "online");
	const targetServer = server || serverId || "";
	const destinationItems = destinations.data?.items ?? [];
	const safetyDestination =
		destination || scheduleDestinationId || destinationItems[0]?.id || "";

	const pick = async (f: File | null) => {
		setFile(f);
		setFileProblem(undefined);
		setFileKind(undefined);
		if (!f) return;
		const checked = await checkDumpFile(f, engine);
		setFileProblem(checked.problem);
		setFileKind(checked.kind ? describeDump(checked.kind) : undefined);
	};

	const create = useMutation({
		mutationFn: () => {
			if (!file) throw new Error("no file");
			return createDumpRestore({
				path: { serviceId },
				body:
					mode === "new"
						? {
								mode: "new",
								fileName: file.name,
								sizeBytes: file.size,
								...(name.trim() ? { name: name.trim() } : {}),
								...(targetServer ? { serverId: targetServer } : {}),
							}
						: {
								mode: "in_place",
								fileName: file.name,
								sizeBytes: file.size,
								confirmSlug: confirm,
								destinationId: safetyDestination,
							},
			});
		},
		onSuccess: async (res) => {
			setRestoreId(res.data.id);
			await invalidateBackups(queryClient);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const cancel = useMutation({
		mutationFn: () => {
			if (!restoreId) throw new Error("no restore");
			return cancelDumpRestore({ path: { restoreId } });
		},
		onSuccess: async () => {
			toast.success("Restore canceled", "The file was not used.");
			await invalidateBackups(queryClient);
			onClose();
		},
		onError: (e) => toast.error("Could not cancel", errorMessage(e)),
	});

	const send = (id: string, f: File) => {
		setTransfer("sending");
		setTransferError(null);
		setSent(0);
		const u = uploadDump(id, f, (n) => setSent(n));
		upload.current = u;
		u.done
			.then(() => setTransfer("sent"))
			.catch((e: unknown) => {
				setTransfer("failed");
				setTransferError(errorMessage(e));
			})
			.finally(() => {
				upload.current = null;
				void invalidateBackups(queryClient);
			});
	};

	// The target is ready: send the file, once per restore.
	useEffect(() => {
		if (
			restoreId &&
			file &&
			view?.status === "awaiting_upload" &&
			transfer === "idle" &&
			startedFor.current !== restoreId
		) {
			startedFor.current = restoreId;
			send(restoreId, file);
		}
	});

	useEffect(() => {
		if (status === "succeeded") void invalidateBackups(queryClient);
	}, [status, queryClient]);

	const valid =
		file !== null &&
		fileProblem === undefined &&
		(mode === "new"
			? targetServer !== ""
			: confirm === serviceSlug && safetyDestination !== "");
	const submit = (e: FormEvent) => {
		e.preventDefault();
		if (!valid || create.isPending) return;
		setError(null);
		create.mutate();
	};

	const close = () => {
		// Closing while the file is on its way abandons the restore; the live
		// database is untouched.
		upload.current?.abort();
		onClose();
	};

	const running = restoreId !== null;
	const retryable = transfer === "failed" && status === "awaiting_upload";
	const pct = file ? uploadPercent(sent, file.size) : 0;
	const p = restorePresentation(status);

	return (
		<Dialog open={open} onOpenChange={(o) => (o ? null : close())}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>Restore from a dump file</DialogTitle>
						<DialogDescription>
							A pg_dump, mysqldump or mongodump file from this machine. The
							format is detected from the file; it streams to the server and is
							not stored.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						{running ? (
							<div className="flex flex-col gap-3">
								<div className="flex flex-wrap items-center gap-2">
									<StatusPill tone={p.tone} pulse={p.pulse}>
										{p.label}
									</StatusPill>
									<span className="text-ink-3 text-xs">
										{file?.name} · {file ? formatBytes(file.size) : ""}
									</span>
								</div>
								{status === "provisioning" ? (
									<p className="text-ink-3 text-sm">
										Creating and starting the new database. The file is sent as
										soon as it runs; keep this window open.
									</p>
								) : null}
								{status === "pre_backup" ? (
									<p className="text-ink-3 text-sm">
										Taking a safety backup of the current data. Nothing is
										replaced unless it succeeds.
									</p>
								) : null}
								{transfer === "sending" || transfer === "sent" ? (
									<Progress
										value={transfer === "sent" ? 100 : pct}
										live={transfer === "sending"}
										aria-label="Upload progress"
									/>
								) : null}
								{transfer === "sending" ? (
									<p className="font-mono text-ink-3 text-xs tabular-nums">
										{formatBytes(sent)} of {file ? formatBytes(file.size) : ""}{" "}
										({pct}%)
									</p>
								) : null}
								{transfer === "sent" && !finished ? (
									<p className="text-ink-3 text-sm">
										Uploaded. The database client is restoring the file; large
										dumps spend most of their time building indexes. You can
										close this window: the restore keeps running.
									</p>
								) : null}
								{transferError ? (
									<FieldError>{transferError}</FieldError>
								) : null}
								{view?.error && status === "awaiting_upload" ? (
									<FieldError>{view.error}</FieldError>
								) : null}
								{status === "succeeded" ? (
									<Alert variant="success">
										<AlertTitle>Restored</AlertTitle>
										<AlertDescription>
											{view?.mode === "new"
												? "The data is in the new database next to this one. Point your apps at it when you are happy, then delete the old one."
												: "This database now holds the contents of the file."}
										</AlertDescription>
									</Alert>
								) : null}
								{status === "failed" ? (
									<Alert variant="destructive">
										<AlertTitle>The restore failed</AlertTitle>
										<AlertDescription>
											{view?.error ?? "No reason was reported."}{" "}
											{view?.mode === "in_place"
												? "The database's live data was not switched over."
												: "The new database is kept for you to inspect or delete."}
										</AlertDescription>
									</Alert>
								) : null}
								{view?.log ? (
									<pre
										className="type-mono max-h-48 overflow-auto rounded-card border border-line bg-surface-sunken p-2 text-2xs text-ink-3"
										role="log"
										aria-label="Restore log"
									>
										{view.log}
									</pre>
								) : null}
							</div>
						) : (
							<>
								<Field>
									<FieldLabel htmlFor="dump-file">Dump file</FieldLabel>
									<Input
										id="dump-file"
										type="file"
										onChange={(e) => void pick(e.target.files?.[0] ?? null)}
									/>
									{file && !fileProblem ? (
										<FieldDescription>
											{formatBytes(file.size)}
											{fileKind ? ` · ${fileKind}` : ""}
										</FieldDescription>
									) : null}
									{fileProblem ? <FieldError>{fileProblem}</FieldError> : null}
								</Field>
								<Field>
									<FieldLabel id="dump-mode">How</FieldLabel>
									<Segmented<Mode>
										aria-label="Restore mode"
										font="sans"
										density="regular"
										value={mode}
										onValueChange={setMode}
										options={[
											{ value: "new", label: "Into a new database" },
											{ value: "in_place", label: "Over this database" },
										]}
									/>
								</Field>
								{mode === "new" ? (
									<>
										<Alert variant="info">
											<AlertTitle>Safe by default</AlertTitle>
											<AlertDescription>
												A new database of the same engine and version is created
												next to this one and the file is restored into it.
												Objects end up owned by the new database's user; roles
												and grants in the file are skipped. This database is not
												touched.
											</AlertDescription>
										</Alert>
										<Field>
											<FieldLabel htmlFor="dump-name">Name</FieldLabel>
											<Input
												id="dump-name"
												autoComplete="off"
												placeholder={`${serviceSlug}-restored-…`}
												value={name}
												onChange={(e) => setName(e.target.value)}
											/>
										</Field>
										<Field>
											<FieldLabel htmlFor="dump-server">Server</FieldLabel>
											<Select
												value={targetServer}
												items={online.map((s) => ({
													value: s.id,
													label: s.name,
												}))}
												onValueChange={(v) => setServer(String(v ?? ""))}
											>
												<SelectTrigger id="dump-server" className="w-full">
													<SelectValue placeholder="Choose an online server" />
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
									</>
								) : (
									<>
										<Alert variant="warning">
											<AlertTitle>This replaces the database's data</AlertTitle>
											<AlertDescription>
												A safety backup is taken first; if it fails nothing is
												changed. The file is restored into a scratch database
												and swapped in only when it restored completely, so a
												broken file leaves the data as it is. Connections of
												your apps are ended at the switch: stop them first if
												that matters.
											</AlertDescription>
										</Alert>
										<Field>
											<FieldLabel htmlFor="dump-destination">
												Safety backup destination
											</FieldLabel>
											<DestinationSelect
												id="dump-destination"
												value={safetyDestination}
												onChange={setDestination}
												items={destinationItems}
											/>
											{destinationItems.length === 0 ? (
												<FieldDescription>
													Add a backup destination in Team settings → Backup
													storage first.
												</FieldDescription>
											) : null}
										</Field>
										<Field>
											<FieldLabel htmlFor="dump-confirm">
												Type <code className="type-mono">{serviceSlug}</code> to
												confirm
											</FieldLabel>
											<Input
												id="dump-confirm"
												autoComplete="off"
												spellCheck={false}
												className="font-mono"
												value={confirm}
												onChange={(e) => setConfirm(e.target.value)}
											/>
										</Field>
									</>
								)}
								{error ? <FieldError>{error}</FieldError> : null}
							</>
						)}
					</DialogBody>
					<DialogFooter>
						{running && !finished ? (
							status === "provisioning" ||
							status === "pre_backup" ||
							status === "awaiting_upload" ? (
								<Button
									type="button"
									variant="ghost"
									disabled={cancel.isPending || transfer === "sending"}
									onClick={() => cancel.mutate()}
								>
									Cancel restore
								</Button>
							) : null
						) : null}
						{retryable && file && restoreId ? (
							<Button
								type="button"
								variant="secondary"
								onClick={() => send(restoreId, file)}
							>
								Try the upload again
							</Button>
						) : null}
						<Button type="button" variant="ghost" onClick={close}>
							{finished
								? "Close"
								: transfer === "sending"
									? "Cancel upload"
									: running
										? "Hide"
										: "Cancel"}
						</Button>
						{running ? null : (
							<Button
								type="submit"
								variant={mode === "in_place" ? "danger-solid" : "primary"}
								disabled={!valid || create.isPending}
							>
								{create.isPending ? <Spinner /> : null}
								{mode === "new"
									? "Restore into new database"
									: "Replace database"}
							</Button>
						)}
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
