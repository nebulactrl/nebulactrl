import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	ArrowUpIcon,
	DownloadIcon,
	FileIcon,
	FolderIcon,
	LinkIcon,
	LockIcon,
	TerminalIcon,
	UploadIcon,
} from "lucide-react";
import { useRef, useState } from "react";
import { errorMessage, isApiError } from "#/api/client-config";
import { setTerminalSettings } from "#/api/gen/sdk.gen";
import type { FileEntry } from "#/api/gen/types.gen";
import { serviceQuery } from "#/api/hooks";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
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
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import {
	Sheet,
	SheetBody,
	SheetContent,
	SheetDescription,
	SheetHeader,
	SheetTitle,
} from "#/components/ui/sheet";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { toast } from "#/components/ui/toast";
import { replicaLabel, stackComponents } from "#/features/stacks/model";
import { formatBytes } from "#/lib/format";
import {
	breadcrumbs,
	bytesLookBinary,
	entryAction,
	joinPath,
	looksBinary,
	modeString,
	parentPath,
	uploadBlocker,
} from "./files-model";
import { fileContentUrl } from "./protocol";
import {
	fetchPreview,
	uploadFile,
	useDirectory,
	useTerminalInfo,
} from "./queries";
import {
	closeFiles,
	openShell,
	useFilesProcess,
	useFilesReplica,
	useFilesTarget,
} from "./store";
import { replicaProcesses, replicasOfProcess, startProcess } from "./targets";

function EntryIcon({ type }: { type: FileEntry["type"] }) {
	if (type === "dir")
		return <FolderIcon aria-hidden className="text-accent-ink" />;
	if (type === "link") return <LinkIcon aria-hidden className="text-ink-3" />;
	return <FileIcon aria-hidden className="text-ink-3" />;
}

interface PreviewTarget {
	path: string;
	name: string;
	size: number;
}

/** A text preview (at most 1 MiB) with the download next to it. */
function PreviewDialog({
	serviceId,
	process,
	replica,
	target,
	previewBytes,
	onClose,
}: {
	serviceId: string;
	process: string;
	replica: number;
	target: PreviewTarget | null;
	previewBytes: number;
	onClose: () => void;
}) {
	const tooBig = target !== null && target.size > previewBytes;
	const blocked = target !== null && (tooBig || looksBinary(target.name));
	const content = useQuery({
		queryKey: ["file-preview", serviceId, process, replica, target?.path],
		enabled: target !== null && !blocked,
		retry: false,
		staleTime: 0,
		gcTime: 0,
		queryFn: async () => {
			const bytes = await fetchPreview(
				fileContentUrl(serviceId, replica, target?.path ?? "", true, process),
			);
			return bytesLookBinary(bytes) ? null : new TextDecoder().decode(bytes);
		},
	});
	const download = target
		? fileContentUrl(serviceId, replica, target.path, false, process)
		: "";

	return (
		<Dialog
			open={target !== null}
			onOpenChange={(open) => {
				if (!open) onClose();
			}}
		>
			<DialogContent size="lg">
				<DialogHeader>
					<DialogTitle className="font-mono">{target?.name}</DialogTitle>
					<DialogDescription className="font-mono">
						{target ? `${target.path} · ${formatBytes(target.size)}` : ""}
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					{blocked ? (
						<p className="text-ink-2 text-sm" role="status">
							{tooBig
								? `Files over ${formatBytes(previewBytes)} are not previewed.`
								: "This looks like a binary file."}{" "}
							Download it instead.
						</p>
					) : content.isPending ? (
						<div
							role="status"
							className="flex items-center gap-2 text-ink-3 text-sm"
						>
							<Spinner /> Reading…
						</div>
					) : content.isError ? (
						<Alert variant="destructive">
							<AlertDescription>{errorMessage(content.error)}</AlertDescription>
						</Alert>
					) : content.data === null ? (
						<p className="text-ink-2 text-sm" role="status">
							This file is not text. Download it instead.
						</p>
					) : (
						<pre className="surface-well max-h-[50vh] overflow-auto overscroll-contain whitespace-pre rounded-card p-3 font-mono text-xs">
							{content.data}
						</pre>
					)}
				</DialogBody>
				<DialogFooter>
					<Button variant="ghost" onClick={onClose}>
						Close
					</Button>
					<a
						href={download}
						download={target?.name}
						className="inline-flex h-[30px] items-center gap-1.5 rounded-ctl bg-white/10 px-3 font-medium text-ink-1 text-xs"
					>
						<DownloadIcon aria-hidden className="size-3.5" />
						Download
					</a>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

/**
 * The file browser of a service: directories and files inside a replica
 * container, text preview, download and (where allowed) upload. Mounted once
 * by `TerminalHost`; opened by `openFiles`.
 */
export function FilesSheet() {
	const serviceId = useFilesTarget();
	const service = useQuery({
		...serviceQuery(serviceId ?? ""),
		enabled: serviceId !== null,
	});
	const info = useTerminalInfo(serviceId);
	const preselected = useFilesReplica();
	const preselectedProcess = useFilesProcess();
	const [pickedProcess, setPickedProcess] = useState<string | null>(null);
	const [picked, setPicked] = useState<number | null>(null);
	const everyReplica = info.data?.replicas ?? [];
	const processes = replicaProcesses(everyReplica);
	// Replicas are numbered inside their process: the process comes first.
	const process =
		processes.length === 0
			? ""
			: (pickedProcess ?? startProcess(everyReplica, preselectedProcess));
	const ofProcess = replicasOfProcess(everyReplica, process);
	const replica = picked ?? preselected ?? 0;
	const components = service.data ? stackComponents(service.data) : [];
	const [path, setPath] = useState("/");
	const [preview, setPreview] = useState<PreviewTarget | null>(null);
	const dir = useDirectory(serviceId, replica, path, process);
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const fileInput = useRef<HTMLInputElement | null>(null);

	const upload = useMutation({
		mutationFn: (file: File) =>
			uploadFile({
				serviceId: serviceId ?? "",
				process,
				replica,
				dir: path,
				file,
			}),
		onSuccess: (res) => {
			toast.success("Uploaded", res.path);
			void queryClient.invalidateQueries({
				queryKey: [{ _id: "listServiceFiles" }],
			});
			void dir.refetch();
		},
		onError: (e) => toast.error("Upload failed", errorMessage(e)),
	});

	const allow = useMutation({
		mutationFn: async () => {
			const { data } = await setTerminalSettings({
				path: { projectId: service.data?.projectId ?? "" },
				body: { allowProductionFileWrites: true },
			});
			return data;
		},
		onSuccess: async () => {
			toast.success("Uploads allowed in production");
			await Promise.all([dir.refetch(), info.refetch()]);
		},
		onError: (e) =>
			toast.error(
				isApiError(e) && e.status === 403
					? "Only an owner or admin can change this"
					: "Could not change the setting",
				errorMessage(e),
			),
	});

	const listing = dir.data;
	const entries = listing?.entries ?? [];
	const maxUpload = info.data?.maxUploadBytes ?? 50 << 20;
	const production = info.data?.production ?? false;

	const pick = (file: File | undefined) => {
		if (!file) return;
		const blocker = uploadBlocker(file, maxUpload);
		if (blocker) {
			toast.error("Cannot upload", blocker);
			return;
		}
		upload.mutate(file);
	};

	const askAllow = async () => {
		await confirm({
			title: "Allow uploads in production?",
			body: "Everyone who can open a terminal in this project's production services will be able to upload files into their containers. Uploads are audited. Files outside volumes vanish at the next deployment.",
			cta: "Allow uploads",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await allow.mutateAsync();
			},
		});
	};

	const open = (e: FileEntry) => {
		const full = joinPath(listing?.path ?? path, e.name);
		const action = entryAction(e);
		if (action === "open") setPath(full);
		else if (action === "preview")
			setPreview({ path: full, name: e.name, size: e.size });
	};

	return (
		<Sheet
			open={serviceId !== null}
			onOpenChange={(next) => {
				if (!next) {
					closeFiles();
					setPath("/");
					setPicked(null);
					setPickedProcess(null);
					setPreview(null);
				}
			}}
		>
			<SheetContent className="sm:max-w-[820px]">
				<SheetHeader className="pb-3">
					<SheetTitle className="pr-10">
						{service.data ? `Files · ${service.data.name}` : "Files"}
					</SheetTitle>
					<SheetDescription className="font-sans">
						The container's own filesystem. Files outside a volume disappear at
						the next deployment.
					</SheetDescription>
				</SheetHeader>
				<SheetBody className="flex min-h-0 flex-col gap-3 overscroll-contain p-3">
					<div className="flex flex-wrap items-center gap-2">
						{processes.length > 1 ? (
							<Select
								value={process}
								items={processes.map((name) => ({ value: name, label: name }))}
								onValueChange={(next) => {
									if (next === null) return;
									setPickedProcess(next);
									setPicked(null);
									setPath("/");
								}}
							>
								<SelectTrigger
									size="sm"
									aria-label="Process"
									className="w-[160px] font-mono"
								>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{processes.map((name) => (
										<SelectItem key={name} value={name} className="font-mono">
											{name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						) : null}
						{ofProcess.length > 1 ? (
							<Select
								value={String(replica)}
								items={ofProcess.map((r) => ({
									value: String(r.index),
									label: replicaLabel(components, r.index),
								}))}
								onValueChange={(v) => {
									setPicked(Number(v));
									setPath("/");
								}}
							>
								<SelectTrigger
									size="sm"
									aria-label={components.length > 0 ? "Component" : "Replica"}
									className="w-[180px]"
								>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{ofProcess.map((r) => (
										<SelectItem
											key={r.index}
											value={String(r.index)}
											disabled={!r.running}
										>
											{replicaLabel(components, r.index)}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						) : null}
						<nav
							aria-label="Path"
							className="flex min-w-0 flex-1 flex-wrap items-center gap-0.5 font-mono text-xs"
						>
							{breadcrumbs(listing?.path ?? path).map((c, i, all) => (
								<span key={c.path} className="flex items-center gap-0.5">
									<button
										type="button"
										className="rounded px-1 py-0.5 text-ink-2 hover:bg-white/5 hover:text-ink-1 focus-visible:focus-ring"
										aria-current={i === all.length - 1 ? "page" : undefined}
										onClick={() => setPath(c.path)}
									>
										{c.label}
									</button>
									{i > 0 && i < all.length - 1 ? (
										<span className="text-ink-3">/</span>
									) : null}
								</span>
							))}
						</nav>
						<Button
							variant="ghost"
							size="sm"
							disabled={(listing?.path ?? path) === "/"}
							onClick={() => setPath(parentPath(listing?.path ?? path))}
						>
							<ArrowUpIcon aria-hidden />
							Up
						</Button>
						{serviceId ? (
							<Button
								variant="ghost"
								size="sm"
								onClick={() => openShell(serviceId, { process, replica })}
							>
								<TerminalIcon aria-hidden />
								Shell
							</Button>
						) : null}
						<input
							ref={fileInput}
							type="file"
							className="sr-only"
							tabIndex={-1}
							aria-hidden
							onChange={(e) => {
								pick(e.target.files?.[0]);
								e.target.value = "";
							}}
						/>
						<Button
							variant="secondary"
							size="sm"
							disabled={!listing?.writable || upload.isPending}
							title={
								listing?.writable
									? `Upload a file here (up to ${formatBytes(maxUpload)})`
									: listing?.readOnlyReason
							}
							onClick={() => fileInput.current?.click()}
						>
							{upload.isPending ? <Spinner /> : <UploadIcon aria-hidden />}
							Upload
						</Button>
					</div>

					{listing && !listing.writable ? (
						<p
							className="flex flex-wrap items-center gap-2 rounded-md border border-line bg-white/5 px-3 py-1.5 text-ink-2 text-xs"
							role="status"
						>
							<LockIcon aria-hidden className="size-3.5" />
							{listing.readOnlyReason ?? "Uploads are off here."}
							{production ? (
								<Button
									variant="ghost"
									size="xs"
									onClick={() => void askAllow()}
								>
									Allow uploads…
								</Button>
							) : null}
						</p>
					) : null}

					{dir.isPending ? (
						<div className="flex flex-col gap-2" role="status">
							<Skeleton className="h-8 rounded-md" />
							<Skeleton className="h-8 rounded-md" />
							<Skeleton className="h-8 rounded-md" />
						</div>
					) : dir.isError ? (
						<ErrorState
							title="This folder did not load"
							body={errorMessage(dir.error)}
							retry={() => void dir.refetch()}
						/>
					) : entries.length === 0 ? (
						<p className="text-ink-3 text-sm" role="status">
							This folder is empty.
						</p>
					) : (
						<div className="min-h-0 flex-1 overflow-auto overscroll-contain">
							<Table>
								<TableHeader>
									<TableRow>
										<TableHead>Name</TableHead>
										<TableHead className="text-right">Size</TableHead>
										<TableHead>Modified</TableHead>
										<TableHead>Mode</TableHead>
										<TableHead className="w-10">
											<span className="sr-only">Download</span>
										</TableHead>
									</TableRow>
								</TableHeader>
								<TableBody>
									{entries.map((e) => (
										<TableRow key={e.name}>
											<TableCell>
												{entryAction(e) === "none" ? (
													<span className="flex items-center gap-2 font-mono text-xs">
														<EntryIcon type={e.type} />
														{e.name}
													</span>
												) : (
													<button
														type="button"
														className="flex items-center gap-2 text-left font-mono text-xs hover:text-accent-ink focus-visible:focus-ring"
														onClick={() => open(e)}
													>
														<EntryIcon type={e.type} />
														{e.name}
														{e.type === "link" && e.linkTarget ? (
															<span className="text-ink-3">
																→ {e.linkTarget}
															</span>
														) : null}
													</button>
												)}
											</TableCell>
											<TableCell className="text-right text-ink-2 text-xs tabular-nums">
												{e.type === "dir" ? "" : formatBytes(e.size)}
											</TableCell>
											<TableCell className="text-ink-2 text-xs">
												{e.modifiedAt ? <Relative iso={e.modifiedAt} /> : ""}
											</TableCell>
											<TableCell className="font-mono text-ink-3 text-xs">
												{modeString(e.mode)}
											</TableCell>
											<TableCell>
												{e.type === "file" ? (
													<a
														href={fileContentUrl(
															serviceId ?? "",
															replica,
															joinPath(listing?.path ?? path, e.name),
															false,
															process,
														)}
														download={e.name}
														aria-label={`Download ${e.name}`}
														className="inline-flex size-7 items-center justify-center rounded-md text-ink-3 hover:bg-white/5 hover:text-ink-1 focus-visible:focus-ring"
													>
														<DownloadIcon aria-hidden className="size-3.5" />
													</a>
												) : null}
											</TableCell>
										</TableRow>
									))}
								</TableBody>
							</Table>
							{listing?.truncated ? (
								<p className="p-2 text-ink-3 text-xs" role="status">
									Only the first {entries.length} entries are listed.
								</p>
							) : null}
						</div>
					)}
				</SheetBody>
				{serviceId ? (
					<PreviewDialog
						serviceId={serviceId}
						process={process}
						replica={replica}
						target={preview}
						previewBytes={info.data?.previewBytes ?? 1 << 20}
						onClose={() => setPreview(null)}
					/>
				) : null}
			</SheetContent>
		</Sheet>
	);
}
